package main

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	formschema "github.com/santhoshbalaji/open-fimra/packages/form-schema"
)

// extensionCases are shared with the web designer's lock checks.
type extensionCases struct {
	Parent    json.RawMessage `json:"parent"`
	Extension []struct {
		Name     string          `json:"name"`
		Locked   []string        `json:"locked"`
		Child    json.RawMessage `json:"child"`
		Expected []string        `json:"expected"`
	} `json:"extension"`
	Rules []struct {
		Name     string          `json:"name"`
		Rules    json.RawMessage `json:"rules"`
		Expected []string        `json:"expected"`
	} `json:"rules"`
}

func readExtensionCases(t *testing.T) extensionCases {
	t.Helper()
	data, err := os.ReadFile("testdata/form-extension-cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases extensionCases
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	return cases
}

func TestCheckExtension(t *testing.T) {
	cases := readExtensionCases(t)
	if problems := formschema.Validate(cases.Parent); len(problems) > 0 {
		t.Fatalf("the shared parent form is not valid: %v", problems)
	}
	for _, c := range cases.Extension {
		t.Run(c.Name, func(t *testing.T) {
			if problems := formschema.Validate(c.Child); len(problems) > 0 {
				t.Fatalf("the child form is not valid: %v", problems)
			}
			got, err := checkExtension(cases.Parent, templateRules{AllowExtension: true, Locked: c.Locked}, c.Child)
			if err != nil {
				t.Fatal(err)
			}
			if !slices.Equal(got, c.Expected) && !(len(got) == 0 && len(c.Expected) == 0) {
				t.Fatalf("got %q, want %q", got, c.Expected)
			}
		})
	}
}

func TestParseTemplateRules(t *testing.T) {
	cases := readExtensionCases(t)
	tree, err := parseFormTree(cases.Parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range cases.Rules {
		t.Run(c.Name, func(t *testing.T) {
			_, got := parseTemplateRules(c.Rules, tree)
			if !slices.Equal(got, c.Expected) && !(len(got) == 0 && len(c.Expected) == 0) {
				t.Fatalf("got %q, want %q", got, c.Expected)
			}
		})
	}
	if _, got := parseTemplateRules(json.RawMessage(`{"allowExtension": true, "locked": [], "lockAll": true}`), tree); len(got) != 1 {
		t.Fatalf("unknown rule names should be refused: %q", got)
	}
}

func TestMergeLocks(t *testing.T) {
	got := mergeLocks(templateRules{AllowExtension: true, Locked: []string{"extra", "basics"}},
		templateRules{AllowExtension: false, Locked: []string{"basics", "crew_size"}})
	if !got.AllowExtension || !slices.Equal(got.Locked, []string{"basics", "crew_size", "extra"}) {
		t.Fatalf("merged %+v", got)
	}
	if parentLevel("state") != "national" || parentLevel("division") != "state" || parentLevel("national") != "" {
		t.Fatal("levels")
	}
}
