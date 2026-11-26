package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	extract "github.com/santhoshbalaji/open-fimra/packages/state-extract"
)

const nationalExtractLimit = 8 << 20

// Both external submission routes accept the signed JSON as the request body.
// The state is taken from the document and must match the caller's grant and
// a receiving workspace's independently registered key.
func (a api) nationalReceive(w http.ResponseWriter, r *http.Request) {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, nationalExtractLimit))
	if err != nil {
		writeError(w, 413, "The extract could not be read or is too large.")
		return
	}
	a.nationalOperation(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		return receiveNationalExtract(ctx, tx, nationalCaller(r), raw, "")
	})
}

// A local state workspace can push an already-approved document directly from
// its immutable export history. The receiving key still has to be registered.
func (a api) nationalLocalPush(w http.ResponseWriter, r *http.Request) {
	var input struct {
		Destination string `json:"destination"`
	}
	source := chi.URLParam(r, "slug")
	id, kind := chi.URLParam(r, "id"), chi.URLParam(r, "kind")
	if !stateExtractID.MatchString(id) || (kind != "incident" && kind != "preparedness") ||
		!decodeDaily(w, r, &input) || input.Destination == "" || len(input.Destination) > 100 {
		writeError(w, 400, "Check the approved extract and receiving workspace.")
		return
	}
	a.nationalOperation(w, r, func(ctx context.Context, tx pgx.Tx) (int, any, error) {
		caller := nationalActor{input.Destination, nationalCaller(r).subject}
		ordinal := 1
		if kind == "preparedness" {
			ordinal = 2
		}
		var raw []byte
		err := tx.QueryRow(ctx, `SELECT row.state_document FROM exports e JOIN export_rows row ON row.export_id=e.id
JOIN workspaces w ON w.id=e.workspace_id
WHERE e.id=$1 AND w.slug=$2 AND e.kind='state_extract' AND e.status='ready' AND row.ordinal=$3
AND EXISTS (SELECT FROM permanent_alert_grants g WHERE g.workspace_id=w.id AND g.oidc_subject=$4
    AND g.role_slug='state_admin' AND g.depth=0)`, id, source, ordinal, caller.subject).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			err = errNationalScope
		}
		if err != nil {
			return 0, nil, err
		}
		return receiveNationalExtract(ctx, tx, caller, raw, source)
	})
}

func receiveNationalExtract(ctx context.Context, tx pgx.Tx, caller nationalActor, raw []byte, localSource string) (int, any, error) {
	var envelope extract.Envelope
	var doc extract.Document
	if len(raw) > nationalExtractLimit || json.Unmarshal(raw, &envelope) != nil ||
		json.Unmarshal(envelope.Payload, &doc) != nil || !stateExtractCode.MatchString(doc.Authority.StateCode) {
		return 0, nil, dailyError(422, "The signed state extract is invalid.")
	}

	var state int64
	var workspace, unitSystem string
	var key []byte
	err := tx.QueryRow(ctx, `SELECT s.id,s.workspace_id::text,s.unit_code_system,k.public_key FROM national_states s
JOIN workspaces w ON w.id=s.workspace_id
LEFT JOIN national_state_keys k ON k.state_id=s.id AND k.key_id=$4
WHERE w.slug=$1 AND s.state_code=$3 AND (
    ($5='' AND EXISTS (SELECT FROM national_state_grants g WHERE g.state_id=s.id
        AND g.oidc_subject=$2 AND g.role_slug='national_nodal'))
    OR ($5<>'' AND EXISTS (SELECT FROM state_extract_authorities a JOIN workspaces source ON source.id=a.workspace_id
        WHERE source.slug=$5 AND a.state_code=s.state_code AND a.unit_code_system=s.unit_code_system)))
FOR UPDATE OF s`, caller.slug, caller.subject, doc.Authority.StateCode, envelope.KeyID, localSource).
		Scan(&state, &workspace, &unitSystem, &key)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, errNationalScope
	}
	if err != nil {
		return 0, nil, err
	}
	if key == nil || unitSystem != doc.Authority.UnitCodeSystem {
		return 0, nil, dailyError(422, "The signing authority is not trusted for this state.")
	}
	now := time.Now()
	receipt, err := extract.Validate(raw, key, now)
	if err != nil || doc.Window.EndsAt.After(now) {
		return 0, nil, dailyError(422, "The signed state extract failed verification.")
	}
	window, err := nationalWindowID(ctx, tx, state, nationalWindow{doc.Window.From, doc.Window.To}, true)
	if err != nil {
		return 0, nil, err
	}

	var saved extract.Receipt
	err = tx.QueryRow(ctx, `SELECT receipt FROM national_extracts WHERE window_id=$1 AND kind=$2 AND revision=$3`,
		window, doc.Kind, doc.Revision).Scan(&saved)
	if err == nil {
		if saved.SHA256 != receipt.SHA256 {
			return 0, nil, dailyError(409, "This revision already has a different approved extract.")
		}
		return 200, saved, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return 0, nil, err
	}

	var counterpartMatches bool
	err = tx.QueryRow(ctx, `SELECT NOT EXISTS (SELECT FROM national_extracts WHERE window_id=$1 AND kind<>$2 AND revision=$3
AND (id<>$4 OR supersedes IS DISTINCT FROM $5::uuid OR payload->'window'<>($6::jsonb)->'window'))`,
		window, doc.Kind, doc.Revision, doc.ID, doc.Supersedes, envelope.Payload).Scan(&counterpartMatches)
	if err != nil {
		return 0, nil, err
	}
	if !counterpartMatches {
		return 0, nil, dailyError(409, "The incident and preparedness files disagree about the approval or reporting window.")
	}

	_, err = tx.Exec(ctx, `INSERT INTO national_extracts(window_id,state_id,kind,id,revision,supersedes,signed_document,payload,receipt)
VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, window, state, doc.Kind, doc.ID, doc.Revision, doc.Supersedes, raw, envelope.Payload, receipt)
	if err != nil {
		return 0, nil, err
	}
	if err = nationalAudit(ctx, tx, workspace, caller.subject, "receive", "national_extract", doc.ID, map[string]any{
		"state": doc.Authority.StateCode, "kind": doc.Kind, "revision": doc.Revision,
	}); err != nil {
		return 0, nil, err
	}
	return 201, receipt, nil
}
