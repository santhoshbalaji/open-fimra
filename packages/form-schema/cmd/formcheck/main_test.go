package main

import (
	"bytes"
	"encoding/json"
	"os"
	"reflect"
	"testing"

	formschema "github.com/santhoshbalaji/open-fimra/packages/form-schema"
)

func TestSingleForm(t *testing.T) {
	data, err := os.ReadFile("../../examples/first-report.json")
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := run(data, &out); err != nil {
		t.Fatal(err)
	}
	var got Result
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	want, _ := formschema.Columns(data)
	if len(got.Problems) != 0 || !reflect.DeepEqual(got.Columns, want) {
		t.Fatalf("got %+v, want no problems and the package's columns", got)
	}
}

func TestBatchKeepsOrderAndMessages(t *testing.T) {
	valid, err := os.ReadFile("../../examples/patrol-log.json")
	if err != nil {
		t.Fatal(err)
	}
	broken := []byte(`{"formatVersion": 1, "id": "Bad", "version": 1, "title": {"en": "Bad"}, "sections": []}`)
	input := append(append(append([]byte("[\n"), valid...), ','), broken...)
	input = append(input, ']')
	var out bytes.Buffer
	if err := run(input, &out); err != nil {
		t.Fatal(err)
	}
	var got []Result
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || len(got[0].Problems) != 0 || len(got[0].Columns) == 0 {
		t.Fatalf("first result should be valid with columns: %+v", got)
	}
	if want := formschema.Validate(broken); !reflect.DeepEqual(got[1].Problems, want) || got[1].Columns != nil {
		t.Fatalf("second result: got %+v, want problems %q and no columns", got[1], want)
	}
}

func TestNotJSON(t *testing.T) {
	var out bytes.Buffer
	if err := run([]byte("not json"), &out); err != nil {
		t.Fatal(err)
	}
	var got Result
	_ = json.Unmarshal(out.Bytes(), &got)
	if !reflect.DeepEqual(got.Problems, formschema.Validate([]byte("not json"))) {
		t.Fatalf("got %+v", got)
	}
	if err := run([]byte("[1, "), &out); err == nil {
		t.Fatal("a broken array should be an input error")
	}
}
