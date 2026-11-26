// Package stateextract validates and authenticates the version 1 state exchange.
package stateextract

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

const (
	Reported    = "reported"
	Zero        = "zero"
	Incomplete  = "incomplete"
	NotReported = "not_reported"
)

//go:embed extract.schema.json
var schemaBytes []byte

var schema = compileSchema()

func compileSchema() *jsonschema.Schema {
	c := jsonschema.NewCompiler()
	c.AssertFormat()
	doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(schemaBytes))
	if err != nil {
		panic(err)
	}
	if err := c.AddResource("extract.schema.json", doc); err != nil {
		panic(err)
	}
	return c.MustCompile("extract.schema.json")
}

type Envelope struct {
	KeyID     string          `json:"keyId"`
	Signature []byte          `json:"signature"`
	Payload   json.RawMessage `json:"payload"`
}

type Document struct {
	SchemaVersion int           `json:"schemaVersion"`
	Kind          string        `json:"kind"`
	Source        string        `json:"source"`
	ID            string        `json:"id"`
	Revision      int           `json:"revision"`
	Supersedes    *string       `json:"supersedes"`
	Authority     Authority     `json:"authority"`
	Window        Window        `json:"window"`
	GeneratedAt   time.Time     `json:"generatedAt"`
	Coverage      *Coverage     `json:"coverage,omitempty"`
	Totals        *Totals       `json:"totals,omitempty"`
	Companion     *Preparedness `json:"preparedness,omitempty"`
}

type Authority struct {
	StateCode      string `json:"stateCode"`
	UnitCodeSystem string `json:"unitCodeSystem"`
	ApproverRole   string `json:"approverRole"`
}

type Window struct {
	From       string    `json:"from"`
	To         string    `json:"to"`
	SeasonFrom string    `json:"seasonFrom"`
	SeasonTo   string    `json:"seasonTo"`
	TimeZone   string    `json:"timeZone"`
	Cutoff     string    `json:"cutoff"`
	StartsAt   time.Time `json:"startsAt"`
	EndsAt     time.Time `json:"endsAt"`
}

type Coverage struct {
	Expected int       `json:"expected"`
	Reported int       `json:"reported"`
	Status   string    `json:"status"`
	Units    []UnitDay `json:"units"`
}

type UnitDay struct {
	Code             string  `json:"code"`
	Day              string  `json:"day"`
	BoundaryVersion  *string `json:"boundaryVersion"`
	Status           string  `json:"status"`
	DetectionRecords *int64  `json:"overlappingDetectionRecords"`
}

type Totals struct {
	Detections map[string]int64    `json:"detections"`
	Alerts     map[string]int64    `json:"alerts"`
	Incidents  map[string]int64    `json:"incidents"`
	Operations map[string]int64    `json:"operations"`
	Response   map[string]Response `json:"response"`
	Area       Area                `json:"area"`
}

type Response struct {
	Denominator  int64   `json:"denominator"`
	Measured     int64   `json:"measured"`
	Unknown      int64   `json:"unknown"`
	TotalSeconds float64 `json:"totalSeconds"`
}

type Area struct {
	Status     string             `json:"status"`
	Expected   int                `json:"expected"`
	Reported   int                `json:"reported"`
	Hectares   *float64           `json:"hectares"`
	Breakdowns map[string]float64 `json:"breakdowns"`
	Impacts    map[string]int     `json:"impacts"`
}

type Preparedness struct {
	Status          string   `json:"status"`
	ApprovedRecords int      `json:"approvedRecords"`
	WorkRecords     int      `json:"workRecords"`
	CompletedWork   int      `json:"completedWork"`
	TrainingRecords int      `json:"trainingRecords"`
	ReadinessChecks int      `json:"readinessChecks"`
	Trained         Quantity `json:"trained"`
	Target          Quantity `json:"target"`
	WorkDone        Quantity `json:"workDone"`
	ExpenditureINR  Quantity `json:"expenditureINR"`
}

type Quantity struct {
	Status   string   `json:"status"`
	Expected int      `json:"expected"`
	Reported int      `json:"reported"`
	Value    *float64 `json:"value"`
}

type Receipt struct {
	SchemaVersion int       `json:"schemaVersion"`
	ExtractID     string    `json:"extractId"`
	Revision      int       `json:"revision"`
	Kind          string    `json:"kind"`
	Status        string    `json:"status"`
	KeyID         string    `json:"keyId"`
	SHA256        string    `json:"sha256"`
	ValidatedAt   time.Time `json:"validatedAt"`
}

func Digest(raw []byte) string {
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func KeyID(key ed25519.PublicKey) string { return Digest(key)[:32] }

func Sign(doc Document, key ed25519.PrivateKey) ([]byte, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{KeyID(key.Public().(ed25519.PublicKey)), ed25519.Sign(key, raw), raw})
}

// Validate uses a trusted public key supplied by the receiving authority.
// Insignificant JSON whitespace can change. Payload token order and bytes are signed.
func Validate(raw []byte, key ed25519.PublicKey, now time.Time) (Receipt, error) {
	var envelope Envelope
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return Receipt{}, errors.New("invalid JSON extract")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, envelope.Payload); err != nil || len(key) != ed25519.PublicKeySize ||
		envelope.KeyID != KeyID(key) || !ed25519.Verify(key, compact.Bytes(), envelope.Signature) {
		return Receipt{}, errors.New("invalid extract signature")
	}
	data, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil || schema.Validate(data) != nil {
		return Receipt{}, errors.New("extract does not match schema version 1")
	}
	var doc Document
	if err := json.Unmarshal(envelope.Payload, &doc); err != nil {
		return Receipt{}, errors.New("invalid extract payload")
	}
	// Sign emits one representation for each document. Requiring it excludes
	// ambiguous input such as duplicate keys that schema parsers may collapse.
	canonical, err := json.Marshal(doc)
	if err != nil || !bytes.Equal(canonical, compact.Bytes()) {
		return Receipt{}, errors.New("invalid extract payload encoding")
	}
	status, err := Check(doc)
	if err != nil {
		return Receipt{}, err
	}
	return Receipt{1, doc.ID, doc.Revision, doc.Kind, status, envelope.KeyID, Digest(compact.Bytes()), now.UTC()}, nil
}

// Preview checks the same schema and coverage as a signed document.
func Preview(doc Document) (string, error) {
	raw, err := json.Marshal(doc)
	if err != nil {
		return "", err
	}
	envelope, err := json.Marshal(Envelope{"00000000000000000000000000000000", make([]byte, ed25519.SignatureSize), raw})
	if err != nil {
		return "", err
	}
	data, err := jsonschema.UnmarshalJSON(bytes.NewReader(envelope))
	if err != nil || schema.Validate(data) != nil {
		return "", errors.New("extract does not match schema version 1")
	}
	return Check(doc)
}

func Status(expected, reported int, value *float64) string {
	switch {
	case reported == 0:
		return NotReported
	case reported < expected:
		return Incomplete
	case value != nil && *value == 0:
		return Zero
	default:
		return Reported
	}
}

func Check(doc Document) (string, error) {
	w := doc.Window
	if w.From < w.SeasonFrom || w.To > w.SeasonTo || w.To < w.From ||
		!w.StartsAt.Before(w.EndsAt) || doc.GeneratedAt.Before(w.EndsAt) ||
		(doc.Revision == 1) != (doc.Supersedes == nil) || (doc.Supersedes != nil && *doc.Supersedes == doc.ID) {
		return "", errors.New("invalid window or revision")
	}
	zone, err := time.LoadLocation(w.TimeZone)
	if err != nil {
		return "", errors.New("invalid reporting time zone")
	}
	from, err := time.Parse(time.DateOnly, w.From)
	if err != nil || !matchesCutoff(w.StartsAt, zone, from.AddDate(0, 0, -1).Format(time.DateOnly), w.Cutoff) ||
		!matchesCutoff(w.EndsAt, zone, w.To, w.Cutoff) {
		return "", errors.New("window times do not match the local cutoff")
	}
	if doc.Kind == "preparedness" {
		p := doc.Companion
		if p.ApprovedRecords != p.WorkRecords+p.TrainingRecords+p.ReadinessChecks || p.CompletedWork > p.WorkRecords ||
			p.Target.Expected != p.WorkRecords || p.WorkDone.Expected != p.WorkRecords ||
			p.ExpenditureINR.Expected != p.WorkRecords || p.Trained.Expected != p.TrainingRecords {
			return "", errors.New("invalid preparedness counts")
		}
		status := Reported
		if p.ApprovedRecords == 0 {
			status = NotReported
		}
		for _, q := range []Quantity{p.Target, p.WorkDone, p.Trained, p.ExpenditureINR} {
			if q.Reported > q.Expected || (q.Reported == 0) != (q.Value == nil) ||
				q.Status != Status(q.Expected, q.Reported, q.Value) {
				return "", errors.New("invalid preparedness coverage")
			}
			if q.Expected > 0 && q.Reported < q.Expected {
				status = Incomplete
			}
		}
		if p.Status != status {
			return "", errors.New("invalid preparedness status")
		}
		return status, nil
	}
	c := doc.Coverage
	seen := map[string]bool{}
	reported, complete, observed := 0, 0, 0
	allNil := true
	var maxDetections, sumDetections int64
	for _, unit := range c.Units {
		id := unit.Day + "/" + unit.Code
		if seen[id] || unit.Day < w.From || unit.Day > w.To ||
			(unit.Status == NotReported && unit.DetectionRecords != nil) ||
			((unit.Status == Reported || unit.Status == Zero) && unit.DetectionRecords == nil) ||
			(unit.Status == Zero && *unit.DetectionRecords != 0) {
			return "", errors.New("invalid daily unit coverage")
		}
		seen[id] = true
		if count := unit.DetectionRecords; count != nil {
			observed++
			maxDetections = max(maxDetections, *count)
			// The useful bound need not exceed the largest exact JSON integer.
			sumDetections = min(9007199254740991, sumDetections+*count)
		}
		if unit.Status != NotReported {
			reported++
		}
		if unit.Status == Reported || unit.Status == Zero {
			complete++
		}
		allNil = allNil && unit.Status == Zero
	}
	if c.Expected != len(c.Units) || c.Reported != reported {
		return "", errors.New("invalid coverage totals")
	}
	var zero float64
	expected := Status(c.Expected, reported, nil)
	if reported > 0 && complete < c.Expected {
		expected = Incomplete
	}
	if reported > 0 && allNil {
		expected = Status(c.Expected, reported, &zero)
	}
	if c.Status != expected || (observed == 0) != (doc.Totals == nil) {
		return "", errors.New("invalid return status")
	}
	if t := doc.Totals; t != nil {
		var raw, dispositions int64
		for key, n := range t.Detections {
			if key != "deduplicated" && key != "records" {
				if n > t.Detections["records"]-raw {
					return "", errors.New("inconsistent detection source totals")
				}
				raw += n
			}
		}
		for key, n := range t.Alerts {
			if key != "routed" && key != "awaiting_at_cutoff" {
				dispositions += n
			}
		}
		if raw != t.Detections["records"] || raw < maxDetections || raw > sumDetections ||
			t.Detections["deduplicated"] > raw || (raw == 0) != (t.Detections["deduplicated"] == 0) ||
			dispositions != t.Alerts["routed"] {
			return "", errors.New("inconsistent detection or alert totals")
		}
		for stage, response := range t.Response {
			if response.Measured+response.Unknown != response.Denominator ||
				(response.Measured == 0 && response.TotalSeconds != 0) {
				return "", fmt.Errorf("invalid response denominator for %s", stage)
			}
		}
		a := t.Area
		var hectares float64
		for _, value := range a.Breakdowns {
			hectares += value
		}
		if a.Reported > a.Expected || (a.Reported == 0) != (a.Hectares == nil) ||
			a.Status != Status(a.Expected, a.Reported, a.Hectares) ||
			(a.Hectares != nil && math.Abs(hectares-*a.Hectares) > 1e-6) ||
			(a.Reported == 0 && (len(a.Breakdowns) > 0 || len(a.Impacts) > 0)) {
			return "", errors.New("invalid area coverage or totals")
		}
		for _, n := range a.Impacts {
			if n > a.Reported {
				return "", errors.New("invalid impact counts")
			}
		}
		if c.Status == Zero && (raw != 0 || dispositions != 0 || t.Alerts["awaiting_at_cutoff"] != 0 ||
			a.Expected != 0 || len(t.Response) != 0 || !countsAreZero(t.Incidents) || !countsAreZero(t.Operations)) {
			return "", errors.New("a nil return contains activity")
		}
	}
	return c.Status, nil
}

func countsAreZero(counts map[string]int64) bool {
	for _, count := range counts {
		if count != 0 {
			return false
		}
	}
	return true
}

func matchesCutoff(instant time.Time, zone *time.Location, day, cutoff string) bool {
	// A skipped local time is resolved by PostgreSQL using the pre-transition
	// offset. During a repeated hour, either occurrence has the same wall time.
	nominal := day + "T" + cutoff
	local := instant.In(zone)
	if local.Format("2006-01-02T15:04") == nominal {
		return local.Second() == 0 && local.Nanosecond() == 0
	}
	_, oldOffset := local.Add(-24 * time.Hour).Zone()
	_, newOffset := local.Zone()
	if oldOffset >= newOffset {
		return false
	}
	previous := local.Add(time.Duration(oldOffset-newOffset) * time.Second)
	if _, offset := previous.Zone(); offset != oldOffset {
		return false
	}
	wall := instant.In(time.FixedZone("", oldOffset))
	return wall.Format("2006-01-02T15:04") == nominal && wall.Second() == 0 && wall.Nanosecond() == 0
}
