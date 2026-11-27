package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// This file checks a submission's answers against its form version. The
// form has already passed the form-schema validator, so the shape of every
// form property can be trusted. The answers cannot: they come from a phone
// or a browser. The rules follow the form format (packages/form-schema/SPEC.md,
// sections 8 to 13).

// formTree is a published form, read once and kept for checking answers.
type formTree struct {
	byKey     map[string]*formNode
	top       []*formNode // fields and repeating groups outside any repeating group
	stageStep map[string]int
	hasStages bool
	lists     map[string][]any
}

type formNode struct {
	key, kind string
	node      map[string]any
	parent    *formNode // the section, group or repeating group that holds it
	repeat    *formNode // the innermost repeating group around it
	stage     int
	children  []*formNode // for a repeating group: its fields and inner repeating groups
}

func (n *formNode) isField() bool {
	return n.kind != "section" && n.kind != "group" && n.kind != "repeat"
}

// parseFormTree reads a form definition that has passed the validator.
func parseFormTree(definition []byte) (*formTree, error) {
	var form map[string]any
	if err := json.Unmarshal(definition, &form); err != nil {
		return nil, err
	}
	t := &formTree{byKey: map[string]*formNode{}, stageStep: map[string]int{}, lists: map[string][]any{}}
	stages := map[string]int{}
	if list, ok := form["stages"].([]any); ok {
		t.hasStages = true
		for i, s := range list {
			stage := s.(map[string]any)
			stages[stage["key"].(string)] = i
			t.stageStep[stage["workflowStep"].(string)] = i
		}
	}
	if list, ok := form["lists"].([]any); ok {
		for _, l := range list {
			l := l.(map[string]any)
			t.lists[l["key"].(string)] = l["choices"].([]any)
		}
	}
	var walk func(parent, repeat *formNode, items []any, stage int)
	walk = func(parent, repeat *formNode, items []any, stage int) {
		for _, raw := range items {
			m := raw.(map[string]any)
			n := &formNode{key: m["key"].(string), kind: m["type"].(string), node: m, parent: parent, repeat: repeat, stage: stage}
			t.byKey[n.key] = n
			if n.isField() || n.kind == "repeat" {
				if repeat == nil {
					t.top = append(t.top, n)
				} else {
					repeat.children = append(repeat.children, n)
				}
			}
			switch n.kind {
			case "group":
				walk(n, repeat, m["items"].([]any), stage)
			case "repeat":
				walk(n, n, m["items"].([]any), stage)
			}
		}
	}
	for _, raw := range form["sections"].([]any) {
		m := raw.(map[string]any)
		stage := -1
		if name, ok := m["stage"].(string); ok {
			stage = stages[name]
		}
		sec := &formNode{key: m["key"].(string), kind: "section", node: m, stage: stage}
		t.byKey[sec.key] = sec
		walk(sec, nil, m["items"].([]any), stage)
	}
	return t, nil
}

// alertFacts are the values an alert-value field copies from the alert.
type alertFacts struct {
	ID          int64
	ObservedAt  time.Time
	RaisedAt    time.Time
	Longitude   float64
	Latitude    float64
	Unit        string
	ForestClass string
	Source      string
	Confidence  *string
	FirePower   *float64
}

// storedAttachment is what the checks need to know about an uploaded file.
type storedAttachment struct {
	ContentType string
	FileName    string
	SizeBytes   int64
}

// answerCheck holds everything the checks need besides the form.
type answerCheck struct {
	tree  *formTree
	stage int // the stage being filled in, or -1 for a form without stages
	alert alertFacts
	now   time.Time
	// attachments uploaded for the same alert, by ID.
	attachments map[string]storedAttachment
	// measure gives the area in hectares of a GeoJSON polygon, or the length
	// in kilometres of a GeoJSON line, on the ellipsoid.
	measure func(geometry json.RawMessage) (float64, error)

	problems []string
	// used lists the attachments each field uses.
	used map[string][]string
}

// scope is the top level of the answers or one repeating group entry.
type scope struct {
	repeat  *formNode // nil at the top level
	parent  *scope
	label   string // where the scope is, for messages: "" or `entry 2 of "teams"`
	answers map[string]any
	clean   map[string]any // answers that passed their type checks
	entries map[string][]*scope
	memo    map[string]evaluated
	busy    map[string]bool
}

type evaluated struct {
	value any
	ok    bool
}

// checkAnswers checks the answers and returns them as the server keeps
// them: hidden answers left out, calculations and alert values filled in.
// It returns the problems, each a plain-English sentence, when the answers
// cannot be accepted.
func checkAnswers(c *answerCheck, raw json.RawMessage) (map[string]any, []string) {
	c.used = map[string][]string{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var answers any
	if err := decoder.Decode(&answers); err != nil {
		return nil, []string{"values must be a JSON object of answers by field key."}
	}
	object, ok := answers.(map[string]any)
	if !ok {
		return nil, []string{"values must be a JSON object of answers by field key."}
	}
	top := c.newScope(nil, nil, "", object)
	if len(c.problems) > 0 {
		return nil, c.problems
	}
	out := c.output(top, c.tree.top)
	if len(c.problems) > 0 {
		return nil, c.problems
	}
	return out, nil
}

func (c *answerCheck) add(sc *scope, format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	if sc != nil && sc.label != "" {
		msg = "In " + sc.label + ": " + msg
	}
	c.problems = append(c.problems, msg)
}

// newScope type-checks the answers given at one level and the entries of
// each repeating group in it.
func (c *answerCheck) newScope(repeat *formNode, parent *scope, label string, answers map[string]any) *scope {
	sc := &scope{repeat: repeat, parent: parent, label: label, answers: answers, clean: map[string]any{},
		entries: map[string][]*scope{}, memo: map[string]evaluated{}, busy: map[string]bool{}}
	members := c.tree.top
	if repeat != nil {
		members = repeat.children
	}
	keys := make([]string, 0, len(answers))
	for k := range answers {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		value := answers[key]
		n := c.tree.byKey[key]
		switch {
		case n == nil || (!n.isField() && n.kind != "repeat"):
			c.add(sc, `values has "%s", which is not a field of this form.`, key)
		case !slices.Contains(members, n):
			if n.repeat != nil {
				c.add(sc, `Field "%s" belongs inside the repeating group "%s".`, key, n.repeat.key)
			} else {
				c.add(sc, `Field "%s" is not inside a repeating group.`, key)
			}
		case n.kind == "calculated" || n.kind == "alert-value":
			// The server works these out itself and ignores what was sent.
		case value == nil:
			// A null answer is the same as no answer.
		case n.kind == "repeat":
			list, ok := value.([]any)
			if !ok {
				c.add(sc, `Repeating group "%s" must be a list of entries.`, key)
				continue
			}
			for i, entry := range list {
				object, ok := entry.(map[string]any)
				where := fmt.Sprintf(`entry %d of "%s"`, i+1, key)
				if label != "" {
					where = label + ", " + where
				}
				if !ok {
					c.add(sc, `Entry %d of "%s" must be an object of answers.`, i+1, key)
					continue
				}
				sc.entries[key] = append(sc.entries[key], c.newScope(n, sc, where, object))
			}
		default:
			if clean, ok := c.checkValue(sc, n, value); ok {
				sc.clean[key] = clean
			}
		}
	}
	return sc
}

var (
	datePattern     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	timePattern     = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9](:[0-5][0-9])?$`)
	dateTimePattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}(:[0-9]{2}(\.[0-9]{1,9})?)?(Z|[+-][0-9]{2}:[0-9]{2})$`)
	uuidPattern     = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// clockSkew allows for a phone clock a little ahead of the server.
const clockSkew = 5 * time.Minute

func parseDateTime(s string) (time.Time, bool) {
	if !dateTimePattern.MatchString(s) {
		return time.Time{}, false
	}
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02T15:04Z07:00"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

// timeSeconds turns a time of day such as 14:30 into seconds after midnight.
func timeSeconds(s string) (int, bool) {
	if !timePattern.MatchString(s) {
		return 0, false
	}
	h, _ := strconv.Atoi(s[0:2])
	m, _ := strconv.Atoi(s[3:5])
	sec := 0
	if len(s) == 8 {
		sec, _ = strconv.Atoi(s[6:8])
	}
	return h*3600 + m*60 + sec, true
}

func number(n map[string]any, key string) (float64, bool) {
	v, ok := n[key].(float64)
	return v, ok
}

// checkValue checks one answer against its field. It returns the value in
// the form the conditions and calculations use.
func (c *answerCheck) checkValue(sc *scope, n *formNode, value any) (any, bool) {
	who := `Field "` + n.key + `"`
	m := n.node
	switch n.kind {
	case "text":
		s, ok := value.(string)
		if !ok {
			c.add(sc, "%s must be text.", who)
			return nil, false
		}
		if s == "" {
			return nil, false
		}
		length := float64(utf8.RuneCountInString(s))
		if lo, ok := number(m, "minLength"); ok && length < lo {
			c.add(sc, "%s needs at least %v characters.", who, lo)
		}
		if hi, ok := number(m, "maxLength"); ok && length > hi {
			c.add(sc, "%s can have at most %v characters.", who, hi)
		}
		if p, ok := m["pattern"].(string); ok && !regexp.MustCompile("^(?:"+p+")$").MatchString(s) {
			if hint, ok := m["patternHint"].(map[string]any); ok {
				c.add(sc, "%s does not match the expected pattern: %s.", who, strings.TrimSuffix(hint["en"].(string), "."))
			} else {
				c.add(sc, "%s does not match the expected pattern.", who)
			}
		}
		return s, true
	case "number":
		num, ok := value.(json.Number)
		f, err := num.Float64()
		if !ok || err != nil || math.IsInf(f, 0) {
			c.add(sc, "%s must be a number.", who)
			return nil, false
		}
		if lo, ok := number(m, "min"); ok && f < lo {
			c.add(sc, "%s must be at least %v.", who, lo)
		}
		if hi, ok := number(m, "max"); ok && f > hi {
			c.add(sc, "%s must be at most %v.", who, hi)
		}
		if d, ok := number(m, "decimals"); ok && !hasDecimals(f, int(d)) {
			if d == 0 {
				c.add(sc, "%s must be a whole number.", who)
			} else {
				c.add(sc, "%s can have at most %v decimal places.", who, d)
			}
		}
		return f, true
	case "date":
		s, _ := value.(string)
		day, err := time.Parse(time.DateOnly, s)
		if !datePattern.MatchString(s) || err != nil {
			c.add(sc, "%s must be a date such as 2026-03-15.", who)
			return nil, false
		}
		// The latest date anywhere on Earth is 14 hours ahead of universal time.
		if m["notInFuture"] == true && day.After(c.now.UTC().Add(14*time.Hour)) {
			c.add(sc, "%s cannot be in the future.", who)
		}
		return s, true
	case "time":
		s, _ := value.(string)
		if _, ok := timeSeconds(s); !ok {
			c.add(sc, "%s must be a time such as 14:30.", who)
			return nil, false
		}
		return s, true
	case "date-time":
		s, _ := value.(string)
		t, ok := parseDateTime(s)
		if !ok {
			c.add(sc, "%s must be a date and time with its offset from universal time, such as 2026-03-15T14:30:00+05:30.", who)
			return nil, false
		}
		if m["notInFuture"] == true && t.After(c.now.Add(clockSkew)) {
			c.add(sc, "%s cannot be in the future.", who)
		}
		return s, true
	case "single-choice":
		s, ok := value.(string)
		if !ok || !c.hasChoice(n, s) {
			c.add(sc, "%s has no choice %s.", who, quoteValue(value))
			return nil, false
		}
		return s, true
	case "multiple-choice":
		list, ok := value.([]any)
		if !ok {
			c.add(sc, "%s must be a list of choices.", who)
			return nil, false
		}
		var keys []string
		for _, v := range list {
			s, ok := v.(string)
			switch {
			case !ok || !c.hasChoice(n, s):
				c.add(sc, "%s has no choice %s.", who, quoteValue(v))
				return nil, false
			case slices.Contains(keys, s):
				c.add(sc, `%s lists "%s" more than once.`, who, s)
				return nil, false
			}
			keys = append(keys, s)
		}
		if len(keys) == 0 {
			return nil, false
		}
		if lo, ok := number(m, "minSelected"); ok && float64(len(keys)) < lo {
			c.add(sc, "%s needs at least %v choices.", who, lo)
		}
		if hi, ok := number(m, "maxSelected"); ok && float64(len(keys)) > hi {
			c.add(sc, "%s can have at most %v choices.", who, hi)
		}
		return keys, true
	case "point", "track", "polygon":
		geometry, problem := checkGeoJSON(n.kind, value)
		if problem != "" {
			c.add(sc, "%s %s", who, problem)
			return nil, false
		}
		return geometry, true
	case "photo", "file":
		list, ok := value.([]any)
		if !ok {
			c.add(sc, "%s must be a list of attachment IDs.", who)
			return nil, false
		}
		ids := make([]string, 0, len(list))
		for _, v := range list {
			id, _ := v.(string)
			if !c.checkAttachment(sc, n, id) {
				return nil, false
			}
			if slices.Contains(ids, id) {
				c.add(sc, `%s lists the attachment "%s" more than once.`, who, id)
				return nil, false
			}
			ids = append(ids, id)
		}
		if len(ids) == 0 {
			return nil, false
		}
		hi := 20.0
		if v, ok := number(m, "maxCount"); ok {
			hi = v
		}
		if lo, ok := number(m, "minCount"); ok && float64(len(ids)) < lo {
			c.add(sc, "%s needs at least %v files.", who, lo)
		}
		if float64(len(ids)) > hi {
			c.add(sc, "%s can have at most %v files.", who, hi)
		}
		return ids, true
	case "signature":
		id, _ := value.(string)
		if !c.checkAttachment(sc, n, id) {
			return nil, false
		}
		return id, true
	}
	return nil, false
}

func quoteValue(v any) string {
	if s, ok := v.(string); ok {
		return strconv.Quote(s)
	}
	data, _ := json.Marshal(v)
	return string(data)
}

func hasDecimals(f float64, decimals int) bool {
	scaled := f * math.Pow10(decimals)
	return math.Abs(scaled-math.Round(scaled)) <= 1e-9*math.Max(1, math.Abs(scaled))
}

func (c *answerCheck) choicesOf(n *formNode) []any {
	if list, ok := n.node["choices"].([]any); ok {
		return list
	}
	return c.tree.lists[n.node["list"].(string)]
}

func (c *answerCheck) choice(n *formNode, key string) map[string]any {
	for _, raw := range c.choicesOf(n) {
		if m := raw.(map[string]any); m["key"] == key {
			return m
		}
	}
	return nil
}

func (c *answerCheck) hasChoice(n *formNode, key string) bool { return c.choice(n, key) != nil }

var attachmentKinds = map[string][]string{
	"photo":     {"image/jpeg", "image/png", "image/webp"},
	"signature": {"image/png", "image/jpeg", "image/webp"},
}

func (c *answerCheck) checkAttachment(sc *scope, n *formNode, id string) bool {
	who := `Field "` + n.key + `"`
	if !uuidPattern.MatchString(id) {
		c.add(sc, "%s must give attachment IDs, as returned by the upload.", who)
		return false
	}
	a, found := c.attachments[id]
	if !found {
		c.add(sc, `%s uses the attachment "%s", which was not uploaded for this alert.`, who, id)
		return false
	}
	if kinds, limited := attachmentKinds[n.kind]; limited && !slices.Contains(kinds, a.ContentType) {
		c.add(sc, `%s needs an image, but the attachment "%s" is %s.`, who, id, a.ContentType)
		return false
	}
	if accept, ok := n.node["accept"].([]any); ok {
		name := strings.ToLower(a.FileName)
		if !slices.ContainsFunc(accept, func(ending any) bool { return strings.HasSuffix(name, strings.ToLower(ending.(string))) }) {
			c.add(sc, `%s does not accept the file "%s".`, who, a.FileName)
			return false
		}
	}
	if mb, ok := number(n.node, "maxSizeMegabytes"); ok && float64(a.SizeBytes) > mb*1024*1024 {
		c.add(sc, `%s accepts files up to %v MB, but "%s" is larger.`, who, mb, a.FileName)
		return false
	}
	return true
}

// checkGeoJSON checks a point, track or polygon. It returns the geometry as
// compact JSON, or the end of a sentence explaining what is wrong.
func checkGeoJSON(kind string, value any) (json.RawMessage, string) {
	want := map[string]string{"point": "Point", "track": "LineString", "polygon": "Polygon"}[kind]
	problem := "must be a GeoJSON " + want + " in longitude and latitude."
	m, ok := value.(map[string]any)
	if !ok || m["type"] != want || len(m) != 2 {
		return nil, problem
	}
	position := func(v any) ([]float64, bool) {
		list, ok := v.([]any)
		if !ok || len(list) < 2 || len(list) > 3 {
			return nil, false
		}
		out := make([]float64, len(list))
		for i, x := range list {
			num, ok := x.(json.Number)
			f, err := num.Float64()
			if !ok || err != nil || math.IsInf(f, 0) {
				return nil, false
			}
			out[i] = f
		}
		return out, out[0] >= -180 && out[0] <= 180 && out[1] >= -90 && out[1] <= 90
	}
	line := func(v any, least int, closed bool) bool {
		list, ok := v.([]any)
		if !ok || len(list) < least {
			return false
		}
		var first, last []float64
		for i, p := range list {
			pos, ok := position(p)
			if !ok {
				return false
			}
			if i == 0 {
				first = pos
			}
			last = pos
		}
		return !closed || (first[0] == last[0] && first[1] == last[1])
	}
	coordinates := m["coordinates"]
	switch kind {
	case "point":
		ok = false
		if _, valid := position(coordinates); valid {
			ok = true
		}
	case "track":
		ok = line(coordinates, 2, false)
	case "polygon":
		rings, isList := coordinates.([]any)
		ok = isList && len(rings) > 0
		for _, ring := range rings {
			ok = ok && line(ring, 4, true)
		}
		if !ok {
			return nil, "must be a GeoJSON Polygon with closed rings of at least four positions."
		}
	}
	if !ok {
		return nil, problem
	}
	data, _ := json.Marshal(m)
	return data, ""
}

// find returns the scope that holds a node's answer, seen from sc.
func find(sc *scope, n *formNode) *scope {
	for s := sc; s != nil; s = s.parent {
		if s.repeat == n.repeat {
			return s
		}
	}
	return nil
}

// visible says whether a node is shown. holder is the scope that holds the
// node: the entry of its innermost repeating group, or the top level.
func (c *answerCheck) visible(n *formNode, holder *scope) bool {
	if n == nil || holder == nil {
		return n == nil
	}
	if c.tree.hasStages && n.stage > c.stage {
		return false
	}
	if p := n.parent; p != nil {
		// A repeating group is shown or hidden in the scope around its entries.
		around := holder
		if p.kind == "repeat" && p == n.repeat {
			around = holder.parent
		}
		if !c.visible(p, around) {
			return false
		}
	}
	if cond, ok := n.node["showWhen"].(map[string]any); ok {
		return c.condition(cond, holder)
	}
	return true
}

// value returns a field's answer as seen from sc: nothing when the field is
// hidden or unanswered, the server's own result for calculations and alert
// values, and the checked answer otherwise.
func (c *answerCheck) value(key string, sc *scope) (any, bool) {
	n := c.tree.byKey[key]
	if n == nil {
		return nil, false
	}
	holder := find(sc, n)
	if holder == nil {
		return nil, false
	}
	if got, done := holder.memo[key]; done {
		return got.value, got.ok
	}
	if holder.busy[key] {
		return nil, false // the validator refuses loops, so this is only a safeguard
	}
	holder.busy[key] = true
	var result evaluated
	if c.visible(n, holder) {
		switch n.kind {
		case "calculated":
			if f, ok := c.calculate(n.node["calculate"].(map[string]any), holder); ok && !math.IsNaN(f) && !math.IsInf(f, 0) {
				if d, has := number(n.node, "decimals"); has {
					f = math.Round(f*math.Pow10(int(d))) / math.Pow10(int(d))
				}
				result = evaluated{f, true}
			}
		case "alert-value":
			result.value, result.ok = c.alertValue(n.node["source"].(string))
		default:
			result.value, result.ok = holder.clean[key]
		}
	}
	holder.busy[key] = false
	holder.memo[key] = result
	return result.value, result.ok
}

func (c *answerCheck) alertValue(source string) (any, bool) {
	a := c.alert
	switch source {
	case "alert-id":
		return strconv.FormatInt(a.ID, 10), true
	case "observed-at":
		return a.ObservedAt.UTC().Format(time.RFC3339), true
	case "raised-at":
		return a.RaisedAt.UTC().Format(time.RFC3339), true
	case "location":
		if a.Source == "lsasaf" {
			return nil, false
		}
		data, _ := json.Marshal(map[string]any{"type": "Point", "coordinates": []float64{a.Longitude, a.Latitude}})
		return json.RawMessage(data), true
	case "unit":
		return a.Unit, a.Unit != ""
	case "forest-class":
		return a.ForestClass, a.ForestClass != ""
	case "source":
		return a.Source, a.Source != ""
	case "confidence":
		if a.Confidence == nil {
			return nil, false
		}
		return *a.Confidence, true
	case "fire-power":
		if a.FirePower == nil {
			return nil, false
		}
		return *a.FirePower, true
	}
	return nil, false
}

// compare orders two answers of the same field: -1, 0 or 1.
func compareAnswer(n *formNode, answer any, want any) (int, bool) {
	kind := n.kind
	if kind == "alert-value" {
		kind = map[string]string{"observed-at": "date-time", "raised-at": "date-time", "fire-power": "number"}[n.node["source"].(string)]
	}
	switch kind {
	case "number", "calculated":
		a, okA := answer.(float64)
		b, okB := want.(float64)
		if !okA || !okB {
			return 0, false
		}
		return cmpFloat(a, b), true
	case "date":
		a, _ := answer.(string)
		b, _ := want.(string)
		return strings.Compare(a, b), true
	case "time":
		a, okA := timeSeconds(fmt.Sprint(answer))
		b, okB := timeSeconds(fmt.Sprint(want))
		return cmpInt(a, b), okA && okB
	case "date-time":
		a, okA := parseDateTime(fmt.Sprint(answer))
		b, okB := parseDateTime(fmt.Sprint(want))
		return a.Compare(b), okA && okB
	}
	return 0, false
}

func cmpFloat(a, b float64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpInt(a, b int) int { return cmpFloat(float64(a), float64(b)) }

func (c *answerCheck) condition(cond map[string]any, sc *scope) bool {
	switch op := cond["op"].(string); op {
	case "all", "any":
		for _, sub := range cond["conditions"].([]any) {
			got := c.condition(sub.(map[string]any), sc)
			if op == "any" && got {
				return true
			}
			if op == "all" && !got {
				return false
			}
		}
		return op == "all"
	case "not":
		return !c.condition(cond["condition"].(map[string]any), sc)
	default:
		key := cond["field"].(string)
		answer, answered := c.value(key, sc)
		if op == "answered" {
			return answered
		}
		if !answered {
			return false
		}
		want := cond["value"]
		n := c.tree.byKey[key]
		switch op {
		case "includes":
			list, _ := answer.([]string)
			return slices.Contains(list, fmt.Sprint(want))
		case "equals", "not-equals":
			same := false
			if order, ok := compareAnswer(n, answer, want); ok {
				same = order == 0
			} else {
				same = fmt.Sprint(answer) == fmt.Sprint(want)
			}
			return same == (op == "equals")
		}
		order, ok := compareAnswer(n, answer, want)
		if !ok {
			return false
		}
		switch op {
		case "more-than":
			return order > 0
		case "at-least":
			return order >= 0
		case "less-than":
			return order < 0
		case "at-most":
			return order <= 0
		}
	}
	return false
}

func (c *answerCheck) calculate(e map[string]any, sc *scope) (float64, bool) {
	args := func() ([]float64, bool) {
		var out []float64
		for _, raw := range e["args"].([]any) {
			v, ok := c.calculate(raw.(map[string]any), sc)
			if !ok {
				return nil, false
			}
			out = append(out, v)
		}
		return out, true
	}
	switch e["op"].(string) {
	case "number":
		return e["value"].(float64), true
	case "field":
		v, ok := c.value(e["field"].(string), sc)
		f, isNumber := v.(float64)
		return f, ok && isNumber
	case "add", "multiply":
		values, ok := args()
		if !ok {
			return 0, false
		}
		total := values[0]
		for _, v := range values[1:] {
			if e["op"] == "add" {
				total += v
			} else {
				total *= v
			}
		}
		return total, true
	case "subtract", "divide":
		values, ok := args()
		if !ok {
			return 0, false
		}
		if e["op"] == "subtract" {
			return values[0] - values[1], true
		}
		if values[1] == 0 {
			return 0, false
		}
		return values[0] / values[1], true
	case "sum", "count":
		group := c.tree.byKey[e["group"].(string)]
		holder := find(sc, &formNode{repeat: group.repeat})
		if holder == nil || !c.visible(group, holder) {
			return 0, false
		}
		entries := holder.entries[group.key]
		if e["op"] == "count" {
			return float64(len(entries)), true
		}
		total := 0.0
		for _, entry := range entries {
			if v, ok := c.value(e["field"].(string), entry); ok {
				total += v.(float64)
			}
		}
		return total, true
	case "area", "length":
		v, ok := c.value(e["field"].(string), sc)
		if !ok || c.measure == nil {
			return 0, false
		}
		f, err := c.measure(v.(json.RawMessage))
		return f, err == nil
	case "hours-between":
		start, okStart := c.value(e["start"].(string), sc)
		end, okEnd := c.value(e["end"].(string), sc)
		a, okA := parseDateTime(fmt.Sprint(start))
		b, okB := parseDateTime(fmt.Sprint(end))
		if !okStart || !okEnd || !okA || !okB {
			return 0, false
		}
		return b.Sub(a).Hours(), true
	}
	return 0, false
}

// required says whether a shown field must be answered.
func (c *answerCheck) required(n *formNode, sc *scope) bool {
	if n.node["required"] == true {
		return true
	}
	if cond, ok := n.node["requireWhen"].(map[string]any); ok {
		return c.condition(cond, sc)
	}
	return false
}

// output builds the answers the server keeps for one scope and checks the
// rules that depend on other answers: required fields, cascading choices
// and the number of repeating group entries.
func (c *answerCheck) output(sc *scope, members []*formNode) map[string]any {
	out := map[string]any{}
	for _, n := range members {
		if n.kind == "repeat" {
			entries := sc.entries[n.key]
			if !c.visible(n, sc) {
				continue
			}
			if lo, ok := number(n.node, "minRepeats"); ok && float64(len(entries)) < lo {
				c.add(sc, `Repeating group "%s" needs at least %v entries.`, n.key, lo)
			}
			if hi, ok := number(n.node, "maxRepeats"); ok && float64(len(entries)) > hi {
				c.add(sc, `Repeating group "%s" can have at most %v entries.`, n.key, hi)
			}
			if len(entries) == 0 {
				continue
			}
			list := make([]any, len(entries))
			for i, entry := range entries {
				list[i] = c.output(entry, n.children)
			}
			out[n.key] = list
			continue
		}
		v, ok := c.value(n.key, sc)
		if !ok {
			if c.visible(n, sc) && n.kind != "calculated" && n.kind != "alert-value" && c.required(n, sc) {
				c.add(sc, `Field "%s" is required.`, n.key)
			}
			continue
		}
		if parent, has := n.node["parent"].(string); has {
			c.checkCascade(sc, n, parent, v)
		}
		switch n.kind {
		case "photo", "file":
			c.used[n.key] = append(c.used[n.key], v.([]string)...)
		case "signature":
			c.used[n.key] = append(c.used[n.key], v.(string))
		}
		out[n.key] = v
	}
	return out
}

func (c *answerCheck) checkCascade(sc *scope, n *formNode, parentKey string, v any) {
	parentAnswer, answered := c.value(parentKey, sc)
	keys, _ := v.([]string)
	if s, ok := v.(string); ok {
		keys = []string{s}
	}
	for _, key := range keys {
		if !answered || c.choice(n, key)["parent"] != parentAnswer {
			c.add(sc, `Field "%s": the choice "%s" does not go with the answer to "%s".`, n.key, key, parentKey)
			return
		}
	}
}

// errNoStage is returned when a staged form is used at a step it has no stage for.
var errNoStage = errors.New("the form has no stage at this step")

// stageAt returns the stage a staged form fills in at a workflow step, or -1
// for a form without stages.
func (t *formTree) stageAt(step string) (int, error) {
	if !t.hasStages {
		return -1, nil
	}
	if stage, ok := t.stageStep[step]; ok {
		return stage, nil
	}
	return 0, errNoStage
}
