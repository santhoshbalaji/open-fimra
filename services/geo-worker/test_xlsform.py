"""XLSForm import and export. Every workbook here is written by the test, and all content is made up."""

import copy
import io
import json
import os
import unittest
from pathlib import Path

import openpyxl

import formcheck
from xlsform import XLSFormError, export_xlsform, import_xlsform, report_text
from xlsform_expr import Ref, Resolver, Untranslatable, to_calculation, to_condition, to_constraint

EXAMPLES = Path(os.environ.get("FIMRA_FORM_EXAMPLES", "/form-examples"))
STARTER_PACK = ["control-room-dispatch", "first-report", "final-report", "patrol-log", "manual-fire-report"]


def equivalent(form):
    """What a round trip must keep: the form, apart from three things that do not change its meaning.

    - "$schema" is only a hint for editors.
    - Layout display settings are one per item, so their order means nothing.
    - A shared list used by one question is the same as that question holding
      the choices itself.
    """
    form = copy.deepcopy(form)
    form.pop("$schema", None)
    items = form.get("layout", {}).get("items")
    if items is not None:
        items.sort(key=lambda i: i["item"])
    users = {}

    def visit(items):
        for item in items:
            if "items" in item:
                visit(item["items"])
            elif "list" in item:
                users.setdefault(item["list"], []).append(item)
    for section in form["sections"]:
        visit(section["items"])
    lists = {lst["key"]: lst for lst in form.get("lists", [])}
    for key, fields in users.items():
        if len(fields) == 1:
            fields[0]["choices"] = lists.pop(key)["choices"]
            del fields[0]["list"]
    if "lists" in form:
        form["lists"] = list(lists.values())
        if not form["lists"]:
            del form["lists"]
    return form


def workbook(sheets):
    """An .xlsx file with the given sheets, each a list of rows (the first row is the header)."""
    book = openpyxl.Workbook()
    book.remove(book.active)
    for name, rows in sheets.items():
        ws = book.create_sheet(name)
        for row in rows:
            ws.append(row)
    out = io.BytesIO()
    book.save(out)
    return out.getvalue()


def read_back(data):
    """The sheets of a workbook as lists of row dictionaries."""
    book = openpyxl.load_workbook(io.BytesIO(data))
    sheets = {}
    for ws in book.worksheets:
        rows = list(ws.iter_rows(values_only=True))
        sheets[ws.title] = [dict(zip(rows[0], r)) for r in rows[1:]]
    return sheets


class RoundTripTests(unittest.TestCase):
    def test_every_example_survives_a_round_trip(self):
        files = sorted(EXAMPLES.glob("*.json"))
        names = {f.stem for f in files}
        self.assertTrue(set(STARTER_PACK) <= names, f"starter-pack examples missing from {EXAMPLES}")
        for file in files:
            with self.subTest(file.name):
                form = json.loads(file.read_text())
                data, _ = export_xlsform(form)
                result = import_xlsform(data, file.name)
                self.assertEqual(result.problems, [])
                self.assertEqual(result.report, [], report_text(result.report))
                self.assertEqual(equivalent(result.form), equivalent(form))
                # Importing the same workbook twice gives the same draft.
                self.assertEqual(import_xlsform(data, file.name).form, result.form)

    def test_export_uses_standard_xlsform_where_it_can(self):
        form = json.loads((EXAMPLES / "first-report.json").read_text())
        data, notes = export_xlsform(form)
        sheets = read_back(data)
        survey = {r["name"]: r for r in sheets["survey"] if r["name"]}
        self.assertEqual(sheets["settings"][0]["form_id"], "first-report")
        self.assertEqual(survey["cause"]["type"], "select_one causes")
        self.assertEqual(survey["cause"]["choice_filter"], "cause_group = ${cause_group}")
        self.assertEqual(survey["teams"]["type"], "begin_repeat")
        self.assertEqual(survey["total_staff"]["calculation"], "round(sum(${members}), 0)")
        self.assertEqual(survey["estimated_area"]["constraint"], ". >= 0 and . <= 10000")
        self.assertEqual(survey["reached_at"]["constraint"], ". <= now()")
        self.assertEqual(survey["vegetation"]["constraint"], "count-selected(.) >= 1")
        self.assertEqual(survey["reporter_signature"]["appearance"], "signature")
        self.assertEqual(survey["cause"]["appearance"], "minimal")
        self.assertEqual(survey["fire_status"]["fimra_widget"], "radio")
        self.assertEqual(survey["observed_at"]["read_only"], "yes")
        self.assertEqual(survey["observed_at"]["fimra_alert_value"], "observed-at")
        self.assertEqual(survey["cause_notes"]["relevant"],
                         "${cause} = 'human-other' or ${cause} = 'natural-other'")
        self.assertEqual(survey["hours_to_reach"]["calculation"],
                         "round(((decimal-date-time(${reached_at}) - decimal-date-time(${observed_at})) * 24), 1)")
        causes = [r for r in sheets["choices"] if r["list_name"] == "causes"]
        self.assertEqual(causes[0]["cause_group"], "human")
        self.assertEqual(sheets["open_fimra"][0]["type"], "title")
        self.assertIn("ODK cannot fill in values from the alert", report_text(notes))

    def test_translations_use_language_columns(self):
        form = json.loads((EXAMPLES / "manual-fire-report.json").read_text())
        sheets = read_back(export_xlsform(form)[0])
        row = next(r for r in sheets["survey"] if r["name"] == "reporter_phone")
        self.assertEqual(row["label::English (en)"], "Phone number")
        self.assertEqual(row["label::Hindi (hi)"], "फ़ोन नंबर")
        self.assertEqual(row["constraint"], "regex(., '^[6-9][0-9]{9}$')")
        self.assertEqual(row["constraint_message::Hindi (hi)"], "दस अंक, जो 6, 7, 8 या 9 से शुरू हों")
        self.assertEqual(row["required"], "${report_source} = 'public-call'")
        self.assertEqual(sheets["settings"][0]["default_language"], "English (en)")
        self.assertEqual([r["type"] for r in sheets["open_fimra"]], ["title", "description", "languages", "page"])

    def test_stages_and_pages_come_back(self):
        form = json.loads((EXAMPLES / "control-room-dispatch.json").read_text())
        sheets = read_back(export_xlsform(form)[0])
        stages = [r for r in sheets["open_fimra"] if r["type"] == "stage"]
        self.assertEqual([(r["name"], r["workflow_step"]) for r in stages],
                         [("dispatch", "team_sent"), ("arrival", "reached_site")])
        section = next(r for r in sheets["survey"] if r["name"] == "arrival")
        self.assertEqual(section["fimra_stage"], "arrival")

    def test_a_shared_list_used_once_comes_back_as_choices(self):
        form = {"formatVersion": 1, "id": "demo-list", "version": 1, "title": {"en": "Demo list"},
                "lists": [{"key": "weather", "choices": [{"key": "dry", "label": {"en": "Dry"}}]}],
                "sections": [{"key": "s", "title": {"en": "Weather"}, "items": [
                    {"key": "weather", "type": "single-choice", "label": {"en": "Weather"}, "list": "weather"}]}]}
        result = import_xlsform(export_xlsform(form)[0])
        self.assertEqual(result.problems, [])
        self.assertNotIn("lists", result.form)
        self.assertEqual(equivalent(result.form), equivalent(form))

    def test_invalid_forms_are_not_exported(self):
        with self.assertRaisesRegex(XLSFormError, "not valid"):
            export_xlsform({"formatVersion": 1, "id": "Bad", "version": 1, "title": {"en": "x"}, "sections": []})


SURVEY_HEADER = ["type", "name", "label::English (en)", "label::Hindi (hi)", "hint", "required", "relevant",
                 "constraint", "constraint_message", "calculation", "choice_filter", "appearance", "default",
                 "required_message", "media::image", "read_only", "repeat_count", "parameters", "notes_for_staff"]


def survey_row(**cells):
    return [cells.get(h.replace("::English (en)", "").replace("::Hindi (hi)", "_hi").replace("media::", "media_"))
            for h in SURVEY_HEADER]


# A made-up ODK patrol form for the fictional Demo State. Rows are numbered as in the sheet.
ODK_SURVEY = [
    SURVEY_HEADER,
    survey_row(type="start", name="start"),                                                            # 2
    survey_row(type="begin_group", name="patrol", label="Patrol", appearance="field-list"),            # 3
    survey_row(type="text", name="Guard-Name", label="Guard name", required="yes",
               required_message="Please give a name"),                                                 # 4
    survey_row(type="integer", name="staff_count", label="People on patrol", default=2,
               constraint=". >= 1 and . <= 30 and . != 13"),                                           # 5
    survey_row(type="date", name="patrol_date", label_hi="गश्त की तारीख", constraint=". <= today()"),  # 6
    survey_row(type="note", name="safety_note", label="Carry water"),                                  # 7
    survey_row(type="select_one ranges", name="demo_range", label="Range", required="yes"),            # 8
    survey_row(type="select_one beats", name="beat", label="Beat", choice_filter="range=${demo_range}"),  # 9
    survey_row(type="select_one causes or_other", name="cause", label="Likely cause"),                 # 10
    survey_row(type="select_one_from_file villages.csv", name="village", label="Village"),             # 11
    survey_row(type="barcode", name="tag", label="Tag"),                                                # 12
    survey_row(type="text", name="cause_other", label="Other cause",
               relevant="${cause} = 'other' and ${patrol_date} = today()"),                            # 13
    survey_row(type="text", name="code", label="Demo code", constraint="regex(., '[A-Z]{3}')",
               constraint_message="Three capital letters"),                                            # 14
    survey_row(type="calculate", name="summary", calculation="concat(${Guard-Name}, ' on patrol')"),   # 15
    survey_row(type="calculate", name="double_staff", calculation="${staff_count} * 2"),               # 16
    survey_row(type="image", name="photo", label="Photo", media_image="demo-icon.png"),                # 17
    survey_row(type="range", name="smoke_level", label="Smoke", parameters="start=0 end=10 step=1"),   # 18
    survey_row(type="select_multiple hazards", name="hazards", label="Hazards", appearance="minimal"),  # 19
    survey_row(type="text", name="remarks", label="Remarks for ${Guard-Name}", read_only="yes"),       # 20
    survey_row(type="text", name="summary_text", label="Summary", relevant="${summary} != ''"),        # 21
    survey_row(type="end_group"),                                                                       # 22
    survey_row(type="begin_repeat", name="visits", label="Visits", repeat_count="${staff_count}"),     # 23
    survey_row(type="geopoint", name="visit_point", label="Where", parameters="capture-accuracy=10"),  # 24
    survey_row(type="end_repeat"),                                                                      # 25
    survey_row(type="text", name="loose_question", label="Anything else", notes_for_staff="ask twice"),  # 26
    survey_row(type="acknowledge", name="ack", label="I checked the fire line"),                       # 27
    survey_row(type="mystery_type", name="mystery", label="Mystery"),                                   # 28
]
ODK_CHOICES = [
    ["list_name", "name", "label::English (en)", "range", "media::image"],
    ["ranges", "north", "North Range", None, None],
    ["ranges", "south", "South Range", None, None],
    ["beats", "Beat 1", "Beat 1", "north", None],
    ["beats", "Beat 2", "Beat 2", "south", "demo-beat.png"],
    ["causes", "lightning", "Lightning", None, None],
    ["causes", "other", "Other", None, None],
    ["hazards", "dry-grass", "Dry grass", None, None],
    ["hazards", "power-line", "Power line", None, None],
    ["spare", "x", "Never used", None, None],
]
ODK_SETTINGS = [
    ["form_title", "form_id", "version", "default_language", "instance_name"],
    ["Demo State patrol", "demo_patrol_ODK", "2026031501", "English (en)", "concat(${Guard-Name})"],
]


class ODKImportTests(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        data = workbook({"survey": ODK_SURVEY, "choices": ODK_CHOICES, "settings": ODK_SETTINGS,
                         "entities": [["list_name"], ["trees"]]})
        cls.result = import_xlsform(data, "demo-patrol.xlsx")
        cls.items = {}

        def visit(items):
            for item in items:
                cls.items[item["key"]] = item
                visit(item.get("items", []))
        for section in cls.result.form["sections"]:
            cls.items[section["key"]] = section
            visit(section["items"])

    def expect(self, row, column, text, sheet="survey"):
        """The report has an entry for this place whose message contains text."""
        found = [i for i in self.result.report if i.sheet == sheet and i.row == row and i.column == column]
        self.assertTrue(any(text in i.message for i in found),
                        f"no report entry for {sheet} row {row} column {column} with “{text}”. "
                        f"Report:\n{report_text(self.result.report)}")

    def test_the_draft_passes_the_validator(self):
        self.assertEqual(self.result.problems, [])
        self.assertEqual(formcheck.check(self.result.form)[0], [])

    def test_supported_parts_are_converted(self):
        items = self.items
        self.assertEqual(self.result.form["id"], "demo-patrol-odk")
        self.assertEqual(self.result.form["version"], 1)
        self.assertEqual(items["guard_name"]["required"], True)
        self.assertEqual((items["staff_count"]["min"], items["staff_count"]["max"]), (1, 30))
        self.assertEqual(items["staff_count"]["decimals"], 0)
        self.assertEqual(items["patrol_date"]["notInFuture"], True)
        self.assertEqual(items["patrol_date"]["label"], {"en": "गश्त की तारीख", "hi": "गश्त की तारीख"})
        self.assertEqual(items["beat"]["parent"], "demo_range")
        lists = {lst["key"]: lst["choices"] for lst in self.result.form["lists"]}
        self.assertEqual(items["beat"]["list"], "beats")
        self.assertEqual([(c["key"], c["parent"]) for c in lists["beats"]], [("beat-1", "north"), ("beat-2", "south")])
        self.assertEqual([c["key"] for c in items["hazards"]["choices"]], ["dry-grass", "power-line"])
        self.assertEqual(items["double_staff"]["calculate"],
                         {"op": "multiply", "args": [{"op": "field", "field": "staff_count"},
                                                     {"op": "number", "value": 2}]})
        self.assertEqual(items["photo"]["maxCount"], 1)
        self.assertEqual((items["smoke_level"]["min"], items["smoke_level"]["max"]), (0, 10))
        self.assertEqual(items["visits"]["type"], "repeat")
        self.assertEqual(self.result.form["sections"][-1]["title"], {"en": "Questions"})
        for dropped in ("start", "safety_note", "village", "tag", "summary", "ack", "mystery"):
            self.assertNotIn(dropped, items)
        # A question shown only when a dropped calculation is answered keeps the question, not the condition.
        self.assertNotIn("showWhen", items["summary_text"])
        self.assertNotIn("showWhen", items["cause_other"])

    def test_unsupported_features_are_in_the_report(self):
        self.expect(2, "type", "phone fills it in by itself")
        self.expect(3, "appearance", "field-list")
        self.expect(4, "name", "became the key “guard_name”")
        self.expect(4, "required_message", "own message")
        self.expect(5, "default", "no default answers")
        self.expect(5, "constraint", "“. != 13” of the constraint was not imported, because it allows or refuses")
        self.expect(7, "type", "has no notes")
        self.expect(10, "type", "or_other")
        self.expect(11, "type", "separate file")
        self.expect(12, "type", "barcodes")
        self.expect(13, "relevant", "with today(), and the form format compares answers only with fixed values")
        self.expect(14, "constraint", "does not start with ^ and end with $")
        self.expect(15, "calculation", "concat()")
        self.expect(17, "media::image", "pictures, sound or video")
        self.expect(18, "type", "slider")
        self.expect(19, "appearance", "minimal")
        self.expect(20, "label", "${…}")
        self.expect(20, "read_only", "read-only")
        self.expect(21, "relevant", "“summary”, which was not imported")
        self.expect(23, "repeat_count", "worked out from answers")
        self.expect(24, "parameters", "no settings")
        self.expect(23, None, "section called “Questions”")
        self.expect(27, "type", "acknowledge")
        self.expect(28, "type", "mystery_type")
        self.expect(None, "notes_for_staff", "not an XLSForm column")
        self.expect(4, "name", "became “beat-1”", sheet="choices")
        self.expect(5, "media::image", "picture", sheet="choices")
        self.expect(10, "list_name", "“spare” is not used", sheet="choices")
        self.expect(2, "instance_name", "instance_name", sheet="settings")
        self.expect(None, "version", "2026031501", sheet="settings")
        self.expect(None, "form_id", "demo-patrol-odk", sheet="settings")
        self.expect(None, None, "“entities” is not used", sheet="entities")
        self.assertTrue(any("no English version" in i.message for i in self.result.report))

    def test_report_text_names_sheet_row_and_question(self):
        text = report_text(self.result.report)
        self.assertIn("survey sheet, row 5 (staff_count): “2” in the default column was not imported", text)
        rows = [i.row for i in self.result.report if i.sheet == "survey" and i.row]
        self.assertEqual(rows, sorted(rows))


class ExpressionTests(unittest.TestCase):
    def setUp(self):
        self.refs = {
            "kind": Ref("kind", "single-choice", "choice", (), {"a": "a", "Other": "other"}),
            "tags": Ref("tags", "multiple-choice", "choices", (), {"x": "x"}),
            "count": Ref("count", "number", "number"),
            "when": Ref("when", "time", "time"),
            "area": Ref("area", "polygon", "polygon"),
            "teams": Ref("teams", "repeat", "repeat", ("teams",)),
            "members": Ref("members", "number", "number", ("teams",)),
            "note": Ref("note", "text", "text"),
        }
        self.res = Resolver(self.refs)

    def test_not_equals_keeps_its_meaning(self):
        # In XLSForm != is true for an unanswered question; the form format's not-equals is not.
        self.assertEqual(to_condition("${kind} != 'a'", self.res),
                         {"op": "not", "condition": {"op": "equals", "field": "kind", "value": "a"}})
        self.assertEqual(to_condition("(${kind} != '' and ${kind} != 'a')", self.res),
                         {"op": "not-equals", "field": "kind", "value": "a"})

    def test_conditions(self):
        self.assertEqual(to_condition("selected(${tags}, 'x')", self.res), {"op": "includes", "field": "tags",
                                                                           "value": "x"})
        self.assertEqual(to_condition("selected(${kind}, 'Other')", self.res), {"op": "equals", "field": "kind",
                                                                               "value": "other"})
        self.assertEqual(to_condition("10 < ${count}", self.res), {"op": "more-than", "field": "count", "value": 10})
        self.assertEqual(to_condition("string-length(${note}) > 0", self.res), {"op": "answered", "field": "note"})
        self.assertEqual(to_condition("decimal-time(${when}) >= decimal-time('06:30:00')", self.res),
                         {"op": "at-least", "field": "when", "value": "06:30"})
        self.assertEqual(to_condition("${note} = ''", self.res),
                         {"op": "not", "condition": {"op": "answered", "field": "note"}})

    def test_untranslatable_conditions_say_why(self):
        for text, reason in (("${kind} = 'b'", "not one of its choices"), ("${members} > 2", "many answers"),
                             ("${note} > 'a'", "in order"), ("${missing} = 1", "not a question in the form"),
                             ("/data/count > 1", "cannot read"), ("${count} > today()", "only with fixed values"), ("${count} > ${note}", "not a number"),
                             ("${tags} = 'x'", "selected()")):
            with self.subTest(text), self.assertRaisesRegex(Untranslatable, reason):
                to_condition(text, self.res)

    def test_calculations(self):
        self.assertEqual(to_calculation("area(${area})", self.res)[0],
                         {"op": "multiply", "args": [{"op": "area", "field": "area"}, {"op": "number", "value": 10000}]})
        self.assertEqual(to_calculation("round(count(${teams}) - -1, 2)", self.res),
                         ({"op": "subtract", "args": [{"op": "count", "group": "teams"},
                                                      {"op": "number", "value": -1}]}, 2))
        with self.assertRaisesRegex(Untranslatable, "if()"):
            to_calculation("if(${count} > 1, 1, 0)", self.res)
        with self.assertRaisesRegex(Untranslatable, "true or false"):
            to_calculation("${count} > 1", self.res)

    def test_constraints(self):
        self.assertEqual(to_constraint(". > 5 and . < 10", "number", True), ({"min": 6, "max": 9}, []))
        props, left = to_constraint(". > 5", "number", False)
        self.assertEqual(props, {})
        self.assertIn("limit itself", left[0].reason)
        self.assertEqual(to_constraint("regex(., '^(DS|DT)-[0-9]+$')", "text")[0], {"pattern": "(DS|DT)-[0-9]+"})
        self.assertEqual(to_constraint("regex(., '^[|a]+$')", "text")[0], {"pattern": "[|a]+"})
        self.assertEqual(to_constraint("regex(., '^(a|b)$')", "text")[0], {"pattern": "a|b"})
        self.assertIn("whole answer", to_constraint("regex(., '^a|b$')", "text")[1][0].reason)


class ImportProblemTests(unittest.TestCase):
    def test_not_a_workbook(self):
        with self.assertRaisesRegex(XLSFormError, "not an Excel workbook"):
            import_xlsform(b"type,name\ntext,demo\n")

    def test_no_survey_sheet(self):
        with self.assertRaisesRegex(XLSFormError, "no survey sheet"):
            import_xlsform(workbook({"choices": [["list_name", "name", "label"]]}))

    def test_no_usable_questions(self):
        with self.assertRaisesRegex(XLSFormError, "no questions"):
            import_xlsform(workbook({"survey": [["type", "name", "label"], ["note", "hello", "Hello"]]}))

    def test_conditions_that_would_loop_or_are_ambiguous_are_reported(self):
        data = workbook({"survey": [
            ["type", "name", "label", "relevant"],
            ["begin_group", "visit", "Visit", "${seen} != ''"],
            ["text", "seen", "Seen", "${seen} = 'x'"],
            ["text", "twice", "First", None],
            ["text", "twice", "Second", None],
            ["text", "after", "After", "${twice} = 'yes'"],
            ["end_group", None, None, None]]})
        result = import_xlsform(data)
        self.assertEqual(result.problems, [])
        messages = {i.row: i.message for i in result.report if i.column == "relevant"}
        self.assertIn("inside this group", messages[2])
        self.assertIn("the question itself", messages[3])
        self.assertIn("more than one question", messages[6])
        keys = [i["key"] for i in result.form["sections"][0]["items"]]
        self.assertEqual(keys, ["seen", "twice", "twice_2", "after"])

    def test_a_pattern_the_validator_refuses_is_reported_with_its_message(self):
        data = workbook({"survey": [["type", "name", "label", "constraint"],
                                    ["text", "demo_code", "Demo code", "regex(., '^(?=D)[A-Z]+$')"]]})
        result = import_xlsform(data)
        self.assertEqual(result.problems, [])
        self.assertNotIn("pattern", result.form["sections"][0]["items"][0])
        message = next(i.message for i in result.report if i.column == "constraint")
        self.assertIn('Field "demo_code"', message)


if __name__ == "__main__":
    unittest.main()
