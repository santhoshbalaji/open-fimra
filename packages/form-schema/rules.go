package formschema

import (
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// This file applies the rules that a JSON Schema cannot express. It runs
// only on forms that already pass the schema, so it can trust the shape of
// every value. The TypeScript copy in ts/form-schema.ts follows the same
// steps in the same order.

type item struct {
	key     string
	kind    string // "section", "group", "repeat" or a field type
	node    map[string]any
	path    string
	parent  *item    // the section or group that holds the item
	repeats []string // keys of the repeating groups around the item, outermost first
	stage   int      // index of the item's stage, or -1
	deps    []*item  // fields this item's visibility, value or choices depend on
}

type rulesChecker struct {
	form      map[string]any
	out       []string
	items     []*item
	byKey     map[string]*item
	lists     map[string][]any
	stages    map[string]int
	hasStages bool
}

func checkRules(form map[string]any) []string {
	r := &rulesChecker{form: form, byKey: map[string]*item{}, lists: map[string][]any{}, stages: map[string]int{}}
	r.checkLanguages()
	r.checkStages()
	r.checkLists()
	r.collectItems()
	for _, it := range r.items {
		r.checkItem(it)
	}
	r.checkColumnNames()
	r.checkCycles()
	r.checkStagesUsed()
	r.checkLayout()
	r.checkTranslations()
	return r.out
}

func (r *rulesChecker) add(msg string) { r.out = append(r.out, msg) }

func objects(v any) []map[string]any {
	list, _ := v.([]any)
	out := make([]map[string]any, len(list))
	for i, x := range list {
		out[i] = x.(map[string]any)
	}
	return out
}

func stringList(v any) []string {
	list, _ := v.([]any)
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = x.(string)
	}
	return out
}

func (r *rulesChecker) checkLanguages() {
	langs, ok := r.form["languages"]
	if !ok {
		return
	}
	seen := map[string]bool{}
	for _, l := range stringList(langs) {
		if seen[l] {
			r.add(`languages lists "` + l + `" more than once.`)
		}
		seen[l] = true
	}
	if !seen["en"] {
		r.add(`languages must include "en".`)
	}
}

func (r *rulesChecker) checkStages() {
	_, r.hasStages = r.form["stages"]
	steps := map[string]string{}
	for i, s := range objects(r.form["stages"]) {
		key, step := s["key"].(string), s["workflowStep"].(string)
		if _, dup := r.stages[key]; dup {
			r.add(`Stage "` + key + `" is defined more than once.`)
		} else {
			r.stages[key] = i
		}
		if other, dup := steps[step]; dup {
			r.add(`Stages "` + other + `" and "` + key + `" both use the workflow step "` + step + `".`)
		} else {
			steps[step] = key
		}
	}
}

func (r *rulesChecker) checkLists() {
	for _, l := range objects(r.form["lists"]) {
		key := l["key"].(string)
		if _, dup := r.lists[key]; dup {
			r.add(`List "` + key + `" is defined more than once.`)
			continue
		}
		choices := l["choices"].([]any)
		r.lists[key] = choices
		for _, c := range duplicateChoices(choices) {
			r.add(`List "` + key + `" has the choice "` + c + `" more than once.`)
		}
	}
}

func duplicateChoices(choices []any) []string {
	seen := map[string]bool{}
	var dups []string
	for _, c := range objects(choices) {
		k := c["key"].(string)
		if seen[k] {
			dups = append(dups, k)
		}
		seen[k] = true
	}
	return dups
}

func (r *rulesChecker) collectItems() {
	for i, s := range objects(r.form["sections"]) {
		stage := -1
		if name, ok := s["stage"].(string); ok {
			if idx, found := r.stages[name]; found {
				stage = idx
			}
		}
		sec := r.addItem(s, "section", "sections["+strconv.Itoa(i)+"]", nil, nil, stage)
		r.collectChildren(sec, nil)
	}
}

func (r *rulesChecker) collectChildren(parent *item, repeats []string) {
	for i, n := range objects(parent.node["items"]) {
		it := r.addItem(n, n["type"].(string), parent.path+".items["+strconv.Itoa(i)+"]", parent, repeats, parent.stage)
		switch it.kind {
		case "group":
			r.collectChildren(it, repeats)
		case "repeat":
			r.collectChildren(it, append(slices.Clone(repeats), it.key))
		}
	}
}

func (r *rulesChecker) addItem(n map[string]any, kind, path string, parent *item, repeats []string, stage int) *item {
	it := &item{key: n["key"].(string), kind: kind, node: n, path: path, parent: parent, repeats: repeats, stage: stage}
	if _, dup := r.byKey[it.key]; dup {
		r.add(`The key "` + it.key + `" is used by more than one item.`)
	} else {
		r.byKey[it.key] = it
	}
	r.items = append(r.items, it)
	return it
}

func isField(it *item) bool {
	return it.kind != "section" && it.kind != "group" && it.kind != "repeat"
}

func owner(it *item) string {
	switch it.kind {
	case "section":
		return `Section "` + it.key + `"`
	case "group":
		return `Group "` + it.key + `"`
	case "repeat":
		return `Repeating group "` + it.key + `"`
	}
	return `Field "` + it.key + `"`
}

var kindWords = map[string]string{
	"section": "a section", "group": "a group", "repeat": "a repeating group",
	"text": "a text field", "number": "a number field", "date": "a date field", "time": "a time field",
	"date-time": "a date and time field", "single-choice": "a single choice field",
	"multiple-choice": "a multiple choice field", "point": "a GPS point", "track": "a walked track",
	"polygon": "a drawn area", "photo": "a photo field", "signature": "a signature", "file": "a file field",
	"calculated": "a calculated field", "alert-value": "a value from the alert",
}

// alertValueTypes gives the kind of value each alert source provides.
var alertValueTypes = map[string]string{
	"alert-id": "text", "observed-at": "date-time", "raised-at": "date-time", "location": "point",
	"unit": "text", "forest-class": "text", "source": "text", "confidence": "text", "fire-power": "number",
}

var fieldValueTypes = map[string]string{
	"text": "text", "number": "number", "date": "date", "time": "time", "date-time": "date-time",
	"single-choice": "choice", "multiple-choice": "choices", "point": "point", "track": "line",
	"polygon": "polygon", "photo": "attachments", "signature": "attachment", "file": "attachments",
	"calculated": "number",
}

func valueType(it *item) string {
	if it.kind == "alert-value" {
		return alertValueTypes[it.node["source"].(string)]
	}
	return fieldValueTypes[it.kind]
}

func (r *rulesChecker) checkItem(it *item) {
	who := owner(it)
	n := it.node
	if it.kind == "section" {
		name, has := n["stage"].(string)
		if has {
			if _, found := r.stages[name]; !found {
				r.add(who + `: uses the stage "` + name + `", which does not exist.`)
			}
		} else if r.hasStages {
			r.add(who + `: needs a stage, because the form has stages.`)
		}
	}
	for _, pair := range [][2]string{{"minLength", "maxLength"}, {"min", "max"}, {"minSelected", "maxSelected"}, {"minCount", "maxCount"}, {"minRepeats", "maxRepeats"}} {
		lo, hasLo := n[pair[0]].(float64)
		hi, hasHi := n[pair[1]].(float64)
		if hasLo && hasHi && lo > hi {
			r.add(who + ": " + pair[0] + " is more than " + pair[1] + ".")
		}
	}
	if p, ok := n["pattern"].(string); ok {
		if bad := unsupportedPattern(p); bad != "" {
			r.add(who + `: pattern uses "` + bad + `", which not every device supports.`)
		} else if _, err := regexp.Compile("^(?:" + p + ")$"); err != nil {
			r.add(who + ": pattern is not a valid regular expression.")
		}
	}
	if it.kind == "single-choice" || it.kind == "multiple-choice" {
		r.checkChoices(it)
	}
	if _, req := n["required"]; req {
		if _, when := n["requireWhen"]; when {
			r.add(who + `: has both "required" and "requireWhen"; use one.`)
		}
	}
	if c, ok := n["showWhen"].(map[string]any); ok {
		r.checkCondition(it, "showWhen", c)
	}
	if c, ok := n["requireWhen"].(map[string]any); ok {
		r.checkCondition(it, "requireWhen", c)
	}
	if e, ok := n["calculate"].(map[string]any); ok {
		r.checkExpression(it, e)
	}
}

// unsupportedPattern returns the first part of a pattern that the Go and
// browser regular expression engines read differently, or "" if none.
func unsupportedPattern(p string) string {
	inClass := false
	for i := 0; i < len(p); i++ {
		c := p[i]
		next := byte(0)
		if i+1 < len(p) {
			next = p[i+1]
		}
		if c == '\\' {
			if next != 0 && strings.IndexByte("123456789kAzQECpPGZ", next) >= 0 {
				return `\` + string(next)
			}
			i++
			continue
		}
		if inClass {
			if c == ']' {
				inClass = false
			} else if c == '[' && next == ':' {
				return "[:"
			}
			continue
		}
		switch c {
		case '[':
			if next == ']' || (next == '^' && i+2 < len(p) && p[i+2] == ']') {
				return "[]"
			}
			inClass = true
		case '(':
			if next == '?' {
				return "(?"
			}
		case '{':
			m := countPattern.FindString(p[i:])
			if m == "" {
				return "{"
			}
			i += len(m) - 1
		case '}':
			return "}"
		case ']':
			return "]"
		}
	}
	return ""
}

var countPattern = regexp.MustCompile(`^\{[0-9]+(,[0-9]*)?\}`)

func choiceKeys(choices []any) map[string]bool {
	keys := map[string]bool{}
	for _, c := range objects(choices) {
		keys[c["key"].(string)] = true
	}
	return keys
}

// choicesOf returns a choice field's choices, or nil if its list is missing.
func (r *rulesChecker) choicesOf(it *item) []any {
	if c, ok := it.node["choices"].([]any); ok {
		return c
	}
	if name, ok := it.node["list"].(string); ok {
		return r.lists[name]
	}
	return nil
}

func (r *rulesChecker) checkChoices(it *item) {
	who := owner(it)
	_, hasChoices := it.node["choices"]
	listName, hasList := it.node["list"].(string)
	switch {
	case !hasChoices && !hasList:
		r.add(who + `: needs "choices" or "list".`)
		return
	case hasChoices && hasList:
		r.add(who + `: has both "choices" and "list"; use one.`)
		return
	case hasList && r.lists[listName] == nil:
		r.add(who + `: uses the list "` + listName + `", which does not exist.`)
		return
	}
	choices := r.choicesOf(it)
	if hasChoices {
		for _, c := range duplicateChoices(choices) {
			r.add(who + `: has the choice "` + c + `" more than once.`)
		}
	}
	parentKey, hasParent := it.node["parent"].(string)
	if !hasParent {
		for _, c := range objects(choices) {
			if _, ok := c["parent"]; ok {
				r.add(who + `: the choice "` + c["key"].(string) + `" has a parent, but the field has no "parent".`)
			}
		}
		return
	}
	parent := r.resolve(it, "parent", parentKey)
	if parent == nil {
		return
	}
	if parent.kind != "single-choice" {
		r.add(who + `: parent "` + parentKey + `" is ` + kindWords[parent.kind] + `, not a single choice field.`)
		return
	}
	it.deps = append(it.deps, parent)
	parentChoices := r.choicesOf(parent)
	if parentChoices == nil {
		return
	}
	allowed := choiceKeys(parentChoices)
	for _, c := range objects(choices) {
		key := c["key"].(string)
		p, ok := c["parent"].(string)
		if !ok {
			r.add(who + `: the choice "` + key + `" has no parent.`)
		} else if !allowed[p] {
			r.add(who + `: the choice "` + key + `" has the parent "` + p + `", which is not a choice of "` + parentKey + `".`)
		}
	}
}

func isInside(it, container *item) bool {
	for p := it.parent; p != nil; p = p.parent {
		if p == container {
			return true
		}
	}
	return false
}

// repeatOutside returns the first repeating group around a target that is
// not also around the owner, or "".
func repeatOutside(target, owner []string) string {
	for i, k := range target {
		if i >= len(owner) || owner[i] != k {
			return k
		}
	}
	return ""
}

func (r *rulesChecker) placeProblem(from *item, prop, key string, target *item) bool {
	who := owner(from)
	if isInside(target, from) {
		r.add(who + ": " + prop + ` refers to "` + key + `", which is inside it.`)
		return true
	}
	if rep := repeatOutside(target.repeats, from.repeats); rep != "" {
		r.add(who + ": " + prop + ` refers to "` + key + `", which is inside the repeating group "` + rep + `".`)
		return true
	}
	if from.stage >= 0 && target.stage > from.stage {
		r.add(who + ": " + prop + ` refers to "` + key + `", which is filled in at a later stage.`)
		return true
	}
	return false
}

// resolve finds the field that an item refers to, or reports why it cannot.
func (r *rulesChecker) resolve(from *item, prop, key string) *item {
	who := owner(from)
	target := r.byKey[key]
	if target == nil {
		r.add(who + ": " + prop + ` refers to "` + key + `", which does not exist.`)
		return nil
	}
	if !isField(target) {
		r.add(who + ": " + prop + ` refers to "` + key + `", which is ` + kindWords[target.kind] + `, not a field.`)
		return nil
	}
	if r.placeProblem(from, prop, key, target) {
		return nil
	}
	return target
}

var compareOps = map[string][]string{
	"equals":     {"text", "number", "date", "time", "date-time", "choice"},
	"not-equals": {"text", "number", "date", "time", "date-time", "choice"},
	"more-than":  {"number", "date", "time", "date-time"},
	"at-least":   {"number", "date", "time", "date-time"},
	"less-than":  {"number", "date", "time", "date-time"},
	"at-most":    {"number", "date", "time", "date-time"},
	"includes":   {"choices"},
}

var (
	datePattern     = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)
	timePattern     = regexp.MustCompile(`^[0-9]{2}:[0-9]{2}$`)
	dateTimePattern = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}(:[0-9]{2})?(Z|[+-][0-9]{2}:[0-9]{2})$`)
)

func (r *rulesChecker) checkCondition(from *item, prop string, c map[string]any) {
	op := c["op"].(string)
	switch op {
	case "all", "any":
		for _, sub := range objects(c["conditions"]) {
			r.checkCondition(from, prop, sub)
		}
		return
	case "not":
		r.checkCondition(from, prop, c["condition"].(map[string]any))
		return
	}
	key := c["field"].(string)
	target := r.resolve(from, prop, key)
	if target == nil {
		return
	}
	if prop == "showWhen" {
		from.deps = append(from.deps, target)
	}
	if op == "answered" {
		return
	}
	who := owner(from) + ": " + prop
	vt := valueType(target)
	if !slices.Contains(compareOps[op], vt) {
		r.add(who + ` cannot use "` + op + `" on "` + key + `", which is ` + kindWords[target.kind] + ".")
		return
	}
	value := c["value"]
	compares := who + ` compares "` + key + `" with `
	str, isStr := value.(string)
	switch vt {
	case "number":
		if _, ok := value.(float64); !ok {
			r.add(compares + "a value that is not a number.")
		}
	case "text":
		if !isStr {
			r.add(compares + "a value that is not text.")
		}
	case "date":
		if !isStr || !datePattern.MatchString(str) {
			r.add(compares + "a value that is not a date such as 2026-03-15.")
		}
	case "time":
		if !isStr || !timePattern.MatchString(str) {
			r.add(compares + "a value that is not a time such as 14:30.")
		}
	case "date-time":
		if !isStr || !dateTimePattern.MatchString(str) {
			r.add(compares + "a value that is not a date and time such as 2026-03-15T14:30+05:30.")
		}
	case "choice", "choices":
		if !isStr {
			r.add(compares + "a value that is not a choice key.")
		} else if choices := r.choicesOf(target); choices != nil && !choiceKeys(choices)[str] {
			r.add(compares + `"` + str + `", which is not one of its choices.`)
		}
	}
}

func (r *rulesChecker) checkExpression(from *item, e map[string]any) {
	who := owner(from) + ": calculate"
	switch op := e["op"].(string); op {
	case "add", "multiply", "subtract", "divide":
		for _, arg := range objects(e["args"]) {
			r.checkExpression(from, arg)
		}
	case "field", "area", "length":
		key := e["field"].(string)
		target := r.resolve(from, "calculate", key)
		if target == nil {
			return
		}
		from.deps = append(from.deps, target)
		switch {
		case op == "field" && valueType(target) != "number":
			r.add(who + ` uses "` + key + `", which is not a number.`)
		case op == "area" && target.kind != "polygon":
			r.add(who + ` takes the area of "` + key + `", which is not a drawn area.`)
		case op == "length" && target.kind != "track":
			r.add(who + ` takes the length of "` + key + `", which is not a walked track.`)
		}
	case "hours-between":
		for _, end := range []string{"start", "end"} {
			key := e[end].(string)
			target := r.resolve(from, "calculate", key)
			if target == nil {
				continue
			}
			from.deps = append(from.deps, target)
			if valueType(target) != "date-time" {
				r.add(who + ` uses "` + key + `" in hours-between, which is not a date and time.`)
			}
		}
	case "sum", "count":
		group := r.resolveGroup(from, e["group"].(string))
		if group == nil || op == "count" {
			return
		}
		key := e["field"].(string)
		target := r.byKey[key]
		switch {
		case target == nil:
			r.add(who + ` refers to "` + key + `", which does not exist.`)
		case !isField(target):
			r.add(who + ` refers to "` + key + `", which is ` + kindWords[target.kind] + `, not a field.`)
		case !slices.Equal(target.repeats, append(slices.Clone(group.repeats), group.key)):
			r.add(who + ` sums "` + key + `", which is not directly inside the repeating group "` + group.key + `".`)
		case from.stage >= 0 && target.stage > from.stage:
			r.add(who + ` refers to "` + key + `", which is filled in at a later stage.`)
		default:
			from.deps = append(from.deps, target)
			if valueType(target) != "number" {
				r.add(who + ` sums "` + key + `", which is not a number.`)
			}
		}
	}
}

func (r *rulesChecker) resolveGroup(from *item, key string) *item {
	who := owner(from) + ": calculate"
	group := r.byKey[key]
	switch {
	case group == nil:
		r.add(who + ` refers to "` + key + `", which does not exist.`)
	case group.kind != "repeat":
		r.add(who + ` needs a repeating group, but "` + key + `" is ` + kindWords[group.kind] + ".")
	case slices.Contains(from.repeats, key):
		r.add(who + ` refers to "` + key + `", which contains it.`)
	case !r.placeProblem(from, "calculate", key, group):
		return group
	}
	return nil
}

// Column is one value that a form version produces. The flat database
// view for a published version has one column per entry, and a repeating
// group becomes its own table of rows.
type Column struct {
	Name      string `json:"name"`
	Field     string `json:"field"`
	ValueType string `json:"valueType"`
	Unit      string `json:"unit,omitempty"`
	Repeat    string `json:"repeat,omitempty"`
}

func columnsOf(items []*item) []Column {
	var cols []Column
	for _, it := range items {
		if !isField(it) {
			continue
		}
		repeat := ""
		if len(it.repeats) > 0 {
			repeat = it.repeats[len(it.repeats)-1]
		}
		unit, _ := it.node["unit"].(string)
		if it.kind == "alert-value" && it.node["source"] == "fire-power" {
			unit = "MW"
		}
		cols = append(cols, Column{Name: it.key, Field: it.key, ValueType: valueType(it), Unit: unit, Repeat: repeat})
		switch it.kind {
		case "polygon":
			cols = append(cols, Column{Name: it.key + "_area_ha", Field: it.key, ValueType: "number", Unit: "ha", Repeat: repeat})
		case "track":
			cols = append(cols, Column{Name: it.key + "_length_km", Field: it.key, ValueType: "number", Unit: "km", Repeat: repeat})
		}
	}
	return cols
}

func (r *rulesChecker) checkColumnNames() {
	taken := map[string]bool{}
	for key := range r.byKey {
		taken[key] = true
	}
	for _, col := range columnsOf(r.items) {
		if col.Name == col.Field {
			continue
		}
		if taken[col.Name] {
			r.add(`Field "` + col.Field + `": the column "` + col.Name + `" it adds clashes with another key.`)
		}
		taken[col.Name] = true
	}
}

func (r *rulesChecker) checkCycles() {
	const (
		unseen = iota
		open
		done
	)
	state := map[*item]int{}
	var stack []*item
	var visit func(*item)
	visit = func(it *item) {
		state[it] = open
		stack = append(stack, it)
		for _, dep := range it.deps {
			switch state[dep] {
			case open:
				start := slices.Index(stack, dep)
				names := make([]string, 0, len(stack)-start+1)
				for _, s := range stack[start:] {
					names = append(names, s.key)
				}
				names = append(names, dep.key)
				r.add(owner(dep) + " depends on itself: " + strings.Join(names, " → ") + ".")
			case unseen:
				visit(dep)
			}
		}
		stack = stack[:len(stack)-1]
		state[it] = done
	}
	for _, it := range r.items {
		if state[it] == unseen {
			visit(it)
		}
	}
}

func (r *rulesChecker) checkStagesUsed() {
	used := map[int]bool{}
	for _, it := range r.items {
		if it.kind == "section" && it.stage >= 0 {
			used[it.stage] = true
		}
	}
	for i, s := range objects(r.form["stages"]) {
		if r.stages[s["key"].(string)] == i && !used[i] {
			r.add(`Stage "` + s["key"].(string) + `" is not used by any section.`)
		}
	}
}

var widgetKinds = map[string][]string{
	"line": {"text"}, "paragraph": {"text"}, "input": {"number"}, "stepper": {"number"},
	"radio": {"single-choice"}, "dropdown": {"single-choice"}, "buttons": {"single-choice", "multiple-choice"},
	"checkboxes": {"multiple-choice"},
}

func (r *rulesChecker) checkLayout() {
	layout, ok := r.form["layout"].(map[string]any)
	if !ok {
		return
	}
	if pages, ok := layout["pages"]; ok {
		pageKeys := map[string]bool{}
		onPage := map[string]bool{}
		for _, p := range objects(pages) {
			page := p["key"].(string)
			if pageKeys[page] {
				r.add(`Layout: the page "` + page + `" is defined more than once.`)
			}
			pageKeys[page] = true
			stagesSeen := map[int]bool{}
			for _, s := range stringList(p["sections"]) {
				sec := r.byKey[s]
				if sec == nil || sec.kind != "section" {
					r.add(`Layout: the page "` + page + `" lists "` + s + `", which is not a section.`)
					continue
				}
				if onPage[s] {
					r.add(`Layout: the section "` + s + `" is on more than one page.`)
					continue
				}
				onPage[s] = true
				stagesSeen[sec.stage] = true
			}
			if r.hasStages && len(stagesSeen) > 1 {
				r.add(`Layout: the page "` + page + `" mixes sections from different stages.`)
			}
		}
		for _, it := range r.items {
			if it.kind == "section" && r.byKey[it.key] == it && !onPage[it.key] {
				r.add(`Layout: the section "` + it.key + `" is not on any page.`)
			}
		}
	}
	listed := map[string]bool{}
	for _, li := range objects(layout["items"]) {
		key := li["item"].(string)
		it := r.byKey[key]
		if it == nil {
			r.add(`Layout: "` + key + `" is not an item in the form.`)
			continue
		}
		if listed[key] {
			r.add(`Layout: "` + key + `" is listed more than once.`)
			continue
		}
		listed[key] = true
		if w, ok := li["widget"].(string); ok && !slices.Contains(widgetKinds[w], it.kind) {
			r.add(`Layout: the widget "` + w + `" does not suit "` + key + `", which is ` + kindWords[it.kind] + ".")
		}
		if _, ok := li["collapsed"]; ok && isField(it) {
			r.add(`Layout: "collapsed" only works on sections and groups, not on "` + key + `".`)
		}
	}
}

var textProperties = []string{"title", "label", "description", "itemLabel", "hint", "patternHint"}

func (r *rulesChecker) checkTranslations() {
	langs, ok := r.form["languages"]
	if !ok {
		return
	}
	want := stringList(langs)
	check := func(path string, v any) {
		text, ok := v.(map[string]any)
		if !ok {
			return
		}
		for _, l := range want {
			if _, has := text[l]; !has {
				r.add(path + ` is missing the "` + l + `" translation.`)
			}
		}
		names := make([]string, 0, len(text))
		for l := range text {
			names = append(names, l)
		}
		slices.Sort(names)
		for _, l := range names {
			if l != "en" && !slices.Contains(want, l) {
				r.add(path + ` has "` + l + `", which is not one of the form's languages.`)
			}
		}
	}
	checkNode := func(path string, n map[string]any) {
		for _, p := range textProperties {
			check(childPath(path, p), n[p])
		}
		for i, c := range objects(n["choices"]) {
			check(path+".choices["+strconv.Itoa(i)+"].label", c["label"])
		}
	}
	checkNode("", r.form)
	for i, s := range objects(r.form["stages"]) {
		check("stages["+strconv.Itoa(i)+"].title", s["title"])
	}
	for i, l := range objects(r.form["lists"]) {
		checkNode("lists["+strconv.Itoa(i)+"]", l)
	}
	for _, it := range r.items {
		checkNode(it.path, it.node)
	}
	if layout, ok := r.form["layout"].(map[string]any); ok {
		for i, p := range objects(layout["pages"]) {
			check("layout.pages["+strconv.Itoa(i)+"].title", p["title"])
		}
	}
}
