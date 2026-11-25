// Package formschema checks Open FIMRA form definitions.
//
// The format is described in SPEC.md and form.schema.json in this folder.
// The TypeScript module in ts/ applies the same checks with the same
// messages; both are tested against the shared files in examples/.
package formschema

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"errors"
	"io"
)

// Schema is the JSON Schema for a form definition, exactly as published.
//
//go:embed form.schema.json
var Schema []byte

// Validate checks a form definition given as JSON text. It returns every
// problem found, in a fixed order, as plain-English messages. An empty
// result means the form is valid.
func Validate(data []byte) []string {
	var form any
	dec := json.NewDecoder(bytes.NewReader(data))
	if err := dec.Decode(&form); err != nil {
		return []string{msgNotJSON}
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return []string{msgNotJSON}
	}
	return ValidateValue(form)
}

// ValidateValue checks a form definition that has already been decoded
// with encoding/json into maps, slices, strings, float64 and bool values.
// The rules that need a well-formed form run only after the schema passes.
func ValidateValue(form any) []string {
	if problems := checkSchema(form); len(problems) > 0 {
		return problems
	}
	return checkRules(form.(map[string]any))
}

// Columns lists the values that a valid form produces, in form order. It
// returns nil and the problems if the form is not valid.
func Columns(data []byte) ([]Column, []string) {
	if problems := Validate(data); len(problems) > 0 {
		return nil, problems
	}
	var form map[string]any
	_ = json.Unmarshal(data, &form)
	r := &rulesChecker{form: form, byKey: map[string]*item{}, lists: map[string][]any{}, stages: map[string]int{}}
	r.collectItems()
	return columnsOf(r.items), nil
}

const msgNotJSON = "The form is not valid JSON."
