package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	formschema "github.com/santhoshbalaji/open-fimra/packages/form-schema"
)

const formExamples = "../../packages/form-schema/examples"

func exampleTree(t *testing.T, name string) (*formTree, []byte) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(formExamples, name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if problems := formschema.Validate(data); len(problems) > 0 {
		t.Fatalf("example %s is not valid: %v", name, problems)
	}
	tree, err := parseFormTree(data)
	if err != nil {
		t.Fatal(err)
	}
	return tree, data
}

var testNow = time.Date(2026, 3, 16, 12, 0, 0, 0, time.UTC)

func testCheck(tree *formTree, stage int) *answerCheck {
	confidence := "nominal"
	power := 12.5
	return &answerCheck{tree: tree, stage: stage, now: testNow,
		alert: alertFacts{ID: 42, ObservedAt: time.Date(2026, 3, 15, 6, 0, 0, 0, time.UTC), RaisedAt: time.Date(2026, 3, 15, 6, 20, 0, 0, time.UTC),
			Longitude: 86.3, Latitude: 16.4, Unit: "Beat 05", ForestClass: "inside", Source: "demo", Confidence: &confidence, FirePower: &power},
		attachments: map[string]storedAttachment{
			"00000000-0000-4000-8000-000000000001": {ContentType: "image/jpeg", FileName: "site.jpg", SizeBytes: 1000},
			"00000000-0000-4000-8000-000000000002": {ContentType: "image/png", FileName: "signature.png", SizeBytes: 500},
			"00000000-0000-4000-8000-000000000003": {ContentType: "application/pdf", FileName: "map.pdf", SizeBytes: 3 << 20},
		},
		measure: func(g json.RawMessage) (float64, error) {
			if strings.Contains(string(g), "Polygon") {
				return 1.23456, nil
			}
			return 0.5, nil
		}}
}

const firstReportAnswers = `{
  "reached_at": "2026-03-15T14:00:00+05:30",
  "fire_status": "controlled",
  "cause_group": "natural",
  "cause": "lightning",
  "cause_notes": "Not shown, so not kept",
  "estimated_area": 1.25,
  "vegetation": ["grass", "leaf-litter"],
  "site_photos": ["00000000-0000-4000-8000-000000000001"],
  "teams": [{"team_name": "Demo Team A", "members": 6, "team_arrived": "09:30"}, {"team_name": "Demo Team B", "members": 4}],
  "equipment": ["beaters"],
  "remarks": "",
  "reporter_signature": "00000000-0000-4000-8000-000000000002",
  "total_staff": 999
}`

func TestFirstReportAnswers(t *testing.T) {
	tree, _ := exampleTree(t, "first-report")
	check := testCheck(tree, -1)
	out, problems := checkAnswers(check, json.RawMessage(firstReportAnswers))
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	data, _ := json.Marshal(out)
	var got map[string]any
	_ = json.Unmarshal(data, &got)
	if _, kept := got["cause_notes"]; kept {
		t.Error("a hidden answer was kept")
	}
	if _, kept := got["remarks"]; kept {
		t.Error("an empty text was kept")
	}
	want := map[string]any{"total_staff": 10.0, "team_count": 2.0, "hours_to_reach": 2.5,
		"observed_at": "2026-03-15T06:00:00Z", "alert_location": map[string]any{"type": "Point", "coordinates": []any{86.3, 16.4}}}
	for key, value := range want {
		if !reflect.DeepEqual(got[key], value) {
			t.Errorf("%s = %v, want %v", key, got[key], value)
		}
	}
	if !reflect.DeepEqual(check.used["site_photos"], []string{"00000000-0000-4000-8000-000000000001"}) || len(check.used["reporter_signature"]) != 1 {
		t.Errorf("attachments used: %v", check.used)
	}
}

func TestSubmissionProblems(t *testing.T) {
	tree, _ := exampleTree(t, "first-report")
	for _, tc := range []struct {
		name, change string
		want         []string
	}{
		{"missing required", `{"reached_at": null}`, []string{`Field "reached_at" is required.`}},
		{"unknown field", `{"fire_tpye": "x"}`, []string{`values has "fire_tpye", which is not a field of this form.`}},
		{"field outside its repeat", `{"members": 3}`, []string{`Field "members" belongs inside the repeating group "teams".`}},
		{"too many decimals", `{"estimated_area": 1.257}`, []string{`Field "estimated_area" can have at most 2 decimal places.`}},
		{"below the minimum", `{"estimated_area": -1}`, []string{`Field "estimated_area" must be at least 0.`}},
		{"number as text", `{"estimated_area": "3"}`, []string{`Field "estimated_area" must be a number.`}},
		{"unknown choice", `{"fire_status": "smouldering"}`, []string{`Field "fire_status" has no choice "smouldering".`}},
		{"cascade mismatch", `{"cause": "campfire"}`, []string{`Field "cause": the choice "campfire" does not go with the answer to "cause_group".`}},
		{"shown and required", `{"cause": "natural-other", "cause_notes": ""}`, []string{`Field "cause_notes" is required.`}},
		{"future time", `{"reached_at": "2026-03-17T09:30:00+05:30"}`, []string{`Field "reached_at" cannot be in the future.`}},
		{"no offset", `{"reached_at": "2026-03-15T09:30:00"}`, []string{`Field "reached_at" must be a date and time with its offset from universal time, such as 2026-03-15T14:30:00+05:30.`}},
		{"too few choices", `{"vegetation": []}`, nil},
		{"duplicate choice", `{"vegetation": ["grass", "grass"]}`, []string{`Field "vegetation" lists "grass" more than once.`}},
		{"no entries", `{"teams": []}`, []string{`Repeating group "teams" needs at least 1 entries.`}},
		// Answers that do not fit their fields are reported before missing ones.
		{"entry problem", `{"teams": [{"members": 0}]}`, []string{`In entry 1 of "teams": Field "members" must be at least 1.`}},
		{"entry missing", `{"teams": [{"members": 2}]}`, []string{`In entry 1 of "teams": Field "team_name" is required.`}},
		{"bad time", `{"teams": [{"team_name": "A", "members": 2, "team_arrived": "25:00"}]}`, []string{`In entry 1 of "teams": Field "team_arrived" must be a time such as 14:30.`}},
		{"unknown attachment", `{"site_photos": ["00000000-0000-4000-8000-0000000000ff"]}`, []string{`Field "site_photos" uses the attachment "00000000-0000-4000-8000-0000000000ff", which was not uploaded for this alert.`}},
		{"pdf as a photo", `{"site_photos": ["00000000-0000-4000-8000-000000000003"]}`, []string{`Field "site_photos" needs an image, but the attachment "00000000-0000-4000-8000-000000000003" is application/pdf.`}},
		{"not an ID", `{"reporter_signature": "signature.png"}`, []string{`Field "reporter_signature" must give attachment IDs, as returned by the upload.`}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var answers, change map[string]any
			_ = json.Unmarshal([]byte(firstReportAnswers), &answers)
			if err := json.Unmarshal([]byte(tc.change), &change); err != nil {
				t.Fatal(err)
			}
			for k, v := range change {
				answers[k] = v
			}
			data, _ := json.Marshal(answers)
			_, problems := checkAnswers(testCheck(tree, -1), data)
			if tc.want == nil {
				// An empty multiple choice is no answer, and vegetation is optional.
				if len(problems) > 0 {
					t.Fatal(problems)
				}
				return
			}
			slices.Sort(problems)
			slices.Sort(tc.want)
			if !slices.Equal(problems, tc.want) {
				t.Fatalf("got %q\nwant %q", problems, tc.want)
			}
		})
	}
	if _, problems := checkAnswers(testCheck(tree, -1), json.RawMessage(`[1]`)); len(problems) != 1 {
		t.Fatal("a list was accepted as values")
	}
}

func TestStagedFormAnswers(t *testing.T) {
	tree, _ := exampleTree(t, "control-room-dispatch")
	stage, err := tree.stageAt("team_sent")
	if err != nil || stage != 0 {
		t.Fatal(stage, err)
	}
	if _, err := tree.stageAt("first_report"); err == nil {
		t.Fatal("a staged form accepted a step without a stage")
	}
	dispatch := `{"team_name": "Demo Team A", "team_leader": "Demo Leader", "team_size": 5, "vehicle": "jeep",
		"dispatched_at": "2026-03-15T07:00:00Z", "reached_at": "2026-03-15T09:00:00Z"}`
	out, problems := checkAnswers(testCheck(tree, 0), json.RawMessage(dispatch))
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	if _, kept := out["reached_at"]; kept {
		t.Error("an answer from a later stage was kept")
	}
	if out["responsible_unit"] != "Beat 05" || out["fire_power"] != 12.5 || out["alert_id"] != "42" {
		t.Errorf("alert values: %v", out)
	}
	_, problems = checkAnswers(testCheck(tree, 1), json.RawMessage(dispatch))
	want := []string{`Field "fire_found" is required.`, `Field "site_location" is required.`}
	slices.Sort(problems)
	if !slices.Equal(problems, want) {
		t.Fatalf("arrival stage: %q", problems)
	}
	arrival := strings.Replace(dispatch, "}", `, "site_location": {"type": "Point", "coordinates": [86.3, 16.4]}, "fire_found": "yes"}`, 1)
	out, problems = checkAnswers(testCheck(tree, 1), json.RawMessage(arrival))
	if len(problems) > 0 || out["travel_hours"] != 2.0 {
		t.Fatalf("arrival: %v %v", out, problems)
	}
}

func TestFieldTypeTourAnswers(t *testing.T) {
	tree, _ := exampleTree(t, "field-type-tour")
	answers := `{
	  "short_code": "DEMO-AB12", "count_seen": 3, "visit_date": "2026-03-15", "visit_time": "14:30",
	  "visit_moment": "2026-03-15T14:30+05:30", "weather": "windy", "hazards": ["power-line"],
	  "spot": {"type": "Point", "coordinates": [86.3, 16.4, 120]},
	  "path": {"type": "LineString", "coordinates": [[86.3, 16.4], [86.31, 16.41]]},
	  "patch": {"type": "Polygon", "coordinates": [[[86.3, 16.4], [86.31, 16.4], [86.31, 16.41], [86.3, 16.4]]]},
	  "attachment": ["00000000-0000-4000-8000-000000000003"],
	  "crews": [
	    {"crew_size": 4, "shifts": [{"shift_hours": 2.5}, {"shift_hours": 1}]},
	    {"crew_size": 0, "shifts": [{"shift_hours": 3}]}
	  ]
	}`
	check := testCheck(tree, -1)
	out, problems := checkAnswers(check, json.RawMessage(answers))
	if len(problems) > 0 {
		t.Fatal(problems)
	}
	crews := out["crews"].([]any)
	first := crews[0].(map[string]any)
	if first["crew_hours"] != 14.0 || len(first["shifts"].([]any)) != 2 {
		t.Errorf("first crew: %v", first)
	}
	if _, kept := crews[1].(map[string]any)["shifts"]; kept {
		t.Error("shifts of a crew of zero were kept although hidden")
	}
	if out["average_crew"] != 2.0 || out["spare_people"] != -6.0 || out["patch_area"] != 1.23 || out["path_length"] != 0.5 {
		t.Errorf("calculations: %v", out)
	}
	for _, bad := range []struct{ change, want string }{
		{`"short_code": "DEMO-ab12"`, `Field "short_code" does not match the expected pattern: DEMO-, two capital letters and two to four digits.`},
		{`"patch": {"type": "Polygon", "coordinates": [[[86.3, 16.4], [86.31, 16.4], [86.3, 16.4]]]}`, `Field "patch" must be a GeoJSON Polygon with closed rings of at least four positions.`},
		{`"spot": {"type": "Point", "coordinates": [186.3, 16.4]}`, `Field "spot" must be a GeoJSON Point in longitude and latitude.`},
		{`"hazards": ["power-line", "dry-grass", "steep-slope"]`, `Field "hazards" can have at most 2 choices.`},
		{`"visit_date": "2026-02-30"`, `Field "visit_date" must be a date such as 2026-03-15.`},
	} {
		key := bad.change[1 : strings.Index(bad.change[1:], `"`)+1]
		start := strings.Index(answers, `"`+key+`"`)
		end := start + strings.Index(answers[start:], "\n")
		line := strings.TrimRight(answers[start:end], ",")
		changed := strings.Replace(answers, line, bad.change, 1)
		_, problems := checkAnswers(testCheck(tree, -1), json.RawMessage(changed))
		if !slices.Equal(problems, []string{bad.want}) {
			t.Errorf("%s: got %q", key, problems)
		}
	}
	// A file field accepts only the listed endings.
	check = testCheck(tree, -1)
	check.attachments["00000000-0000-4000-8000-000000000003"] = storedAttachment{ContentType: "application/zip", FileName: "map.zip", SizeBytes: 10}
	if _, problems := checkAnswers(check, json.RawMessage(answers)); !slices.Equal(problems, []string{`Field "attachment" does not accept the file "map.zip".`}) {
		t.Errorf("file ending: %q", problems)
	}
}

func TestFormViewsAreSafe(t *testing.T) {
	tree, data := exampleTree(t, "field-type-tour")
	columns, _ := formschema.Columns(data)
	views, err := formViews(7, tree, columns)
	if err != nil {
		t.Fatal(err)
	}
	names := []string{}
	for _, v := range views {
		names = append(names, v.Name)
	}
	if !slices.Equal(names, []string{"f7", "f7_crews", "f7_shifts"}) {
		t.Fatalf("view names %v", names)
	}
	if !strings.Contains(views[2].SQL, `concat_ws('.', e1.n, e2.n) AS _entry, concat_ws('.', e1.n) AS _parent_entry`) ||
		!strings.Contains(views[0].SQL, `ST_Area(ST_Force2D(ST_SetSRID(ST_GeomFromGeoJSON(s.answers->'patch'), 4326))::geography) / 10000 AS "patch_area_ha"`) {
		t.Fatalf("view SQL:\n%s\n%s", views[0].SQL, views[2].SQL)
	}
	evil := append(slices.Clone(columns), formschema.Column{Name: `x"; DROP TABLE alerts; --`, Field: "x", ValueType: "text"})
	if _, err := formViews(7, tree, evil); err == nil {
		t.Fatal("an unsafe column name was accepted")
	}
	if sqlLiteral("it's") != "'it''s'" {
		t.Fatal("literal quoting")
	}
}
