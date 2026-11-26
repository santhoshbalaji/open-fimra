package main

import (
	"context"
	"crypto/ed25519"
	_ "embed"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	extract "github.com/santhoshbalaji/open-fimra/packages/state-extract"
)

//go:embed demo_replay_check.sql
var operationsReplayCheckSQL string

func checkOperationsReplay(ctx context.Context, tx pgx.Tx, workspace, day, stateCode string) error {
	var failures []string
	if err := tx.QueryRow(ctx, operationsReplayCheckSQL, workspace, day).Scan(&failures); err != nil {
		return fmt.Errorf("check operations replay: %w", err)
	}
	if len(failures) > 0 {
		return fmt.Errorf("operations replay did not match the reference: %v", failures)
	}
	var raw, public []byte
	err := tx.QueryRow(ctx, `SELECT r.state_document,k.public_key FROM exports e
JOIN export_rows r ON r.export_id=e.id AND r.ordinal=1
JOIN state_extract_keys k ON k.workspace_id=e.workspace_id AND k.key_id=convert_from(r.state_document,'UTF8')::jsonb->>'keyId'
WHERE e.workspace_id=$1 AND e.kind='state_extract' AND e.status='ready'`, workspace).Scan(&raw, &public)
	if err != nil {
		return err
	}
	if _, err := extract.Validate(raw, ed25519.PublicKey(public), time.Now()); err != nil {
		return fmt.Errorf("validate the signed aggregate: %w", err)
	}
	var signed extract.Envelope
	var doc extract.Document
	if err := json.Unmarshal(raw, &signed); err != nil {
		return err
	}
	if err := json.Unmarshal(signed.Payload, &doc); err != nil {
		return err
	}
	want := map[string]string{"D1": extract.Reported, "D2": extract.Reported, "D3": extract.Zero, "D4": extract.NotReported}
	if doc.Coverage == nil || doc.Coverage.Expected != 4 || doc.Coverage.Reported != 3 || doc.Coverage.Status != extract.Incomplete ||
		doc.Authority.StateCode != stateCode || doc.Window.From != day || len(doc.Coverage.Units) != len(want) {
		return fmt.Errorf("unexpected aggregate coverage: %+v", doc.Coverage)
	}
	for _, unit := range doc.Coverage.Units {
		if unit.Status != want[unit.Code] {
			return fmt.Errorf("unexpected aggregate coverage for %s: %s", unit.Code, unit.Status)
		}
	}
	if doc.Totals == nil || doc.Totals.Detections["records"] == 0 || doc.Totals.Alerts["routed"] == 0 {
		return fmt.Errorf("the approved return has no detections or routed alerts")
	}
	return nil
}
