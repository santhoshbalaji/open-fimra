package stateextract

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"os"
	"reflect"
	"testing"
	"time"
)

func TestPublishedExamples(t *testing.T) {
	read := func(name string, out any) []byte {
		t.Helper()
		raw, err := os.ReadFile("examples/" + name + ".json")
		if err != nil {
			t.Fatal(err)
		}
		if out != nil && json.Unmarshal(raw, out) != nil {
			t.Fatalf("invalid example %s", name)
		}
		return raw
	}
	var key struct {
		PublicKey ed25519.PublicKey
		KeyID     string
	}
	read("trusted-public-key", &key)
	if key.KeyID != KeyID(key.PublicKey) {
		t.Fatal("public key ID does not match")
	}
	var receipts []Receipt
	read("receipts", &receipts)
	var original, correction Document
	for i, name := range []string{"incident", "incident-correction", "preparedness-correction"} {
		t.Run(name, func(t *testing.T) {
			var envelope Envelope
			raw := read(name, &envelope)
			receipt, err := Validate(raw, key.PublicKey, receipts[i].ValidatedAt)
			if err != nil || !reflect.DeepEqual(receipt, receipts[i]) {
				t.Fatalf("receipt = %+v, %v", receipt, err)
			}
			var document Document
			if err := json.Unmarshal(envelope.Payload, &document); err != nil {
				t.Fatal(err)
			}
			switch i {
			case 0:
				original = document
			case 1:
				correction = document
			case 2:
				if document.ID != correction.ID || document.Totals != nil || document.Companion.ExpenditureINR.Status != Zero {
					t.Fatalf("unexpected companion: %+v", document)
				}
			}
		})
	}
	if correction.Supersedes == nil || *correction.Supersedes != original.ID || correction.Window != original.Window ||
		correction.Revision != original.Revision+1 || correction.Coverage.Status == original.Coverage.Status {
		t.Fatal("examples do not demonstrate a correction in the same window")
	}
}

func TestValidationRejectsInconsistentOrChangedDocuments(t *testing.T) {
	raw, err := os.ReadFile("examples/incident.json")
	if err != nil {
		t.Fatal(err)
	}
	var envelope Envelope
	var doc Document
	if err := json.Unmarshal(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(envelope.Payload, &doc); err != nil {
		t.Fatal(err)
	}
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	verify := func(candidate []byte) error {
		_, err := Validate(candidate, public, time.Now())
		return err
	}
	valid, err := Sign(doc, private)
	if err != nil || verify(valid) != nil {
		t.Fatal("a signed valid document failed validation")
	}
	tampered := bytes.Replace(valid, []byte(`"expected":2`), []byte(`"expected":3`), 1)
	if verify(tampered) == nil {
		t.Fatal("tampering was accepted")
	}
	for _, change := range []func(*Document){
		func(d *Document) { d.Coverage.Reported++ },
		func(d *Document) { d.Coverage.Units[1].Status = Zero },
		func(d *Document) { d.Coverage.Units[1].Code = d.Coverage.Units[0].Code },
		func(d *Document) { d.Totals.Alerts["routed"] = 1 },
		func(d *Document) { d.Totals.Response["new"] = Response{Denominator: 2, Unknown: 1} },
		func(d *Document) { d.Totals.Operations["private_notes"] = 1 },
		func(d *Document) { d.Window.EndsAt = d.Window.EndsAt.Add(-time.Hour) },
	} {
		var fresh Document
		if err := json.Unmarshal(envelope.Payload, &fresh); err != nil {
			t.Fatal(err)
		}
		change(&fresh)
		signed, err := Sign(fresh, private)
		if err != nil || verify(signed) == nil {
			t.Fatalf("inconsistent document accepted: %+v, %v", fresh, err)
		}
	}
}
