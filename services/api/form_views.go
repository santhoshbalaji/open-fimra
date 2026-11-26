package main

import (
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	formschema "github.com/santhoshbalaji/open-fimra/packages/form-schema"
)

// Each published form version gets flat views in the form_views schema
// (form format, section 13): one row per submission, and one row per entry
// of each repeating group. Their columns are the form's columns, so fields
// can be charted and exported like ordinary columns.
//
// Column names come from field keys. The validator only accepts keys of
// small letters, digits and underscores that start with a letter, and this
// file checks that again before it writes any SQL. Names are quoted as
// identifiers and keys as literals as well, so a key can never become SQL.

// formViewKey matches a field or repeating group key, and the longest
// column name a key can give, with _length_km added.
var (
	formViewKey    = regexp.MustCompile(`^[a-z][a-z0-9_]{0,39}$`)
	formViewColumn = regexp.MustCompile(`^[a-z][a-z0-9_]{0,49}$`)
)

// formView is one flat view of a published version.
type formView struct {
	RepeatKey string // "" for the submission view
	Name      string // the view name inside the form_views schema
	SQL       string
}

// submissionViewColumns are the columns every submission view starts with.
// They begin with an underscore, which no field key can, so they never clash.
const submissionViewColumns = `s.id AS _submission_id, s.alert_id AS _alert_id, s.incident_id AS _incident_id,
    s.workspace_id AS _workspace_id, s.user_id AS _submitted_by, s.step AS _step, s.device_time AS _device_time,
    s.received_at AS _received_at, s.location AS _location`

func sqlLiteral(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }

// formViews returns the CREATE VIEW statements for a published version.
func formViews(versionID int64, tree *formTree, columns []formschema.Column) ([]formView, error) {
	id := strconv.FormatInt(versionID, 10)
	byRepeat := map[string][]formschema.Column{}
	var repeats []string
	for _, col := range columns {
		if !formViewColumn.MatchString(col.Name) || !formViewKey.MatchString(col.Field) || (col.Repeat != "" && !formViewKey.MatchString(col.Repeat)) {
			return nil, fmt.Errorf("form version %d has an unsafe column name %q", versionID, col.Name)
		}
		if col.Repeat != "" && !slices.Contains(repeats, col.Repeat) {
			repeats = append(repeats, col.Repeat)
		}
		byRepeat[col.Repeat] = append(byRepeat[col.Repeat], col)
	}
	// A repeating group that holds only other repeating groups has no
	// columns of its own but still links its inner entries.
	for key, n := range tree.byKey {
		if n.kind == "repeat" && !slices.Contains(repeats, key) {
			if !formViewKey.MatchString(key) {
				return nil, fmt.Errorf("form version %d has an unsafe repeating group key %q", versionID, key)
			}
			repeats = append(repeats, key)
		}
	}
	slices.Sort(repeats)

	main := "f" + id
	views := []formView{{Name: main, SQL: "CREATE VIEW form_views." + pgx.Identifier{main}.Sanitize() + " AS\nSELECT " +
		submissionViewColumns + columnList(byRepeat[""], "s.answers") +
		"\nFROM submissions s WHERE s.alert_id IS NOT NULL AND s.form_version_id = " + id}}

	for _, key := range repeats {
		n := tree.byKey[key]
		if n == nil || n.kind != "repeat" {
			return nil, fmt.Errorf("form version %d names an unknown repeating group %q", versionID, key)
		}
		var chain []*formNode
		for r := n; r != nil; r = r.repeat {
			chain = append([]*formNode{r}, chain...)
		}
		var from strings.Builder
		from.WriteString("\nFROM submissions s")
		source := "s.answers"
		var ordinals []string
		for i, r := range chain {
			alias := "e" + strconv.Itoa(i+1)
			list := source + "->" + sqlLiteral(r.key)
			fmt.Fprintf(&from, "\nCROSS JOIN LATERAL jsonb_array_elements(CASE WHEN jsonb_typeof(%s) = 'array' THEN %s ELSE '[]' END) WITH ORDINALITY %s(v, n)", list, list, alias)
			source = alias + ".v"
			ordinals = append(ordinals, alias+".n")
		}
		parent := "NULL::text"
		if len(ordinals) > 1 {
			parent = "concat_ws('.', " + strings.Join(ordinals[:len(ordinals)-1], ", ") + ")"
		}
		name := main + "_" + key
		views = append(views, formView{RepeatKey: key, Name: name, SQL: "CREATE VIEW form_views." + pgx.Identifier{name}.Sanitize() + " AS\nSELECT " +
			"s.id AS _submission_id, s.alert_id AS _alert_id, s.workspace_id AS _workspace_id, s.received_at AS _received_at,\n    " +
			"concat_ws('.', " + strings.Join(ordinals, ", ") + ") AS _entry, " + parent + " AS _parent_entry" +
			columnList(byRepeat[key], source) + from.String() + "\nWHERE s.alert_id IS NOT NULL AND s.form_version_id = " + id})
	}
	return views, nil
}

func columnList(columns []formschema.Column, source string) string {
	var b strings.Builder
	for _, col := range columns {
		b.WriteString(",\n    ")
		b.WriteString(columnExpression(col, source))
		b.WriteString(" AS ")
		b.WriteString(pgx.Identifier{col.Name}.Sanitize())
	}
	return b.String()
}

func columnExpression(col formschema.Column, source string) string {
	text := source + "->>" + sqlLiteral(col.Field)
	value := source + "->" + sqlLiteral(col.Field)
	shape := "ST_Force2D(ST_SetSRID(ST_GeomFromGeoJSON(" + value + "), 4326))"
	array := func(element string) string {
		return "CASE WHEN jsonb_typeof(" + value + ") = 'array' THEN ARRAY(SELECT " + element + " FROM jsonb_array_elements_text(" + value + ") x) END"
	}
	switch {
	case col.Name == col.Field+"_area_ha":
		return "ST_Area(" + shape + "::geography) / 10000"
	case col.Name == col.Field+"_length_km":
		return "ST_Length(" + shape + "::geography) / 1000"
	}
	switch col.ValueType {
	case "number":
		return "(" + text + ")::numeric"
	case "date":
		return "(" + text + ")::date"
	case "time":
		return "(" + text + ")::time"
	case "date-time":
		return "(" + text + ")::timestamptz"
	case "choices":
		return array("x")
	case "attachment":
		return "(" + text + ")::uuid"
	case "attachments":
		return array("x::uuid")
	case "point":
		return shape + "::geometry(Point, 4326)"
	case "line":
		return shape + "::geometry(LineString, 4326)"
	case "polygon":
		return shape + "::geometry(Polygon, 4326)"
	}
	return text // text and choice
}
