// Package starterpack holds the starter forms a new workspace can switch on
// in the first-run wizard (PLAN.md sections 5.4 and 7), and the workflow
// steps each one is filled in at.
//
// The forms use the format in packages/form-schema. pack.json lists them in
// order with their steps. A form with stages is assigned at the step of each
// stage. A form with no steps is published but not put on a step.
package starterpack

import (
	"embed"
	"encoding/json"
	"fmt"
)

//go:embed pack.json forms/*.json
var files embed.FS

// ManualFireReport is the key of the form that records a fire reported by
// another source. Saving it makes an alert instead of filling in a step.
const ManualFireReport = "manual-fire-report"

// Form is one starter form.
type Form struct {
	// Key is the form's id, such as first-report.
	Key     string
	Version int
	// Title is the English title.
	Title string
	// Steps are the default workflow steps the form is assigned to.
	Steps []string
	// Source is the exact text of the form file.
	Source []byte
}

var forms = mustLoad()

// Forms returns the starter forms in the order of pack.json.
func Forms() []Form {
	out := make([]Form, len(forms))
	for i, f := range forms {
		// Never nil, so a form with no steps lists them as [] in JSON.
		f.Steps = append([]string{}, f.Steps...)
		f.Source = append([]byte(nil), f.Source...)
		out[i] = f
	}
	return out
}

func mustLoad() []Form {
	out, err := load()
	if err != nil {
		panic("starter pack: " + err.Error())
	}
	return out
}

func load() ([]Form, error) {
	data, err := files.ReadFile("pack.json")
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Forms []struct {
			Form  string   `json:"form"`
			Steps []string `json:"steps"`
		} `json:"forms"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("read pack.json: %w", err)
	}
	out := make([]Form, 0, len(manifest.Forms))
	for _, entry := range manifest.Forms {
		source, err := files.ReadFile("forms/" + entry.Form + ".json")
		if err != nil {
			return nil, err
		}
		var head struct {
			ID      string            `json:"id"`
			Version int               `json:"version"`
			Title   map[string]string `json:"title"`
		}
		if err := json.Unmarshal(source, &head); err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Form, err)
		}
		if head.ID != entry.Form {
			return nil, fmt.Errorf("forms/%s.json has the id %q", entry.Form, head.ID)
		}
		out = append(out, Form{Key: head.ID, Version: head.Version, Title: head.Title["en"],
			Steps: append([]string{}, entry.Steps...), Source: source})
	}
	return out, nil
}
