package main

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	extract "github.com/santhoshbalaji/open-fimra/packages/state-extract"
)

const stateSigningSecret = "state_extract_signing_key"

var (
	stateExtractCode = regexp.MustCompile(`^[A-Z][A-Z0-9-]{1,31}$`)
	stateUnitSystem  = regexp.MustCompile(`^[a-z][a-z0-9-]{1,31}$`)
	stateUnitCode    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._/-]{0,63}$`)
	stateExtractID   = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

type stateExtractRevision struct {
	ID         string            `json:"id"`
	Revision   int               `json:"revision"`
	Supersedes *string           `json:"supersedes"`
	From       string            `json:"from"`
	To         string            `json:"to"`
	Current    bool              `json:"current"`
	Receipts   []extract.Receipt `json:"receipts"`
}

type stateExtractRequest struct {
	Action string `json:"action"`
	From   string `json:"from"`
	To     string `json:"to"`
	Digest string `json:"digest"`
}

type stateExtractPreview struct {
	Documents stateExtractPair `json:"documents"`
	Digest    string           `json:"digest"`
}

func (a api) stateExtractAccess(w http.ResponseWriter, r *http.Request, tx pgx.Tx) (string, string, error) {
	w.Header().Set("Cache-Control", "private, no-store")
	subject := r.Context().Value(identityKey{}).(identity).Subject
	var workspace string
	err := tx.QueryRow(r.Context(), `SELECT workspace_id::text FROM permanent_alert_grants
WHERE slug=$1 AND oidc_subject=$2 AND role_slug='state_admin' AND depth=0 LIMIT 1`,
		chi.URLParam(r, "slug"), subject).Scan(&workspace)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", dailyError(403, "A workspace administrator role at the root is required.")
	}
	return workspace, subject, err
}

func (a api) stateExtractFailure(w http.ResponseWriter, err error) {
	var problem *reportProblem
	var dbErr *pgconn.PgError
	switch {
	case errors.As(err, &problem):
		writeError(w, problem.status, problem.message)
	case errors.As(err, &dbErr) && (dbErr.Code == "40001" || dbErr.Code == "23505" || dbErr.Code == "23P01"):
		writeError(w, 409, "The reporting window or authority changed. Reload and try again.")
	default:
		a.logger.Error("Could not process the state extract", "error", err)
		writeError(w, 500, "Could not process the state extract.")
	}
}

func readStateAuthority(ctx context.Context, tx pgx.Tx, workspace string) (extract.Authority, error) {
	authority := extract.Authority{ApproverRole: "state_admin"}
	err := tx.QueryRow(ctx, `SELECT state_code,unit_code_system FROM state_extract_authorities WHERE workspace_id=$1`,
		workspace).Scan(&authority.StateCode, &authority.UnitCodeSystem)
	return authority, err
}

func (a api) stateExtractSettings(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Action         string `json:"action"`
		StateCode      string `json:"stateCode"`
		UnitCodeSystem string `json:"unitCodeSystem"`
	}
	if !decodeDaily(w, r, &in) || (in.Action != "configure" && in.Action != "rotate") ||
		(in.Action == "configure" && (!stateExtractCode.MatchString(in.StateCode) || !stateUnitSystem.MatchString(in.UnitCodeSystem))) {
		writeError(w, 400, "Check the state return authority and unit code system.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var result string
	err := pgx.BeginFunc(ctx, a.pool, func(tx pgx.Tx) error {
		workspace, subject, err := a.stateExtractAccess(w, r.WithContext(ctx), tx)
		if err != nil {
			return err
		}
		if a.secrets == nil {
			return dailyError(503, "Turn on encrypted settings before configuring a signing key.")
		}
		if in.Action == "configure" {
			if _, err := tx.Exec(ctx, `INSERT INTO state_extract_authorities(workspace_id,state_code,unit_code_system) VALUES($1,$2,$3)`,
				workspace, in.StateCode, in.UnitCodeSystem); err != nil {
				return err
			}
		}
		if _, err := readStateAuthority(ctx, tx, workspace); errors.Is(err, pgx.ErrNoRows) {
			return dailyError(409, "Configure the producing authority first.")
		} else if err != nil {
			return err
		}
		result, err = generateStateSigningKey(ctx, tx, a.secrets, workspace, subject)
		return err
	})
	if err != nil {
		a.stateExtractFailure(w, err)
		return
	}
	writeJSON(w, 201, map[string]string{"keyId": result})
}

func generateStateSigningKey(ctx context.Context, tx pgx.Tx, cipher *settingsCipher, workspace, subject string) (string, error) {
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		return "", err
	}
	defer clear(private)
	id := extract.KeyID(public)
	if _, err := tx.Exec(ctx, `INSERT INTO state_extract_keys(workspace_id,key_id,public_key) VALUES($1,$2,$3)`, workspace, id, []byte(public)); err != nil {
		return "", err
	}
	if err := saveSecret(ctx, tx, cipher, workspace, stateSigningSecret, base64.StdEncoding.EncodeToString(private.Seed()), subject); err != nil {
		return "", err
	}
	return id, auditStateExtract(ctx, tx, workspace, subject, "key", id, nil)
}

func auditStateExtract(ctx context.Context, tx pgx.Tx, workspace, subject, action, id string, details any) error {
	_, err := tx.Exec(ctx, `INSERT INTO audit_log(workspace_id,actor_kind,actor_id,action,target_type,target_id,details)
VALUES($1,'user',$2,'state_extract.' || $3,'state_extract',$4,coalesce($5::jsonb,'{}'))`, workspace, subject, action, id, details)
	return err
}

func (a api) stateExtracts(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	var result any
	var in stateExtractRequest
	status := 200
	if r.Method == http.MethodPost {
		if !decodeDaily(w, r, &in) || !dailyDate(in.From) || !dailyDate(in.To) || in.To < in.From ||
			(in.Action != "preview" && (in.Action != "approve" || len(in.Digest) != 64)) {
			writeError(w, 400, "Check the state return dates and action.")
			return
		}
		// Keep the number of unit-days and response size bounded.
		from, _ := time.Parse(time.DateOnly, in.From)
		to, _ := time.Parse(time.DateOnly, in.To)
		if to.Sub(from) >= 366*24*time.Hour {
			writeError(w, 400, "Choose a window of at most 366 days.")
			return
		}
	}
	err := pgx.BeginTxFunc(ctx, a.pool, pgx.TxOptions{IsoLevel: pgx.RepeatableRead}, func(tx pgx.Tx) error {
		workspace, subject, err := a.stateExtractAccess(w, r.WithContext(ctx), tx)
		if err != nil {
			return err
		}
		if r.Method == http.MethodGet {
			result, err = stateExtractHistory(ctx, tx, workspace)
			return err
		}
		authority, err := readStateAuthority(ctx, tx, workspace)
		if errors.Is(err, pgx.ErrNoRows) {
			return dailyError(409, "Configure the producing authority first.")
		}
		if err != nil {
			return err
		}
		preview, revision, window, err := prepareStateExtract(ctx, tx, workspace, in, authority)
		if err != nil {
			return err
		}
		result = preview
		if in.Action == "preview" {
			return nil
		}
		if in.Digest != preview.Digest {
			return dailyError(409, "The state return changed. Preview it again before signing.")
		}
		revision, err = storeStateExtract(ctx, tx, a.secrets, workspace, subject, window, revision, preview.Documents)
		status, result = 201, revision
		return err
	})
	if err != nil {
		a.stateExtractFailure(w, err)
		return
	}
	writeJSON(w, status, result)
}

func stateExtractHistory(ctx context.Context, tx pgx.Tx, workspace string) (any, error) {
	authority, err := readStateAuthority(ctx, tx, workspace)
	if errors.Is(err, pgx.ErrNoRows) {
		return map[string]any{"items": []stateExtractRevision{}}, nil
	}
	if err != nil {
		return nil, err
	}
	rows, err := tx.Query(ctx, `SELECT e.id::text,e.state_revision,e.state_supersedes::text,lower(w.days)::text,(upper(w.days)-1)::text,
NOT EXISTS (SELECT FROM exports n WHERE n.state_supersedes=e.id AND n.status='ready'),e.state_receipt
FROM state_extract_windows w JOIN exports e ON e.state_window=w.id WHERE w.workspace_id=$1 AND e.status='ready'
ORDER BY e.created_at DESC,e.state_revision DESC,e.id`, workspace)
	if err != nil {
		return nil, err
	}
	items, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (stateExtractRevision, error) {
		var item stateExtractRevision
		err := row.Scan(&item.ID, &item.Revision, &item.Supersedes, &item.From, &item.To, &item.Current, &item.Receipts)
		return item, err
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"authority": authority, "items": items}, nil
}

func prepareStateExtract(ctx context.Context, tx pgx.Tx, workspace string, in stateExtractRequest,
	authority extract.Authority) (stateExtractPreview, stateExtractRevision, int64, error) {
	rev := stateExtractRevision{Revision: 1, From: in.From, To: in.To, Current: true}
	var windowID int64
	var same bool
	err := tx.QueryRow(ctx, `SELECT id,days=daterange($2::date,$3::date,'[]') FROM state_extract_windows
WHERE workspace_id=$1 AND days && daterange($2::date,$3::date,'[]') LIMIT 1`, workspace, in.From, in.To).Scan(&windowID, &same)
	if err == nil && !same {
		return stateExtractPreview{}, rev, 0, dailyError(409, "A state extract already covers part of this window.")
	}
	if err == nil {
		err = tx.QueryRow(ctx, `SELECT id::text,state_revision+1 FROM exports
WHERE state_window=$1 AND status='ready' ORDER BY state_revision DESC LIMIT 1`, windowID).Scan(&rev.Supersedes, &rev.Revision)
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return stateExtractPreview{}, rev, 0, err
	}
	docs, err := buildStateExtract(ctx, tx, workspace, in.From, in.To, authority)
	if err != nil {
		return stateExtractPreview{}, rev, 0, err
	}
	for _, doc := range []*extract.Document{&docs.Incident, &docs.Preparedness} {
		doc.ID = "00000000-0000-4000-8000-000000000000"
		doc.Revision, doc.Supersedes = rev.Revision, rev.Supersedes
		doc.GeneratedAt = doc.Window.EndsAt
		if _, err := extract.Preview(*doc); err != nil {
			return stateExtractPreview{}, rev, 0, dailyError(422, "The figures failed state return validation.")
		}
		doc.ID, doc.GeneratedAt = "", time.Time{}
	}
	raw, err := json.Marshal(docs)
	return stateExtractPreview{docs, extract.Digest(raw)}, rev, windowID, err
}

func storeStateExtract(ctx context.Context, tx pgx.Tx, cipher *settingsCipher, workspace, subject string,
	window int64, rev stateExtractRevision, docs stateExtractPair) (stateExtractRevision, error) {
	if cipher == nil {
		return rev, dailyError(503, "Turn on encrypted settings before signing.")
	}
	encoded, found, err := loadSecret(ctx, tx, cipher, workspace, stateSigningSecret)
	if err != nil {
		return rev, err
	}
	if !found {
		return rev, dailyError(409, "Configure the state return signing key first.")
	}
	seed, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(seed) != ed25519.SeedSize {
		return rev, errors.New("invalid stored state signing key")
	}
	private := ed25519.NewKeyFromSeed(seed)
	clear(seed)
	defer clear(private)
	if window == 0 {
		if err := tx.QueryRow(ctx, `INSERT INTO state_extract_windows(workspace_id,days) VALUES($1,daterange($2::date,$3::date,'[]')) RETURNING id`,
			workspace, rev.From, rev.To).Scan(&window); err != nil {
			return rev, err
		}
	}
	err = tx.QueryRow(ctx, `INSERT INTO exports(workspace_id,requested_by,kind,format,filters,requester_roles,row_count,
expires_at,state_window,state_revision,state_supersedes)
VALUES($1,$2,'state_extract','json','{}','["state_admin"]',2,'infinity',$3,$4,$5::uuid) RETURNING id::text`,
		workspace, subject, window, rev.Revision, rev.Supersedes).Scan(&rev.ID)
	if err != nil {
		return rev, err
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	var files [][]byte
	for _, doc := range []extract.Document{docs.Incident, docs.Preparedness} {
		doc.ID, doc.GeneratedAt = rev.ID, now
		raw, err := extract.Sign(doc, private)
		if err != nil {
			return rev, err
		}
		receipt, err := extract.Validate(raw, private.Public().(ed25519.PublicKey), now)
		if err != nil {
			return rev, err
		}
		files = append(files, raw)
		rev.Receipts = append(rev.Receipts, receipt)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO export_rows(export_id,ordinal,workspace_id,state_document)
VALUES($1,1,$2,$3),($1,2,$2,$4)`, rev.ID, workspace, files[0], files[1]); err != nil {
		return rev, err
	}
	if _, err := tx.Exec(ctx, `UPDATE exports SET status='ready',finished_at=$2,state_receipt=$3 WHERE id=$1`,
		rev.ID, now, rev.Receipts); err != nil {
		return rev, err
	}
	return rev, auditStateExtract(ctx, tx, workspace, subject, "approve", rev.ID, rev)
}

func (a api) stateExtractFile(w http.ResponseWriter, r *http.Request) {
	id, kind := chi.URLParam(r, "id"), chi.URLParam(r, "kind")
	if !stateExtractID.MatchString(id) || (kind != "incident" && kind != "preparedness") {
		writeError(w, 404, "The state extract was not found.")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	var raw []byte
	err := pgx.BeginFunc(ctx, a.pool, func(tx pgx.Tx) error {
		workspace, subject, err := a.stateExtractAccess(w, r.WithContext(ctx), tx)
		if err != nil {
			return err
		}
		ordinal := 1
		if kind == "preparedness" {
			ordinal = 2
		}
		err = tx.QueryRow(ctx, `SELECT r.state_document FROM exports e JOIN export_rows r ON r.export_id=e.id
WHERE e.id=$1 AND e.workspace_id=$2 AND e.kind='state_extract' AND e.status='ready' AND r.ordinal=$3`, id, workspace, ordinal).Scan(&raw)
		if errors.Is(err, pgx.ErrNoRows) {
			return dailyError(404, "The state extract was not found.")
		}
		if err != nil {
			return err
		}
		return auditStateExtract(ctx, tx, workspace, subject, "download", id, map[string]string{"kind": kind})
	})
	if err != nil {
		a.stateExtractFailure(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="state-`+kind+"-"+id+`.json"`)
	_, _ = w.Write(raw)
}

func (a api) stateExtractValidate(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 8<<20))
	var envelope extract.Envelope
	if err != nil || json.Unmarshal(raw, &envelope) != nil {
		writeError(w, 400, "Choose a signed JSON state extract.")
		return
	}
	var receipt extract.Receipt
	err = pgx.BeginFunc(ctx, a.pool, func(tx pgx.Tx) error {
		workspace, subject, err := a.stateExtractAccess(w, r.WithContext(ctx), tx)
		if err != nil {
			return err
		}
		var public []byte
		var stateCode string
		err = tx.QueryRow(ctx, `SELECT k.public_key,a.state_code FROM state_extract_keys k
JOIN state_extract_authorities a USING (workspace_id) WHERE k.workspace_id=$1 AND k.key_id=$2`,
			workspace, envelope.KeyID).Scan(&public, &stateCode)
		if errors.Is(err, pgx.ErrNoRows) {
			return dailyError(422, "The extract signing key is unknown in this workspace.")
		}
		if err != nil {
			return err
		}
		receipt, err = extract.Validate(raw, ed25519.PublicKey(public), time.Now())
		if err != nil {
			return dailyError(422, err.Error())
		}
		var doc extract.Document
		if err := json.Unmarshal(envelope.Payload, &doc); err != nil || doc.Authority.StateCode != stateCode {
			return dailyError(422, "The extract is for a different state.")
		}
		return auditStateExtract(ctx, tx, workspace, subject, "validate", receipt.ExtractID, receipt)
	})
	if err != nil {
		a.stateExtractFailure(w, err)
		return
	}
	writeJSON(w, 200, receipt)
}

func (a api) stateExtractPublicKey(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	id := chi.URLParam(r, "keyId")
	var public []byte
	var stateCode string
	err := pgx.BeginFunc(ctx, a.pool, func(tx pgx.Tx) error {
		workspace, _, err := a.stateExtractAccess(w, r.WithContext(ctx), tx)
		if err != nil {
			return err
		}
		err = tx.QueryRow(ctx, `SELECT k.public_key,a.state_code FROM state_extract_keys k
JOIN state_extract_authorities a USING (workspace_id) WHERE k.workspace_id=$1 AND k.key_id=$2`, workspace, id).Scan(&public, &stateCode)
		if errors.Is(err, pgx.ErrNoRows) {
			return dailyError(404, "The state signing key was not found.")
		}
		return err
	})
	if err != nil {
		a.stateExtractFailure(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"stateCode": stateCode, "keyId": id, "publicKey": public})
}
