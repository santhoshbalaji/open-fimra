package formschema

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// The examples are shared with the TypeScript tests in ts/. Set
// FORM_SCHEMA_WRITE_COLUMNS=1 to rewrite examples/columns/ from this
// package after changing an example; the TypeScript tests then check
// that they agree.

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestValidExamples(t *testing.T) {
	files, _ := filepath.Glob("examples/*.json")
	if len(files) < 6 {
		t.Fatalf("found %d valid examples, want at least 6", len(files))
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			data := readFile(t, file)
			cols, problems := Columns(data)
			if len(problems) > 0 {
				t.Fatalf("valid example has problems:\n%s", strings.Join(problems, "\n"))
			}
			colFile := filepath.Join("examples", "columns", filepath.Base(file))
			if os.Getenv("FORM_SCHEMA_WRITE_COLUMNS") == "1" {
				out, _ := json.MarshalIndent(cols, "", "  ")
				if err := os.WriteFile(colFile, append(out, '\n'), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			var want []Column
			if err := json.Unmarshal(readFile(t, colFile), &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(cols, want) {
				t.Errorf("columns differ from %s:\n got %+v\nwant %+v", colFile, cols, want)
			}
		})
	}
}

type invalidCase struct {
	Expected string          `json:"expected"`
	Form     json.RawMessage `json:"form"`
}

func TestInvalidExamples(t *testing.T) {
	files, _ := filepath.Glob("examples/invalid/*.json")
	if len(files) < 30 {
		t.Fatalf("found %d invalid examples, want at least 30", len(files))
	}
	for _, file := range files {
		t.Run(filepath.Base(file), func(t *testing.T) {
			var c invalidCase
			if err := json.Unmarshal(readFile(t, file), &c); err != nil {
				t.Fatal(err)
			}
			if c.Expected == "" || len(c.Form) == 0 {
				t.Fatal(`an invalid example needs "expected" and "form"`)
			}
			got := Validate(c.Form)
			if len(got) != 1 || got[0] != c.Expected {
				t.Errorf("got %q\nwant [%q]", got, c.Expected)
			}
		})
	}
}

func TestNotJSON(t *testing.T) {
	for _, text := range []string{"", "{", `{"id": "a"} {}`, "[1,]"} {
		if got := Validate([]byte(text)); len(got) != 1 || got[0] != msgNotJSON {
			t.Errorf("Validate(%q) = %q", text, got)
		}
	}
}

func TestSchemaIsPublishedCopy(t *testing.T) {
	if string(Schema) != string(readFile(t, "form.schema.json")) {
		t.Fatal("the embedded schema differs from form.schema.json")
	}
	if schemaRules == nil {
		t.Fatal("schema did not load")
	}
}
