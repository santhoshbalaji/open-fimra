package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/santhoshbalaji/open-fimra/services/api/internal/firms"
)

func incidentResponseDemo(ctx context.Context, cfg config) error {
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	pool, err := openPool(ctx, cfg.databaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	if err := ensureDemoSeed(ctx, pool); err != nil {
		return err
	}
	const slug, subject = "demo", "d0000000-0000-4000-8000-0000000000a1"
	const radioID = "de310000-0000-4000-8000-000000000001"
	var mainID, newID int64
	err = pgx.BeginFunc(ctx, pool, func(tx pgx.Tx) error {
		access, err := formAccess(ctx, tx, subject, slug)
		if err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, detectionWriteLock); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, incidentGroupingLock, access.workspaceID); err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, "SELECT EXISTS (SELECT FROM fire_intake WHERE workspace_id=$1 AND request_id=$2)",
			access.workspaceID, radioID).Scan(&exists); err != nil || exists {
			return err
		}
		if _, err := tx.Exec(ctx, "SET LOCAL jit=off"); err != nil {
			return err
		}
		if _, err := selectResponseWorkflow(ctx, tx, access, subject, true); err != nil {
			return err
		}
		// These observations are spaced away from the regular demo replay.
		now := time.Now().UTC().Truncate(time.Minute)
		radioLon, radioLat := 86.295, 16.420
		radio, err := saveIntake(ctx, tx, access, subject, slug, intakeRequest{ID: radioID, Source: "radio",
			ObservedAt: now.Add(-90 * time.Hour), Longitude: &radioLon, Latitude: &radioLat,
			Note: "Ridge crew saw smoke from the lookout."}, nil, "demo")
		if err != nil {
			return err
		}
		if err := tx.QueryRow(ctx, "SELECT incident_id FROM incident_members WHERE alert_id=$1", radio).Scan(&mainID); err != nil {
			return err
		}
		run := ingestRun{Source: "demo_response", Product: demoProduct, Kind: runImport, WorkspaceID: &access.workspaceID}
		_, err = runIngest(ctx, tx, run, func(context.Context) ([]firms.Observation, error) {
			return []firms.Observation{
				{Satellite: "N", Instrument: "VIIRS", ObservedAt: now.Add(-88 * time.Hour), Longitude: 86.2988, Latitude: 16.420},
				{Satellite: "N", Instrument: "VIIRS", ObservedAt: now.Add(-86 * time.Hour), Longitude: 86.295, Latitude: 16.4056},
			}, nil
		})
		if err != nil {
			return err
		}
		var nearby, remote int64
		var nearbyCase, remoteCase int64
		for _, observation := range []struct {
			at                  time.Time
			alertID, incidentID *int64
		}{{now.Add(-88 * time.Hour), &nearby, &nearbyCase}, {now.Add(-86 * time.Hour), &remote, &remoteCase}} {
			if err := tx.QueryRow(ctx, `SELECT m.alert_id,m.incident_id FROM incident_members m
JOIN alerts a ON a.id=m.alert_id JOIN detections d ON (d.id,d.observed_at)=(a.detection_id,a.observed_at)
WHERE a.workspace_id=$1 AND d.source=$2 AND a.observed_at=$3`,
				access.workspaceID, run.Source, observation.at).Scan(observation.alertID, observation.incidentID); err != nil {
				return err
			}
		}
		if nearbyCase != mainID || remoteCase == mainID {
			return fmt.Errorf("synthetic alert grouping did not match the demo")
		}
		merge, err := readMembershipImpact(ctx, tx, access.workspaceID, subject, slug, remoteCase, mainID, nil)
		if err != nil {
			return err
		}
		if _, err := saveMembershipChange(ctx, tx, access, subject, remoteCase, mainID,
			membershipRequest{Operation: "merge", Reason: "Both crews reported a fire along the access track."}, merge); err != nil {
			return err
		}
		split, err := readMembershipImpact(ctx, tx, access.workspaceID, subject, slug, mainID, 0,
			[]string{strconv.FormatInt(remote, 10)})
		if err != nil {
			return err
		}
		receipt, err := saveMembershipChange(ctx, tx, access, subject, mainID, 0,
			membershipRequest{Operation: "split", Reason: "The site survey confirmed two separate fires."}, split)
		if err != nil {
			return err
		}
		var result struct {
			TargetID int64 `json:"targetId,string"`
		}
		if err := json.Unmarshal(receipt, &result); err != nil {
			return err
		}
		newID = result.TargetID
		for _, move := range []stepRequest{
			{Step: "not_reachable", Note: "The track was blocked."},
			{Step: "reopened", Reason: "A crew confirmed access from a second track."},
		} {
			var receipt json.RawMessage
			if err := recordIncidentStep(ctx, tx, subject, slug, newID, move, &receipt); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	if mainID == 0 {
		fmt.Println("The synthetic incident response is already loaded.")
	} else {
		fmt.Printf("Radio and satellite reports share incident #%d. Incident #%d was split and reopened.\n", mainID, newID)
	}
	return nil
}
