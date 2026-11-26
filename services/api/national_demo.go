package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	extract "github.com/santhoshbalaji/open-fimra/packages/state-extract"
)

const (
	nationalDemoSlug  = "national-demo"
	nationalDemoNodal = "d1000000-0000-4000-8000-0000000000a4"
	nationalDemoNorth = "d1000000-0000-4000-8000-0000000000a6"
)

func nationalDemo(ctx context.Context, cfg config) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	pool, err := openPool(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtext('open-fimra:national-demo'))`); err != nil {
			return err
		}
		var exists bool
		err := tx.QueryRow(ctx, `SELECT EXISTS (SELECT FROM workspaces WHERE slug=$1)`, nationalDemoSlug).Scan(&exists)
		if err != nil || exists {
			return err
		}
		if err := seedNationalWorkspace(ctx, tx, nationalDemoSlug); err != nil {
			return err
		}
		for _, fixture := range []struct {
			state, actor string
			examples     []string
		}{
			{"XS-DEMO", nationalDemoNodal, []string{"incident", "incident-correction", "preparedness-original", "preparedness-correction"}},
			{"XS-NORTH", nationalDemoNorth, []string{"incident-north"}},
		} {
			public, private, err := ed25519.GenerateKey(nil)
			if err != nil {
				return err
			}
			err = seedNationalReturns(ctx, tx, nationalActor{nationalDemoSlug, fixture.actor}, fixture.state, fixture.examples, public, private)
			clear(private)
			if err != nil {
				return err
			}
		}
		raw, err := os.ReadFile("../../seed/national-replay.sql")
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, string(raw))
		return err
	})
}

func seedNationalWorkspace(ctx context.Context, tx pgx.Tx, slug string) error {
	raw, err := os.ReadFile("../../seed/national.sql")
	if err != nil {
		return err
	}
	// The integration test uses its own synthetic slug.
	if !layerSlug.MatchString(slug) {
		return errors.New("invalid fictional national workspace slug")
	}
	_, err = tx.Exec(ctx, strings.ReplaceAll(string(raw), nationalDemoSlug, slug))
	return err
}

func seedNationalReturns(ctx context.Context, tx pgx.Tx, caller nationalActor, state string, examples []string, public ed25519.PublicKey, private ed25519.PrivateKey) error {
	if _, err := tx.Exec(ctx, `INSERT INTO national_state_keys (state_id,key_id,public_key)
SELECT s.id,$3,$4 FROM national_states s JOIN workspaces w ON w.id=s.workspace_id WHERE w.slug=$1 AND s.state_code=$2`,
		caller.slug, state, extract.KeyID(public), []byte(public)); err != nil {
		return err
	}
	for _, example := range examples {
		doc, err := nationalExample(example)
		if err != nil {
			return err
		}
		signed, err := extract.Sign(doc, private)
		if err != nil {
			return err
		}
		if _, _, err := receiveNationalExtract(ctx, tx, caller, signed, ""); err != nil {
			return err
		}
	}
	return nil
}

func nationalExample(name string) (extract.Document, error) {
	fixture := name
	switch name {
	case "incident-north":
		fixture = "incident-correction"
	case "preparedness-original":
		fixture = "preparedness-correction"
	}
	raw, err := os.ReadFile("../../packages/state-extract/examples/" + fixture + ".json")
	if err != nil {
		return extract.Document{}, err
	}
	var envelope extract.Envelope
	var doc extract.Document
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return doc, err
	}
	if err := json.Unmarshal(envelope.Payload, &doc); err != nil {
		return doc, err
	}
	switch name {
	case "incident-north":
		doc.Authority.StateCode = "XS-NORTH"
		doc.ID, doc.Revision, doc.Supersedes = "acc94e2b-b93b-4fb7-a192-179084fdd045", 1, nil
		doc.Totals.Incidents["new"] = 7
		doc.Totals.Incidents["active"] = 7
	case "preparedness-original":
		doc.ID, doc.Revision, doc.Supersedes = *doc.Supersedes, 1, nil
	}
	return doc, nil
}
