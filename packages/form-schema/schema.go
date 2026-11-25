package formschema

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// This file applies form.schema.json. It understands only the JSON Schema
// keywords that the schema uses, so that the Go and TypeScript validators
// give the same messages in the same order. Loading the schema fails on
// any other keyword, which stops the schema from quietly outgrowing the
// validators.

var schemaKeywords = map[string]bool{
	"$schema": true, "$id": true, "$defs": true, "$ref": true, "title": true, "description": true,
	"type": true, "enum": true, "const": true, "pattern": true, "x-patternMessage": true,
	"minLength": true, "maxLength": true, "minimum": true, "maximum": true,
	"required": true, "properties": true, "additionalProperties": true, "propertyNames": true,
	"items": true, "minItems": true, "maxItems": true, "allOf": true, "if": true, "then": true, "else": true,
}

var typeWords = map[string]string{
	"string": "text", "number": "a number", "integer": "a whole number", "boolean": "true or false",
	"object": "an object", "array": "a list", "null": "empty",
}

type schemaChecker struct {
	root     map[string]any
	patterns map[string]*regexp.Regexp
}

var schemaRules = loadSchema()

func loadSchema() *schemaChecker {
	var root map[string]any
	if err := json.Unmarshal(Schema, &root); err != nil {
		panic("formschema: form.schema.json is not valid JSON: " + err.Error())
	}
	c := &schemaChecker{root: root, patterns: map[string]*regexp.Regexp{}}
	c.prepare(root, "#")
	for name, def := range root["$defs"].(map[string]any) {
		c.prepare(def.(map[string]any), "#/$defs/"+name)
	}
	return c
}

// prepare checks the keywords of one schema and its subschemas and compiles
// its patterns.
func (c *schemaChecker) prepare(s map[string]any, at string) {
	for k := range s {
		if !schemaKeywords[k] {
			panic(fmt.Sprintf("formschema: %s uses the keyword %q, which the validators do not support", at, k))
		}
	}
	if ref, ok := s["$ref"].(string); ok {
		c.resolve(ref)
	}
	if p, ok := s["pattern"].(string); ok {
		c.patterns[p] = regexp.MustCompile(p)
	}
	if e, ok := s["enum"].([]any); ok {
		for _, v := range e {
			if _, ok := v.(string); !ok {
				panic("formschema: " + at + " has an enum value that is not text")
			}
		}
	}
	sub := func(name string, v any) {
		if m, ok := v.(map[string]any); ok {
			c.prepare(m, at+"/"+name)
		}
	}
	if props, ok := s["properties"].(map[string]any); ok {
		for name, p := range props {
			sub("properties/"+name, p)
		}
	}
	for _, name := range []string{"additionalProperties", "propertyNames", "items", "if", "then", "else"} {
		sub(name, s[name])
	}
	if all, ok := s["allOf"].([]any); ok {
		for i, p := range all {
			sub("allOf/"+strconv.Itoa(i), p)
		}
	}
}

func (c *schemaChecker) resolve(ref string) map[string]any {
	name, ok := strings.CutPrefix(ref, "#/$defs/")
	if !ok {
		panic("formschema: unsupported reference " + ref)
	}
	def, ok := c.root["$defs"].(map[string]any)[name].(map[string]any)
	if !ok {
		panic("formschema: unknown reference " + ref)
	}
	return def
}

func checkSchema(form any) []string {
	var out []string
	schemaRules.check(schemaRules.root, form, "", &out)
	return out
}

// subject turns a location into the start of a sentence.
func subject(at string) string {
	if at == "" {
		return "The form"
	}
	return at
}

func childPath(at, name string) string {
	if at == "" {
		return name
	}
	return at + "." + name
}

func (c *schemaChecker) check(s map[string]any, v any, at string, out *[]string) {
	add := func(rest string) { *out = append(*out, subject(at)+" "+rest+".") }
	if ref, ok := s["$ref"].(string); ok {
		c.check(c.resolve(ref), v, at, out)
	}
	if t, ok := s["type"].(string); ok && !hasType(v, t) {
		add("must be " + typeWords[t])
		return
	}
	if e, ok := s["enum"].([]any); ok {
		str, isStr := v.(string)
		if !isStr || !slices.ContainsFunc(e, func(x any) bool { return x.(string) == str }) {
			words := make([]string, len(e))
			for i, x := range e {
				words[i] = x.(string)
			}
			add("must be one of: " + strings.Join(words, ", "))
		}
	}
	if want, ok := s["const"]; ok && !sameValue(v, want) {
		add("must be " + describeValue(want))
	}
	switch x := v.(type) {
	case string:
		if p, ok := s["pattern"].(string); ok && !c.patterns[p].MatchString(x) {
			if m, ok := s["x-patternMessage"].(string); ok {
				add(m)
			} else {
				add("does not match the pattern " + p)
			}
		}
		n := utf8.RuneCountInString(x)
		if min, ok := s["minLength"].(float64); ok && float64(n) < min {
			if min == 1 {
				add("must not be empty")
			} else {
				add("must have at least " + plural(min, "character"))
			}
		}
		if max, ok := s["maxLength"].(float64); ok && float64(n) > max {
			add("must have at most " + plural(max, "character"))
		}
	case float64:
		if min, ok := s["minimum"].(float64); ok && x < min {
			add("must be at least " + formatNumber(min))
		}
		if max, ok := s["maximum"].(float64); ok && x > max {
			add("must be at most " + formatNumber(max))
		}
	case map[string]any:
		if req, ok := s["required"].([]any); ok {
			for _, name := range req {
				if _, has := x[name.(string)]; !has {
					add(`is missing "` + name.(string) + `"`)
				}
			}
		}
		props, _ := s["properties"].(map[string]any)
		names := make([]string, 0, len(x))
		for name := range x {
			names = append(names, name)
		}
		slices.Sort(names)
		for _, name := range names {
			if pn, ok := s["propertyNames"].(map[string]any); ok {
				where := "the form"
				if at != "" {
					where = at
				}
				c.check(pn, name, `The name "`+name+`" in `+where, out)
			}
			if p, ok := props[name].(map[string]any); ok {
				c.check(p, x[name], childPath(at, name), out)
				continue
			}
			switch extra := s["additionalProperties"].(type) {
			case bool:
				if !extra {
					add(`has "` + name + `", which is not allowed here`)
				}
			case map[string]any:
				c.check(extra, x[name], childPath(at, name), out)
			}
		}
	case []any:
		if min, ok := s["minItems"].(float64); ok && float64(len(x)) < min {
			if min == 1 {
				add("must not be empty")
			} else {
				add("must have at least " + plural(min, "item"))
			}
		}
		if max, ok := s["maxItems"].(float64); ok && float64(len(x)) > max {
			add("must have at most " + plural(max, "item"))
		}
		if items, ok := s["items"].(map[string]any); ok {
			for i, item := range x {
				c.check(items, item, at+"["+strconv.Itoa(i)+"]", out)
			}
		}
	}
	if all, ok := s["allOf"].([]any); ok {
		for _, sub := range all {
			c.check(sub.(map[string]any), v, at, out)
		}
	}
	if cond, ok := s["if"].(map[string]any); ok {
		var trial []string
		c.check(cond, v, at, &trial)
		branch := "then"
		if len(trial) > 0 {
			branch = "else"
		}
		if next, ok := s[branch].(map[string]any); ok {
			c.check(next, v, at, out)
		}
	}
}

func hasType(v any, t string) bool {
	switch t {
	case "string":
		_, ok := v.(string)
		return ok
	case "number":
		_, ok := v.(float64)
		return ok
	case "integer":
		f, ok := v.(float64)
		return ok && f == math.Trunc(f)
	case "boolean":
		_, ok := v.(bool)
		return ok
	case "object":
		_, ok := v.(map[string]any)
		return ok
	case "array":
		_, ok := v.([]any)
		return ok
	case "null":
		return v == nil
	}
	panic("formschema: unknown type " + t)
}

// sameValue compares a value with a const, which is always text or a number.
func sameValue(v, want any) bool {
	switch w := want.(type) {
	case string:
		s, ok := v.(string)
		return ok && s == w
	case float64:
		f, ok := v.(float64)
		return ok && f == w
	}
	panic("formschema: const must be text or a number")
}

func describeValue(v any) string {
	if s, ok := v.(string); ok {
		return `"` + s + `"`
	}
	return formatNumber(v.(float64))
}

func formatNumber(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func plural(n float64, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return formatNumber(n) + " " + word + "s"
}
