package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func stateExtractDemo(ctx context.Context, cfg config) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cipher, err := readSettingsCipher()
	if err != nil {
		return err
	}
	if cipher == nil {
		return errSecretsOff
	}
	pool, err := openPool(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	const slug, reviewer = "demo", "d0000000-0000-4000-8000-0000000000a1"
	const officer = "d0000000-0000-4000-8000-0000000000a2"
	return pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		window, err := loadDailyWindow(ctx, tx, reviewer, slug, "")
		if err != nil {
			return err
		}
		ws := window.WorkspaceID
		authority, err := readStateAuthority(ctx, tx, ws)
		if errors.Is(err, pgx.ErrNoRows) {
			_, err = tx.Exec(ctx, `INSERT INTO state_extract_authorities VALUES($1,'XS-DEMO','demo-forest-units')`, ws)
			if err != nil {
				return err
			}
			if _, err := generateStateSigningKey(ctx, tx, cipher, ws, reviewer); err != nil {
				return err
			}
			authority, err = readStateAuthority(ctx, tx, ws)
		}
		if err != nil {
			return err
		}
		if authority.StateCode != "XS-DEMO" {
			return errors.New("the state extract replay requires the fictional demo authority")
		}
		request := stateExtractRequest{From: window.Day, To: window.Day}
		draft, rev, chain, err := prepareStateExtract(ctx, tx, ws, request, authority)
		if err != nil || rev.Revision > 1 {
			return err
		}
		if _, err := storeStateExtract(ctx, tx, cipher, ws, reviewer, chain, rev, draft.Documents); err != nil {
			return err
		}
		var unit dailyExpectedUnit
		err = tx.QueryRow(ctx, `SELECT path::text,code,name,layer_version_id::text
FROM daily_return_units($1,$2,$3) WHERE path='ds.c1.d2'`, window.HierarchyID, window.Depth, window.Due).
			Scan(&unit.Path, &unit.Code, &unit.Name, &unit.BoundaryVersion)
		if err != nil {
			return err
		}
		id, err := submitDailyReturn(ctx, tx, window, officer, "Fictional officer", unit, dailyRequest{Nil: true}, dailyRevision{})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO daily_return_decisions(report_id,decision,decided_by,decided_name,note)
VALUES($1,'approved',$2,'Fictional reviewer','')`, id, reviewer); err != nil {
			return err
		}
		draft, rev, chain, err = prepareStateExtract(ctx, tx, ws, request, authority)
		if err != nil {
			return err
		}
		correction, err := storeStateExtract(ctx, tx, cipher, ws, reviewer, chain, rev, draft.Documents)
		if err == nil {
			fmt.Printf("Fictional state return %s, revision %d. Open Reports → State return for both receipts.\n", window.Day, correction.Revision)
		}
		return err
	})
}
