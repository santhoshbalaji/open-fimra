package main

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	extract "github.com/santhoshbalaji/open-fimra/packages/state-extract"
)

func syntheticStateCode(prefix string) string {
	return "XS-" + strings.ToUpper(strings.TrimSuffix(strings.TrimPrefix(prefix, "daily-"), "-"))
}

func seedStateExtractInputs(t *testing.T, ctx context.Context, tx pgx.Tx, workspace, hierarchy, prefix string, alert int64, observed time.Time) {
	t.Helper()
	// Two provider records for one observation, both falling in two divisions.
	if _, err := tx.Exec(ctx, `WITH original AS (
    UPDATE detection_matches m SET unit_paths=ARRAY['dailyroot.c1.d1','dailyroot.c1.d2']::ltree[]
    FROM alerts a WHERE a.id=$1 AND a.workspace_id=$2
      AND (m.workspace_id,m.detection_id,m.observed_at)=(a.workspace_id,a.detection_id,a.observed_at)
    RETURNING m.detection_id,m.observed_at,m.unit_paths
), other AS (
    INSERT INTO detections(source,product,observed_at,geom,source_key)
    SELECT 'daily_second','synthetic',observed_at,ST_SetSRID(ST_MakePoint(10.13579,10.2468),4326),$3
    FROM original RETURNING id,observed_at
)
INSERT INTO detection_links(detection_id,observed_at,primary_id,primary_observed_at,reason,distance_m)
SELECT o.id,o.observed_at,a.detection_id,a.observed_at,'same_observation',1 FROM other o CROSS JOIN original a`, alert, workspace, prefix); err != nil {
		t.Fatal(err)
	}
	// A separate alert is still awaiting verification. A fourth provider
	// record has a match and no alert. The three datasets have distinct counts.
	if _, err := tx.Exec(ctx, `WITH more AS (
    INSERT INTO detections(source,product,observed_at,geom,source_key)
    SELECT 'daily_second','synthetic',$1::timestamptz+n*interval '1 minute',ST_SetSRID(ST_MakePoint(10,10),4326),$2
    FROM generate_series(1,2) n RETURNING id,observed_at
), matches AS (
    INSERT INTO detection_matches(workspace_id,detection_id,observed_at,forest_class,unit_paths)
    SELECT $3,id,observed_at,'inside',ARRAY['dailyroot.c1.d1','dailyroot.c1.d2']::ltree[] FROM more
    RETURNING detection_id,observed_at
)
INSERT INTO alerts(workspace_id,detection_id,observed_at,queue,hierarchy_id,unit_path,hierarchy,
    hierarchy_unit_paths,unit_paths,forest_class,layer_version_ids)
SELECT $3,detection_id,observed_at,'unit',$4,'dailyroot.c1.d1','{}',
    jsonb_build_object($4::uuid::text,jsonb_build_array('dailyroot.c1.d1')),
    ARRAY['dailyroot.c1.d1']::ltree[],'inside','{}' FROM matches ORDER BY observed_at LIMIT 1`, observed, prefix, workspace, hierarchy); err != nil {
		t.Fatal(err)
	}
	var incident, template int64
	if err := tx.QueryRow(ctx, `SELECT im.incident_id,i.template_id FROM incident_members im
JOIN workflow_instances i ON i.alert_id=im.alert_id WHERE im.alert_id=$1`, alert).Scan(&incident, &template); err != nil {
		t.Fatal(err)
	}
	var officer, reviewer string
	if err := tx.QueryRow(ctx, `SELECT a.id::text,b.id::text FROM users a,users b WHERE a.oidc_subject=$1 AND b.oidc_subject=$2`,
		prefix+"a2", prefix+"a1").Scan(&officer, &reviewer); err != nil {
		t.Fatal(err)
	}
	for _, input := range []struct {
		key, step, answers string
		approved           bool
	}{
		{"response-flash", "reached_site", `{"fire_seen":"yes","land_class":"forest","remarks":"Field Person Example 9876543210"}`, false},
		{"damage-assessment", "final_report", `{"area_ha":19,"state_code":"XS-CODE","state_area_ha":2.4,"state_land_class":"forest",
"state_area_method":"field","state_area_status":"provisional","state_impact_categories":["vegetation"],"vegetation":"Field Person Example 9876543210",
"people":"Private Household Ref","location":"10.13579 10.2468"}`, true},
	} {
		var version int64
		err := tx.QueryRow(ctx, `WITH f AS (INSERT INTO forms(workspace_id,form_key) VALUES($1,$2) RETURNING id)
INSERT INTO form_versions(form_id,version,source,definition,columns,published_at,published_by)
SELECT id,1,'{}','{"version":1}','[]',$3,$4 FROM f RETURNING id`, workspace, input.key, observed, reviewer).Scan(&version)
		if err != nil {
			t.Fatal(err)
		}
		var submitted string
		err = tx.QueryRow(ctx, `INSERT INTO submissions(id,workspace_id,form_version_id,alert_id,incident_id,template_id,
step,user_id,received_at,location,answers,request_hash)
VALUES(gen_random_uuid(),$1,$2,$3,$4,$5,$6,$7,$8,ST_SetSRID(ST_MakePoint(10.13579,10.2468),4326),$9,repeat('a',64))
RETURNING id::text`, workspace, version, alert, incident, template, input.step, officer, observed,
			strings.ReplaceAll(input.answers, "XS-CODE", syntheticStateCode(prefix))).Scan(&submitted)
		if err != nil {
			t.Fatal(err)
		}
		if input.approved {
			if _, err := tx.Exec(ctx, `INSERT INTO approvals(submission_id,decision,user_id) VALUES($1,'approved',$2)`,
				submitted, reviewer); err != nil {
				t.Fatal(err)
			}
		}
	}
	if _, err := tx.Exec(ctx, `WITH f AS (
    INSERT INTO forms(workspace_id,form_key) VALUES($1,'preparedness-work') RETURNING id
), v AS (
    INSERT INTO form_versions(form_id,version,source,definition,columns,published_at,published_by)
    SELECT id,1,'{}','{"version":1}','[]',$2,$3 FROM f RETURNING id
), r AS (
    INSERT INTO operations_records(id,workspace_id,hierarchy_id,unit_path,kind,owner_id,created_at)
    VALUES(gen_random_uuid(),$1,$4,'dailyroot.c1.d1','preparedness',$5,$2) RETURNING id
), s AS (
    INSERT INTO submissions(id,workspace_id,form_version_id,operations_record_id,user_id,received_at,
        completes_record,answers,request_hash)
    SELECT gen_random_uuid(),$1,v.id,r.id,$5,$2,true,
        '{"target_quantity":5,"work_done_quantity":3,"expenditure_inr":0,"work":"Field Person Example 9876543210"}',
        repeat('a',64) FROM v,r RETURNING id
)
INSERT INTO approvals(submission_id,decision,user_id) SELECT id,'approved',$3 FROM s`,
		workspace, observed, reviewer, hierarchy, officer); err != nil {
		t.Fatal(err)
	}
}

type stateTestCall func(actor, method, path string, input any, want int, out any) []byte

func testStateExtractRequests(t *testing.T, call stateTestCall, prefix, second, nilID string) {
	t.Helper()
	const endpoint = "/state-extracts"
	code := syntheticStateCode(prefix)
	call("a2", "GET", endpoint, nil, 403, nil)
	call("a1", "POST", endpoint+"/settings", map[string]string{
		"action": "configure", "stateCode": code, "unitCodeSystem": "fictional-units",
	}, 201, nil)
	request := stateExtractRequest{Action: "preview", From: "2026-03-08", To: "2026-03-08"}
	var preview stateExtractPreview
	call("a1", "POST", endpoint, request, 200, &preview)
	incident := preview.Documents.Incident
	if c := incident.Coverage; c.Status != extract.Incomplete || c.Expected != 4 || c.Reported != 2 ||
		c.Units[1].Status != extract.Zero || c.Units[2].Status != extract.NotReported {
		t.Fatalf("incorrect coverage: %+v", c)
	}
	if totals := incident.Totals; totals.Detections["records"] != 4 || totals.Detections["deduplicated"] != 3 ||
		totals.Alerts["routed"] != 2 || totals.Alerts["awaiting_verification"] != 1 || totals.Incidents["new"] != 1 || totals.Area.Hectares == nil ||
		*totals.Area.Hectares != 2.4 || totals.Area.Impacts["vegetation"] != 1 {
		t.Fatalf("incorrect independent counts or area: %+v", totals)
	}
	if p := preview.Documents.Preparedness.Companion; p.WorkRecords != 1 || *p.Target.Value != 5 ||
		*p.WorkDone.Value != 3 || p.ExpenditureINR.Status != extract.Zero {
		t.Fatalf("incorrect companion: %+v", p)
	}
	var original, correction stateExtractRevision
	request.Action, request.Digest = "approve", preview.Digest
	call("a1", "POST", endpoint, request, 201, &original)
	call("a1", "POST", endpoint, request, 409, nil)
	call("a1", "POST", endpoint+"/settings", map[string]string{"action": "rotate"}, 201, nil)
	before := call("a1", "GET", endpoint+"/"+original.ID+"/incident", nil, 200, nil)
	companion := call("a1", "GET", endpoint+"/"+original.ID+"/preparedness", nil, 200, nil)
	for _, raw := range [][]byte{before, companion} {
		for _, private := range []string{"Field Person Example", "9876543210", "10.13579", "10.2468",
			"Private Household Ref", "Assistance tally corrected.", prefix + "a2", `"location"`, `"answers"`, `"referral"`} {
			if bytes.Contains(raw, []byte(private)) {
				t.Fatalf("private data reached the extract: %q", private)
			}
		}
		var verified extract.Receipt
		call("a1", "POST", endpoint+"/validate", json.RawMessage(raw), 200, &verified)
		if verified.ExtractID != original.ID || verified.KeyID != original.Receipts[0].KeyID {
			t.Fatalf("wrong verification receipt: %+v", verified)
		}
	}
	tampered := bytes.Replace(before, []byte(`"records":4`), []byte(`"records":5`), 1)
	call("a1", "POST", endpoint+"/validate", json.RawMessage(tampered), 422, nil)
	call("a2", "GET", endpoint+"/"+original.ID+"/incident", nil, 403, nil)

	var submitted struct{ ID string }
	call("a2", "POST", second, dailyRequest{Action: "submit", Base: nilID}, 201, &submitted)
	call("a1", "POST", second, dailyRequest{Action: "approved", Base: submitted.ID}, 201, nil)
	request.Action = "preview"
	call("a1", "POST", endpoint, request, 200, &preview)
	incident = preview.Documents.Incident
	if incident.Coverage.Units[0].DetectionRecords == nil || *incident.Coverage.Units[0].DetectionRecords != 4 ||
		incident.Coverage.Units[1].DetectionRecords == nil || *incident.Coverage.Units[1].DetectionRecords != 4 ||
		incident.Totals.Detections["records"] != 4 || incident.Totals.Detections["deduplicated"] != 3 || incident.Totals.Alerts["routed"] != 2 {
		t.Fatalf("overlap affected state totals: %+v %+v", incident.Coverage, incident.Totals)
	}
	request.Action, request.Digest = "approve", preview.Digest
	call("a1", "POST", endpoint, request, 201, &correction)
	if correction.Revision != 2 || correction.Supersedes == nil || *correction.Supersedes != original.ID ||
		correction.Receipts[0].KeyID == original.Receipts[0].KeyID || len(correction.Receipts) != 2 {
		t.Fatalf("incorrect correction chain: %+v", correction)
	}
	var history struct{ Items []stateExtractRevision }
	call("a1", "GET", endpoint, nil, 200, &history)
	if len(history.Items) != 2 || !history.Items[0].Current || history.Items[1].Current {
		t.Fatalf("incorrect current history: %+v", history)
	}
	after := call("a1", "GET", endpoint+"/"+original.ID+"/incident", nil, 200, nil)
	if !bytes.Equal(before, after) {
		t.Fatal("a correction changed the signed original")
	}
	call("a1", "POST", endpoint, stateExtractRequest{Action: "preview", From: "2026-03-08", To: "2026-03-09"}, 409, nil)
	var missing stateExtractPreview
	call("a1", "POST", endpoint, stateExtractRequest{Action: "preview", From: "2026-03-09", To: "2026-03-09"}, 200, &missing)
	if missing.Documents.Incident.Totals != nil || missing.Documents.Incident.Coverage.Status != extract.NotReported {
		t.Fatalf("missing returns were treated as zero: %+v", missing.Documents.Incident)
	}
}
