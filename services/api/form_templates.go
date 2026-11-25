package main

import (
	"bytes"
	"encoding/json"
	"io"
	"reflect"
	"slices"
)

// National, state and division forms extend each other (PLAN.md section
// 5.4). A version's template rules say whether forms one level down may
// extend it, and which of its parts they must keep as they are. The web
// designer runs the same checks in apps/web/src/forms/form-locks.ts, with the same
// messages, so people see a problem before they save.

// maxLockedKeys is the most keys a template can lock.
const maxLockedKeys = 500

// templateRules is stored with each version and frozen with it.
type templateRules struct {
	AllowExtension bool     `json:"allowExtension"`
	Locked         []string `json:"locked"`
}

// formLevels lists the levels from the top. A form extends a form one level up.
var formLevels = []string{"national", "state", "division"}

func parentLevel(level string) string {
	i := slices.Index(formLevels, level)
	if i <= 0 {
		return ""
	}
	return formLevels[i-1]
}

// parseTemplateRules reads a template's rules for a form that has passed the
// validator. Missing rules allow no extension and lock nothing.
func parseTemplateRules(raw json.RawMessage, tree *formTree) (templateRules, []string) {
	rules := templateRules{Locked: []string{}}
	if len(bytes.TrimSpace(raw)) == 0 || string(bytes.TrimSpace(raw)) == "null" {
		return rules, nil
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&rules) != nil || d.Decode(new(any)) != io.EOF {
		return rules, []string{`The template rules need "allowExtension" (true or false) and "locked" (a list of keys), and nothing else.`}
	}
	if rules.Locked == nil {
		rules.Locked = []string{}
	}
	if len(rules.Locked) > maxLockedKeys {
		return rules, []string{"A template can lock at most 500 keys."}
	}
	var problems []string
	seen := map[string]bool{}
	for _, key := range rules.Locked {
		switch {
		case seen[key]:
			problems = append(problems, `The template locks "`+key+`" more than once.`)
		case tree.byKey[key] == nil:
			problems = append(problems, `The template locks "`+key+`", which is not a section or item of this form.`)
		}
		seen[key] = true
	}
	return rules, problems
}

// mergeLocks adds the locks a form inherits from the template it extends,
// so a division form keeps the national parts of its state form too.
func mergeLocks(own, inherited templateRules) templateRules {
	locked := slices.Clone(inherited.Locked)
	for _, key := range own.Locked {
		if !slices.Contains(locked, key) {
			locked = append(locked, key)
		}
	}
	return templateRules{AllowExtension: own.AllowExtension, Locked: locked}
}

// describeNode names a section or item the way the validator does.
func describeNode(n *formNode) string {
	switch n.kind {
	case "section":
		return `Section "` + n.key + `"`
	case "group":
		return `Group "` + n.key + `"`
	case "repeat":
		return `Repeating group "` + n.key + `"`
	}
	return `Field "` + n.key + `"`
}

func containerWords(n *formNode) string {
	if n == nil {
		return "the top level of the form"
	}
	switch n.kind {
	case "section":
		return `section "` + n.key + `"`
	case "group":
		return `group "` + n.key + `"`
	}
	return `repeating group "` + n.key + `"`
}

func containerKey(n *formNode) string {
	if n.parent == nil {
		return ""
	}
	return n.parent.key
}

// formStages reads the stages of a form that has passed the validator.
func formStages(definition []byte) map[string]any {
	var form struct {
		Stages []map[string]any `json:"stages"`
	}
	_ = json.Unmarshal(definition, &form)
	stages := map[string]any{}
	for _, s := range form.Stages {
		key, _ := s["key"].(string)
		stages[key] = s
	}
	return stages
}

// checkExtension compares a form with the template version it extends.
// Every locked section or item must be in the new form, unchanged and in
// the same place. Lists and stages that locked parts use must be unchanged
// too. Everything else may change, and new items may be added outside
// locked parts.
func checkExtension(parentDefinition []byte, rules templateRules, childDefinition []byte) ([]string, error) {
	parent, err := parseFormTree(parentDefinition)
	if err != nil {
		return nil, err
	}
	child, err := parseFormTree(childDefinition)
	if err != nil {
		return nil, err
	}
	parentStages, childStages := formStages(parentDefinition), formStages(childDefinition)
	var problems []string
	var lists, stages []string
	for _, key := range rules.Locked {
		p := parent.byKey[key]
		if p == nil {
			continue
		}
		c := child.byKey[key]
		who := describeNode(p)
		switch {
		case c == nil:
			problems = append(problems, who+" is locked by the template it extends and cannot be removed.")
			continue
		case c.kind != p.kind || !reflect.DeepEqual(c.node, p.node):
			problems = append(problems, who+" is locked by the template it extends and cannot change.")
		case containerKey(c) != containerKey(p):
			problems = append(problems, who+" is locked by the template it extends and must stay in "+containerWords(p.parent)+".")
		}
		// The lists and stage the locked part uses.
		section := p
		for section.parent != nil {
			section = section.parent
		}
		if name, ok := section.node["stage"].(string); ok && !slices.Contains(stages, name) {
			stages = append(stages, name)
		}
		var walk func(n map[string]any)
		walk = func(n map[string]any) {
			if name, ok := n["list"].(string); ok && !slices.Contains(lists, name) {
				lists = append(lists, name)
			}
			items, _ := n["items"].([]any)
			for _, item := range items {
				if m, ok := item.(map[string]any); ok {
					walk(m)
				}
			}
		}
		walk(p.node)
	}
	for _, name := range lists {
		if !reflect.DeepEqual(parent.lists[name], child.lists[name]) {
			problems = append(problems, `The list "`+name+`" is used by a locked part of the template and cannot change.`)
		}
	}
	for _, name := range stages {
		if !reflect.DeepEqual(parentStages[name], childStages[name]) {
			problems = append(problems, `The stage "`+name+`" is used by a locked part of the template and cannot change.`)
		}
	}
	return problems, nil
}
