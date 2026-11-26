package starterpack

import (
	"encoding/json"
	"slices"
	"testing"

	formschema "github.com/santhoshbalaji/open-fimra/packages/form-schema"
)

// The steps of the default workflow (PLAN.md section 5.4), as the database
// migration creates them. Final steps never need a form.
var defaultSteps = []string{"new", "acknowledged", "team_sent", "reached_site", "controlled", "extinguished",
	"first_report", "final_report", "verified"}

func TestFormsAreValid(t *testing.T) {
	list := Forms()
	want := []string{"control-room-dispatch", "first-report", "final-report", ManualFireReport, "patrol-log",
		"offence-referral", "relief-referral", "preparedness-work", "fire-training", "readiness-check",
		"prescribed-burn-authorisation", "prescribed-burn-completion", "after-action-finding",
		"response-intake", ResponseFlash, ResponseUpdate, "response-action", DamageAssessment}
	var keys []string
	for _, f := range list {
		keys = append(keys, f.Key)
		if problems := formschema.Validate(f.Source); len(problems) > 0 {
			t.Errorf("%s is not valid: %v", f.Key, problems)
		}
		if f.Version != 1 || f.Title == "" {
			t.Errorf("%s has version %d and title %q", f.Key, f.Version, f.Title)
		}
	}
	if !slices.Equal(keys, want) {
		t.Fatalf("the pack has %v, want %v", keys, want)
	}
}

func TestStepsFitTheDefaultWorkflow(t *testing.T) {
	used := map[string]string{}
	extendedSteps := append(slices.Clone(defaultSteps), "assigned", "flash_update", "response_update")
	for _, f := range Forms() {
		var form struct {
			Stages []struct {
				WorkflowStep string `json:"workflowStep"`
			} `json:"stages"`
		}
		if err := json.Unmarshal(f.Source, &form); err != nil {
			t.Fatal(err)
		}
		var stageSteps []string
		for _, s := range form.Stages {
			stageSteps = append(stageSteps, s.WorkflowStep)
		}
		if len(stageSteps) > 0 && !slices.Equal(stageSteps, f.Steps) {
			t.Errorf("%s has stages at %v but is assigned to %v", f.Key, stageSteps, f.Steps)
		}
		if len(stageSteps) == 0 && len(f.Steps) > 1 {
			t.Errorf("%s has no stages, so it can be filled in at one step only", f.Key)
		}
		for _, step := range f.Steps {
			steps := defaultSteps
			if f.Extended {
				steps = extendedSteps
			}
			if !slices.Contains(steps, step) || step == "new" {
				t.Errorf("%s is assigned to %s, which is not a step after New in the default workflow", f.Key, step)
			}
			if f.Extended {
				continue
			}
			if other, taken := used[step]; taken {
				t.Errorf("%s and %s are both assigned to %s", f.Key, other, step)
			}
			used[step] = f.Key
		}
	}
	for _, step := range []string{"team_sent", "reached_site", "first_report", "final_report"} {
		if used[step] == "" {
			t.Errorf("no starter form is filled in at %s", step)
		}
	}
}

// The manual fire report makes an alert from two of its answers.
func TestManualFireReportHasTimeAndPlace(t *testing.T) {
	for _, f := range Forms() {
		if f.Key != ManualFireReport {
			continue
		}
		columns, problems := formschema.Columns(f.Source)
		if len(problems) > 0 {
			t.Fatal(problems)
		}
		types := map[string]string{}
		for _, c := range columns {
			if c.Repeat == "" {
				types[c.Name] = c.ValueType
			}
		}
		if len(f.Steps) != 0 {
			t.Errorf("the manual fire report makes its own alert, so it is on no step: %v", f.Steps)
		}
		for name, want := range map[string]string{"reported_at": "date-time", "fire_location": "point"} {
			if types[name] != want {
				t.Errorf("column %s is %q, want %q (columns %v)", name, types[name], want, types)
			}
		}
		var form struct {
			Sections []struct {
				Items []struct {
					Type string `json:"type"`
				} `json:"items"`
			} `json:"sections"`
		}
		_ = json.Unmarshal(f.Source, &form)
		for _, s := range form.Sections {
			for _, item := range s.Items {
				if item.Type == "photo" || item.Type == "file" || item.Type == "signature" {
					t.Errorf("the manual fire report cannot take %s answers: attachments belong to an alert, and this report makes the alert", item.Type)
				}
			}
		}
		return
	}
	t.Fatal("the pack has no manual fire report")
}

func TestStepsAreNeverNil(t *testing.T) {
	for _, f := range Forms() {
		if data, _ := json.Marshal(f.Steps); string(data) == "null" {
			t.Errorf("%s lists its steps as null", f.Key)
		}
	}
}

func TestFormsReturnsCopies(t *testing.T) {
	a := Forms()
	a[0].Source[0] = 'x'
	a[0].Steps[0] = "x"
	if b := Forms(); b[0].Source[0] == 'x' || b[0].Steps[0] == "x" {
		t.Fatal("Forms must not share its data with callers")
	}
}
