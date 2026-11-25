"""Convert between XLSForm workbooks and the Open FIMRA form format.

XLSForm is the spreadsheet form format of ODK. A workbook has a survey sheet
(one row per question or group), a choices sheet (the choice lists) and a
settings sheet. The Open FIMRA form format is described in
packages/form-schema/SPEC.md.

import_xlsform reads a workbook into a draft form and a review report. The
report lists, with its sheet, row and reason, everything in the workbook that
the form format cannot hold or holds only approximately. Nothing is dropped
without a report item. The draft is checked with the Go validator from
packages/form-schema (see formcheck.py).

export_xlsform writes a form as a workbook. Most of a form maps to standard
XLSForm. What XLSForm has no place for is kept in survey columns whose names
start with fimra_ and in a sheet named open_fimra, which ODK tools ignore and
import_xlsform reads back. The geo worker README lists the mapping.
"""

import io
import re
import zipfile
from dataclasses import dataclass, field

import openpyxl
from openpyxl.utils.exceptions import InvalidFileException

import formcheck
from xlsform_expr import (AMBIGUOUS, Ref, Resolver, Untranslatable, bare, calculation_text, condition_text,
                          number_text, parse, pattern_text, to_calculation, to_condition, to_constraint)

MAX_FILE_BYTES = 10 * 1024 * 1024
MAX_UNPACKED_BYTES = 64 * 1024 * 1024
MAX_ZIP_ENTRIES = 512
MAX_SHEET_ROWS = 10_000
MAX_TEXT = 2000

FIMRA_SHEET = "open_fimra"


class XLSFormError(Exception):
    """The workbook cannot be converted at all. The message says why, in plain English."""


@dataclass
class ReportItem:
    """One entry in a review report."""
    message: str
    sheet: str | None = None
    row: int | None = None
    column: str | None = None
    item: str | None = None

    def as_dict(self):
        return {k: v for k, v in (("sheet", self.sheet), ("row", self.row), ("column", self.column),
                                  ("item", self.item), ("message", self.message)) if v is not None}

    def text(self):
        where = []
        if self.sheet:
            where.append(f"{self.sheet} sheet")
        if self.row:
            where.append(f"row {self.row}")
        place = ", ".join(where)
        if self.item:
            place = f"{place} ({self.item})" if place else self.item
        return f"{place}: {self.message}" if place else self.message


@dataclass
class ImportResult:
    """A draft form, its review report, and any problems the form validator found."""
    form: dict
    report: list
    problems: list


def report_text(items):
    """A review report as plain text, one entry per line."""
    if not items:
        return "Nothing to review: every part of the workbook was converted exactly.\n"
    return "".join(f"- {item.text()}\n" for item in items)


# ---------------------------------------------------------------------------
# Languages

LANGUAGE_NAMES = {"en": "English", "hi": "Hindi"}
_LANG_CODE = re.compile(r"^[a-z]{2,3}(-[A-Za-z0-9]{2,8})*$")
_LANG_HEADER = re.compile(r"^(?P<name>.*?)\s*\((?P<code>[^()]+)\)\s*$")


def language_code(text):
    """The code in a language name such as "Hindi (hi)", or None."""
    m = _LANG_HEADER.match(text.strip())
    if m and _LANG_CODE.match(m.group("code").strip()):
        return m.group("code").strip()
    if text.strip().lower() == "english":
        return "en"
    return None


def language_header(code):
    return f"{LANGUAGE_NAMES.get(code, code)} ({code})"


# ---------------------------------------------------------------------------
# Reading the workbook

@dataclass
class Row:
    sheet: str
    number: int
    cells: dict  # (base, language or None) -> text

    def get(self, base):
        return self.cells.get((base, None), "")

    def has(self, base):
        return any(b == base and v for (b, _), v in self.cells.items())


@dataclass
class Sheet:
    name: str
    rows: list
    headers: list  # (base, language or None, header as written)


_ALIASES = {
    "list name": "list_name", "constraint message": "constraint_message", "jr:constraintmsg": "constraint_message",
    "required message": "required_message", "jr:requiredmsg": "required_message", "choice filter": "choice_filter",
    "read only": "read_only", "repeat count": "repeat_count", "jr:count": "repeat_count",
    "guidance hint": "guidance_hint", "image": "media::image", "audio": "media::audio", "video": "media::video",
    "big-image": "media::big-image",
}
_PREFIXED = {"media", "bind", "body", "instance"}


def _header(text):
    parts = [p.strip() for p in text.split("::")]
    if parts[0].lower() in _PREFIXED and len(parts) > 1:
        base, lang = parts[0].lower() + "::" + parts[1], (parts[2] if len(parts) > 2 else None)
    else:
        base, lang = parts[0].lower(), ("::".join(parts[1:]) if len(parts) > 1 else None)
    return _ALIASES.get(base, base), lang


def _cell_text(value):
    if value is None:
        return ""
    if isinstance(value, bool):
        return "yes" if value else "no"
    if isinstance(value, float) and value.is_integer():
        return str(int(value))
    if hasattr(value, "isoformat"):
        return value.isoformat()
    return str(value).strip()


def read_workbook(data):
    """The sheets of an .xlsx workbook, by lower-case name."""
    if len(data) > MAX_FILE_BYTES:
        raise XLSFormError("The file is larger than 10 MB. An XLSForm is usually much smaller; check that this is "
                           "the right file.")
    try:
        with zipfile.ZipFile(io.BytesIO(data)) as zf:
            entries = zf.infolist()
    except zipfile.BadZipFile:
        raise XLSFormError("The file is not an Excel workbook. Save the XLSForm as an Excel Workbook (.xlsx) "
                           "and upload it again.") from None
    if len(entries) > MAX_ZIP_ENTRIES or sum(e.file_size for e in entries) > MAX_UNPACKED_BYTES:
        raise XLSFormError("The workbook unpacks to more than 64 MB. An XLSForm is usually much smaller; check "
                           "that this is the right file.")
    try:
        book = openpyxl.load_workbook(io.BytesIO(data), read_only=True, data_only=True)
    except (InvalidFileException, KeyError, ValueError, OSError, zipfile.BadZipFile, SyntaxError) as exc:
        raise XLSFormError("The workbook could not be read. Open it in a spreadsheet program, save it as an "
                           "Excel Workbook (.xlsx) and upload it again.") from exc
    sheets = {}
    try:
        for ws in book.worksheets:
            name = ws.title.strip()
            headers, rows = [], []
            for number, values in enumerate(ws.iter_rows(values_only=True), start=1):
                if number > MAX_SHEET_ROWS + 1:
                    raise XLSFormError(f"The {name} sheet has more than {MAX_SHEET_ROWS} rows.")
                texts = [_cell_text(v) for v in values]
                if not headers:
                    if any(texts):
                        headers = [(*_header(t), t) if t else None for t in texts]
                    continue
                cells = {}
                for header, text in zip(headers, texts):
                    if header and text:
                        cells.setdefault(header[:2], text)
                if cells:
                    rows.append(Row(name, number, cells))
            sheets.setdefault(name.lower(), Sheet(name, rows, [h for h in headers if h]))
    except (KeyError, ValueError, OSError, SyntaxError) as exc:
        raise XLSFormError("The workbook could not be read. Open it in a spreadsheet program, save it as an "
                           "Excel Workbook (.xlsx) and upload it again.") from exc
    finally:
        book.close()
    return sheets


# ---------------------------------------------------------------------------
# Keys

_KEY_BAD = re.compile(r"[^a-z0-9_]")
_CHOICE_BAD = re.compile(r"[^a-z0-9_-]")


def _item_key(name):
    key = _KEY_BAD.sub("", re.sub(r"[\s.\-]+", "_", name.strip().lower()))
    key = key.strip("_")
    if not key:
        return ""
    if not key[0].isalpha():
        key = "q_" + key
    return key[:40].rstrip("_")


def _form_id(text):
    ident = re.sub(r"[^a-z0-9]+", "-", text.strip().lower()).strip("-")
    if ident and not ident[0].isalpha():
        ident = "form-" + ident
    ident = ident[:60].rstrip("-")
    return ident if len(ident) >= 2 else ""


def _choice_key(name):
    key = _CHOICE_BAD.sub("", re.sub(r"[\s.]+", "-", name.strip().lower()))
    key = key.lstrip("_-")
    return key[:40]


class Keys:
    """Hands out unique keys."""

    def __init__(self):
        self.taken = set()

    def take(self, wanted, fallback):
        base = wanted or fallback
        key, n = base, 2
        while key in self.taken:
            suffix = f"_{n}"
            key = base[:40 - len(suffix)] + suffix
            n += 1
        self.taken.add(key)
        return key


def _humanise(key):
    return key.replace("_", " ").strip().capitalize() or "Question"


# ---------------------------------------------------------------------------
# What each XLSForm question type becomes

FIELD_TYPES = {
    "text": "text", "string": "text", "integer": "number", "int": "number", "decimal": "number", "range": "number",
    "date": "date", "time": "time", "datetime": "date-time", "geopoint": "point", "geotrace": "track",
    "geoshape": "polygon", "image": "photo", "file": "file", "calculate": "calculated",
    "select_one": "single-choice", "select_multiple": "multiple-choice",
}
_AUTOMATIC = ("the phone fills it in by itself, and Open FIMRA records who sent each report and when on its own")
DROPPED_TYPES = {
    "note": "it shows text without asking a question, and the form format has no notes. Put the text in a "
            "hint or a section description instead",
    "acknowledge": "the form format has no acknowledge question. A required yes or no choice does the same job",
    "trigger": "the form format has no acknowledge question. A required yes or no choice does the same job",
    "barcode": "the form format cannot scan barcodes",
    "audio": "the form format takes photos and files, not sound recordings",
    "background-audio": "the form format takes photos and files, not sound recordings",
    "video": "the form format takes photos and files, not video recordings",
    "hidden": "it holds a value that nobody sees, and the form format has no such question",
    "rank": "the form format has no question for putting choices in order",
    "select_one_from_file": "its choices come from a separate file, and the form format keeps choices in the form",
    "select_multiple_from_file": "its choices come from a separate file, and the form format keeps choices in "
                                 "the form",
    "select_one_external": "its choices come from a separate file, and the form format keeps choices in the form",
    "xml-external": "it reads a separate data file, which the form format cannot do",
    "csv-external": "it reads a separate data file, which the form format cannot do",
    "start": _AUTOMATIC, "end": _AUTOMATIC, "today": _AUTOMATIC, "deviceid": _AUTOMATIC,
    "phonenumber": _AUTOMATIC, "username": _AUTOMATIC, "email": _AUTOMATIC, "simserial": _AUTOMATIC,
    "subscriberid": _AUTOMATIC, "audit": _AUTOMATIC, "start-geopoint": _AUTOMATIC,
}
VALUE_TYPES = {
    "text": "text", "number": "number", "date": "date", "time": "time", "date-time": "date-time",
    "single-choice": "choice", "multiple-choice": "choices", "point": "point", "track": "line",
    "polygon": "polygon", "photo": "attachments", "signature": "attachment", "file": "attachments",
    "calculated": "number",
}
ALERT_SOURCES = {
    "alert-id": ("text", "text"), "observed-at": ("dateTime", "date-time"), "raised-at": ("dateTime", "date-time"),
    "location": ("geopoint", "point"), "unit": ("text", "text"), "forest-class": ("text", "text"),
    "source": ("text", "text"), "confidence": ("text", "text"), "fire-power": ("decimal", "number"),
}
# The form-format widget for an XLSForm appearance, and back.
APPEARANCE_WIDGETS = {
    ("text", "multiline"): "paragraph", ("number", "counter"): "stepper",
    ("single-choice", "minimal"): "dropdown", ("single-choice", "columns-pack"): "buttons",
    ("multiple-choice", "columns-pack"): "buttons",
}
WIDGET_APPEARANCES = {(t, w): a for (t, a), w in APPEARANCE_WIDGETS.items()}
WIDGETS = {"text": {"line", "paragraph"}, "number": {"input", "stepper"},
           "single-choice": {"radio", "dropdown", "buttons"}, "multiple-choice": {"checkboxes", "buttons"}}

# Survey columns that carry what XLSForm has no column for. ODK tools ignore them.
FIMRA_COLUMNS = ["fimra_stage", "fimra_unit", "fimra_decimals", "fimra_min_count", "fimra_max_count",
                 "fimra_max_size_mb", "fimra_accept", "fimra_max_accuracy", "fimra_alert_value",
                 "fimra_widget", "fimra_width", "fimra_collapsed"]
FIMRA_TEXT_COLUMNS = ["fimra_item_label"]
FIMRA_NAMES = {
    "fimra_stage": "the stage", "fimra_unit": "the unit", "fimra_decimals": "decimal places",
    "fimra_min_count": "the fewest entries", "fimra_max_count": "the most entries",
    "fimra_max_size_mb": "the largest file size", "fimra_accept": "file endings",
    "fimra_max_accuracy": "the GPS accuracy limit", "fimra_alert_value": "the alert value",
    "fimra_widget": "the kind of control", "fimra_width": "the width", "fimra_collapsed": "folding away",
    "fimra_item_label": "the name of one entry",
}

SURVEY_COLUMNS = {"type", "name", "label", "hint", "required", "relevant", "constraint", "constraint_message",
                  "calculation", "choice_filter", "appearance", "read_only", "repeat_count", "default",
                  "required_message", "guidance_hint", "parameters", "trigger", "save_to", "autoplay", "note",
                  *FIMRA_COLUMNS, *FIMRA_TEXT_COLUMNS}
TEXT_COLUMNS = {"label", "hint", "constraint_message", "required_message", "guidance_hint", "fimra_item_label"}
CHOICE_COLUMNS = {"list_name", "name", "label"}
SETTINGS_USED = {"form_title", "form_id", "version", "default_language"}

COLUMN_REASONS = {
    "default": "the form format has no default answers",
    "required_message": "the form format shows its own message for a required question",
    "guidance_hint": "the form format has one hint per question; put the guidance in the hint",
    "parameters": "the form format has no settings for this",
    "trigger": "the form format has no triggers that recalculate a question",
    "save_to": "the form format does not save answers to entity lists",
    "autoplay": "the form format has no sound or video in labels",
    "read_only": "only values filled in from the alert are read-only in the form format",
    "repeat_count": "the form format sets the fewest and most entries, not a count worked out from answers",
    "choice_filter": "only choice questions can have a choice filter",
    "calculation": "only calculate rows can have a calculation in the form format",
    "constraint": "the form format has no checks for this kind of question",
    "constraint_message": "the form format keeps a check message only for the pattern of a text question",
    "note": "the form format has no notes",
}


def _column_reason(base):
    if base.startswith("media::"):
        return "the form format has no pictures, sound or video in labels"
    if base.split("::")[0] in ("bind", "body", "instance"):
        return "the form format has no custom XForm attributes"
    return COLUMN_REASONS.get(base, "the form format has no place for it here")


def _yes(text):
    return text.strip().lower() in ("yes", "true", "true()", "1", "y")


def _no(text):
    return text.strip().lower() in ("", "no", "false", "false()", "0", "n")


# ---------------------------------------------------------------------------
# Import


@dataclass
class _Node:
    row: Row
    kind: str  # group, repeat or field
    item_type: str = ""  # the form-format type of a field
    xtype: str = ""  # the XLSForm type word
    list_name: str = ""
    name: str = ""
    key: str = ""
    children: list = field(default_factory=list)
    repeats: tuple = ()
    inside: tuple = ()
    used: set = field(default_factory=set)
    dropped: bool = False
    item: dict = field(default_factory=dict)


class _Importer:
    def __init__(self, sheets, file_name, check):
        self.sheets = sheets
        self.file_name = file_name
        self.check = check
        self.report = []
        self.default_lang = "en"
        self.languages_seen = set()
        self.text_sets = []
        self.no_english = False
        self.bad_language_headers = set()

    # -- report helpers

    def note(self, message, row=None, column=None, item=None, sheet=None):
        self.report.append(ReportItem(message, sheet or (row.sheet if row else None), row.number if row else None,
                                      column, item))

    def texts(self, row, base, node=None, used=None):
        """The text in every language for one column, by language code."""
        out = {}
        for (b, lang), value in row.cells.items():
            if b != base or not value:
                continue
            if used is not None:
                used.add((b, lang))
            code = self.default_lang if lang is None else language_code(lang)
            if code is None:
                if (row.sheet, base, lang) not in self.bad_language_headers:
                    self.bad_language_headers.add((row.sheet, base, lang))
                    self.note(f"The column “{base}::{lang}” does not give a language code, such as “Hindi (hi)”, "
                              "so it was not used.", sheet=row.sheet, column=f"{base}::{lang}")
                continue
            if len(value) > MAX_TEXT:
                self.note(f"The text in “{base}” was cut to {MAX_TEXT} characters.", row, base,
                          node.key if node else None)
                value = value[:MAX_TEXT]
            if "${" in value and base in ("label", "hint"):
                self.note(f"The {base} shows answers to other questions with ${{…}}. The form format shows "
                          "labels and hints as written, so check the wording.", row, base, node.key if node else None)
            out.setdefault(code, value)
        if out and "en" not in out:
            self.no_english = True
            out = {"en": out.get(self.default_lang) or next(iter(out.values())), **out}
        self.languages_seen.update(out)
        if out:
            self.text_sets.append(set(out))
        return out

    # -- reading

    def run(self):
        survey = self.sheets.get("survey")
        if survey is None:
            raise XLSFormError("The workbook has no survey sheet, so it is not an XLSForm.")
        for name, sheet in self.sheets.items():
            if name not in ("survey", "choices", "settings", FIMRA_SHEET):
                self.note(f"The sheet “{sheet.name}” is not used. Open FIMRA reads the survey, choices, settings "
                          f"and {FIMRA_SHEET} sheets.", sheet=sheet.name)
        settings = self.read_settings()
        extras = self.read_fimra_sheet()
        self.check_headers(survey, SURVEY_COLUMNS)
        roots = self.read_survey(survey)
        nodes = list(self.walk(roots))
        if not any(n.kind == "field" and not n.dropped for n in nodes):
            raise XLSFormError("The survey sheet has no questions that Open FIMRA can use.")
        self.fimra_counts = any(h[0] == "fimra_max_count" for h in survey.headers)
        self.allocate_keys(nodes)
        self.read_choices(nodes)
        self.refs = self.make_refs(nodes)
        self.translate_calculations(nodes)
        for node in nodes:
            if not node.dropped:
                self.build(node)
        sections = self.make_sections(roots)
        form = self.assemble(settings, extras, sections, nodes)
        for node in nodes:
            if not node.dropped:
                self.unused_columns(node)
        problems, _ = self.check(form)
        return ImportResult(form, self.sorted_report(), problems)

    def sorted_report(self):
        """The report in sheet and row order, with notes about the whole form last."""
        order = {name: n for n, name in enumerate(["survey", "choices", "settings", FIMRA_SHEET])}
        indexed = list(enumerate(self.report))
        indexed.sort(key=lambda p: (p[1].sheet is None, order.get((p[1].sheet or "").lower(), len(order)),
                                    p[1].sheet or "", p[1].row or 0, p[0]))
        return [item for _, item in indexed]

    def check_headers(self, sheet, known, prefixes=("media::", "bind::", "body::", "instance::")):
        for base, lang, written in sheet.headers:
            if base in known or base.startswith(prefixes):
                continue
            self.note(f"The column “{written}” is not an XLSForm column that Open FIMRA knows, so it was not used.",
                      sheet=sheet.name, column=written)

    def read_settings(self):
        sheet = self.sheets.get("settings")
        values = {}
        if sheet and sheet.rows:
            row = sheet.rows[0]
            for (base, lang), value in row.cells.items():
                if base in SETTINGS_USED and lang is None:
                    values[base] = value
                else:
                    reason = ("the form format sets this in its own way" if base in
                              ("instance_name", "submission_url", "public_key", "style", "namespaces",
                               "allow_choice_duplicates", "clean_text_values", "auto_send", "auto_delete")
                              else "Open FIMRA does not know this setting")
                    self.note(f"The setting “{base}” was not used, because {reason}.", row, base)
            for row in sheet.rows[1:]:
                self.note("Only the first row of settings is used.", row)
        if values.get("default_language"):
            code = language_code(values["default_language"])
            if code:
                self.default_lang = code
            else:
                self.note(f"The default language “{values['default_language']}” does not give a language code, "
                          "such as “Hindi (hi)”, so English was assumed.", sheet.rows[0], "default_language")
        return values

    def read_fimra_sheet(self):
        extras = {"stages": [], "pages": [], "rows": {}}
        sheet = self.sheets.get(FIMRA_SHEET)
        if not sheet:
            return extras
        for row in sheet.rows:
            kind = row.get("type").lower()
            label = self.texts(row, "label")
            name = row.get("name")
            if kind in ("title", "description"):
                extras[kind] = (row, label)
            elif kind == "languages":
                extras["languages"] = (row, name.split())
            elif kind == "stage":
                extras["stages"].append((row, name, row.get("workflow_step"), label))
            elif kind == "page":
                extras["pages"].append((row, name, row.get("sections").split(), label))
            else:
                self.note(f"The row type “{row.get('type')}” is not one Open FIMRA knows on this sheet, so the row "
                          "was not used.", row, "type")
        return extras

    def read_survey(self, survey):
        roots, stack = [], []
        for row in survey.rows:
            raw = re.sub(r"\s+", " ", row.get("type")).strip()
            low = raw.lower()
            parent = stack[-1].children if stack else roots
            if not raw:
                self.note("The row has no type, so it was not used.", row)
                continue
            if low in ("begin group", "begin_group", "begin repeat", "begin_repeat"):
                node = _Node(row, "repeat" if "repeat" in low else "group", name=row.get("name"),
                             used={("type", None), ("name", None)})
                parent.append(node)
                stack.append(node)
                continue
            if low in ("end group", "end_group", "end repeat", "end_repeat"):
                kind = "repeat" if "repeat" in low else "group"
                if stack and stack[-1].kind == kind:
                    stack.pop()
                elif any(n.kind == kind for n in stack):
                    while stack[-1].kind != kind:
                        opened = stack.pop()
                        self.note(f"The {opened.kind} “{opened.name}” has no end row of its own, so it was closed "
                                  "here.", opened.row)
                    stack.pop()
                else:
                    self.note(f"This end of a {kind} has no matching beginning, so it was not used.", row)
                continue
            node = self.read_field(row, raw)
            parent.append(node)
        for opened in reversed(stack):
            self.note(f"The {opened.kind} “{opened.name}” has no end row, so it was closed at the end of the sheet.",
                      opened.row)
        return roots

    def read_field(self, row, raw):
        words = raw.split(" ")
        word = words[0].lower()
        if word == "select" and len(words) > 2 and words[1].lower() in ("one", "multiple"):
            word, words = f"select_{words[1].lower()}", [f"select_{words[1].lower()}", *words[2:]]
        node = _Node(row, "field", xtype=word, name=row.get("name"), used={("type", None), ("name", None)})
        alert = row.get("fimra_alert_value")
        if alert:
            node.used.add(("fimra_alert_value", None))
            if alert in ALERT_SOURCES:
                node.item_type = "alert-value"
                node.used.add(("read_only", None))
                return node
            self.note(f"“{alert}” is not a value from the alert, so the question was not imported.", row,
                      "fimra_alert_value", node.name)
            node.dropped = True
            return node
        if word in ("select_one", "select_multiple"):
            if len(words) < 2:
                self.note("The choice question does not name its list of choices, so it was not imported.", row,
                          "type", node.name)
                node.dropped = True
                return node
            node.list_name = words[1]
            if len(words) > 2:
                if words[2].lower() == "or_other":
                    self.note("“or_other” adds a choice with a box for other answers, which the form format "
                              "does not do by itself. Add an “other” choice and a text question shown when it is "
                              "picked.", row, "type", node.name)
                else:
                    self.note(f"The type “{raw}” has extra words after the list name, which were not used.", row,
                              "type", node.name)
        if word == "image" and "signature" in row.get("appearance").lower().split():
            node.item_type = "signature"
        elif word in FIELD_TYPES:
            node.item_type = FIELD_TYPES[word]
        else:
            reason = DROPPED_TYPES.get(word, f"Open FIMRA does not know the question type “{raw}”")
            self.note(f"The {word} question was not imported, because {reason}.", row, "type", node.name)
            node.dropped = True
        return node

    def walk(self, nodes, repeats=(), inside=()):
        for node in nodes:
            node.repeats, node.inside = repeats, inside
            yield node
            yield from self.walk(node.children, repeats + ((node,) if node.kind == "repeat" else ()),
                                 inside + (node,))

    # -- keys and choices

    def allocate_keys(self, nodes):
        keys = Keys()
        for node in nodes:
            if node.dropped:
                continue
            wanted = _item_key(node.name)
            node.key = keys.take(wanted, "group" if node.kind != "field" else "question")
            if not node.name:
                self.note(f"The row has no name, so it was given the key “{node.key}”.", node.row, "name", node.key)
            elif node.key != node.name:
                self.note(f"The name “{node.name}” became the key “{node.key}”, because keys use small letters, "
                          "digits and underscores, start with a letter and are unique.", node.row, "name", node.key)
            if node.item_type == "polygon":
                keys.taken.add(node.key + "_area_ha")
            elif node.item_type == "track":
                keys.taken.add(node.key + "_length_km")

    def read_choices(self, nodes):
        """Turn the choices sheet into lists, keeping each list's renamed choice keys."""
        sheet = self.sheets.get("choices")
        self.choice_rows = {}
        if sheet:
            for base, lang, written in sheet.headers:
                if base not in CHOICE_COLUMNS and not base.startswith("media::") and lang is not None:
                    self.note(f"The column “{written}” was not used.", sheet=sheet.name, column=written)
            for row in sheet.rows:
                list_name = row.get("list_name")
                if not list_name:
                    self.note("The choice has no list_name, so it was not used.", row)
                    continue
                self.choice_rows.setdefault(list_name, []).append(row)
        self.lists = {}  # list name -> (choices, original name -> key)
        for node in nodes:
            if node.dropped or not node.list_name:
                continue
            if node.list_name not in self.choice_rows:
                self.note(f"The choice question uses the list “{node.list_name}”, which is not on the choices "
                          "sheet, so it was not imported.", node.row, "type", node.key)
                node.dropped = True
                continue
            if node.list_name not in self.lists:
                self.lists[node.list_name] = self.make_choices(node.list_name)
            if not self.lists[node.list_name][0]:
                node.dropped = True
                self.note(f"The list “{node.list_name}” has no usable choices, so the question was not imported.",
                          node.row, "type", node.key)
        used = {n.list_name for n in nodes if n.list_name and not n.dropped}
        for name, rows in self.choice_rows.items():
            if name not in used:
                self.note(f"The list “{name}” is not used by any imported question, so it was left out.", rows[0],
                          "list_name")

    def make_choices(self, list_name):
        choices, names, keys = [], {}, set()
        for row in self.choice_rows[list_name]:
            name = row.get("name")
            if not name:
                self.note("The choice has no name, so it was not used.", row, "name")
                continue
            if name in names:
                self.note(f"The list “{list_name}” has the choice “{name}” twice, so the second was not used.",
                          row, "name")
                continue
            key = _choice_key(name) or "choice"
            base, n = key, 2
            while key in keys:
                key = f"{base[:36]}-{n}"
                n += 1
            if key != name:
                self.note(f"The choice “{name}” became “{key}”, because choice keys use small letters, digits, "
                          "underscores and hyphens and are unique in their list.", row, "name")
            label = self.texts(row, "label")
            if not label:
                label = {"en": name}
                self.note(f"The choice “{name}” has no label, so its name is shown instead.", row, "label")
            for (base_col, lang), value in row.cells.items():
                if base_col.startswith("media::") and value:
                    self.note("The choice has a picture, sound or video, which the form format does not show.", row,
                              base_col)
            keys.add(key)
            names[name] = key
            choices.append({"key": key, "label": label, "_row": row})
        return choices, names

    def make_refs(self, nodes):
        refs, seen = {}, set()
        for node in nodes:
            if not node.name:
                continue
            if node.name in seen:
                refs[node.name] = AMBIGUOUS
                continue
            seen.add(node.name)
            if node.dropped:
                refs.setdefault(node.name, None)
                continue
            repeats = tuple(r.key for r in node.repeats)
            if node.kind != "field":
                vt = node.kind
                if node.kind == "repeat":
                    repeats += (node.key,)
                refs[node.name] = Ref(node.key, node.kind, vt, repeats, inside=tuple(n.key for n in node.inside))
                continue
            item_type, vt = node.item_type, VALUE_TYPES.get(node.item_type)
            if item_type == "alert-value":
                item_type = node.row.get("fimra_alert_value")
                vt = ALERT_SOURCES[item_type][1]
            choices = self.lists[node.list_name][1] if node.list_name else None
            refs[node.name] = Ref(node.key, item_type, vt, repeats, choices, tuple(n.key for n in node.inside))
        return refs

    def resolver(self, node):
        """A field's expressions see its own repeating groups. A group's condition is tested from outside it."""
        return Resolver(self.refs, tuple(r.key for r in node.repeats), node.key)

    def translate_calculations(self, nodes):
        """Translate calculations first, so questions that use a dropped one know about it."""
        pending = [n for n in nodes if not n.dropped and n.item_type == "calculated"]
        changed = True
        while changed:
            changed = False
            for node in pending:
                if node.dropped:
                    continue
                text = node.row.get("calculation")
                node.used.add(("calculation", None))
                try:
                    if not text:
                        raise Untranslatable("it has no calculation")
                    node.item["calculate"], decimals = to_calculation(text, self.resolver(node))
                    if decimals is not None:
                        node.item["decimals"] = decimals
                except Untranslatable as exc:
                    self.note(f"The calculation “{text}” was not imported, because {exc}. The calculated question "
                              "was left out.", node.row, "calculation", node.key)
                    node.dropped = True
                    self.refs[node.name] = None
                    changed = True

    # -- building items

    def condition(self, node, column, text):
        try:
            return to_condition(text, self.resolver(node))
        except Untranslatable as exc:
            effect = {"relevant": "It is always shown now.",
                      "required": "It is no longer required."}.get(column, "")
            self.note(f"The {column} expression “{text}” was not imported, because {exc}. {effect}".strip(),
                      node.row, column, node.key)
            return None

    def build(self, node):
        row, item = node.row, node.item
        out = {"key": node.key}
        if node.kind != "field":
            out["type"] = node.kind
            title = self.texts(row, "label", node, node.used)
            if not title:
                title = {"en": _humanise(node.key)}
                self.note("The group has no label, so it was given one from its key.", row, "label", node.key)
            out["title"] = title
            description = self.texts(row, "hint", node, node.used)
            if description:
                out["description"] = description
        else:
            out["type"] = node.item_type
            label = self.texts(row, "label", node, node.used)
            if not label:
                label = {"en": _humanise(node.key)}
                if node.item_type != "calculated" or row.has("label"):
                    self.note("The question has no label, so it was given one from its key.", row, "label",
                              node.key)
            out["label"] = label
            hint = self.texts(row, "hint", node, node.used)
            if hint:
                out["hint"] = hint
        relevant = row.get("relevant")
        if relevant:
            node.used.add(("relevant", None))
            cond = self.condition(node, "relevant", relevant)
            if cond:
                out["showWhen"] = cond
        if node.kind == "field":
            self.build_required(node, out)
            getattr(self, "field_" + node.item_type.replace("-", "_"))(node, out)
        elif node.kind == "repeat":
            self.build_repeat(node, out)
        self.build_layout(node, out)
        out.update(item)
        node.item = out

    def build_required(self, node, out):
        text = node.row.get("required")
        if not text:
            return
        node.used.add(("required", None))
        if node.item_type in ("calculated", "alert-value"):
            self.note("The question is filled in by the system, so it cannot be required.", node.row, "required",
                      node.key)
        elif _yes(text):
            out["required"] = True
        elif not _no(text):
            cond = self.condition(node, "required", text)
            if cond:
                out["requireWhen"] = cond

    def number_column(self, node, column, whole=True, low=None, high=None):
        text = node.row.get(column)
        if not text:
            return None
        node.used.add((column, None))
        try:
            value = float(text)
            if whole:
                if not value.is_integer():
                    raise ValueError
                value = int(value)
            elif value.is_integer():
                value = int(value)
            if (low is not None and value < low) or (high is not None and value > high):
                raise ValueError
            return value
        except ValueError:
            span = f" from {low} to {high}" if low is not None and high is not None else ""
            self.note(f"“{text}” is not a {'whole ' if whole else ''}number{span}, so it was not used.", node.row,
                      column, node.key)
            return None

    def put(self, out, name, value):
        if value is not None:
            out[name] = value

    def constraint(self, node, out, item_type, whole=False):
        text = node.row.get("constraint")
        if not text:
            return
        node.used.add(("constraint", None))
        try:
            props, left = to_constraint(text, item_type, whole)
        except Untranslatable as exc:
            self.note(f"The constraint “{text}” was not imported, because {exc}.", node.row, "constraint", node.key)
            return
        for part in left:
            self.note(f"The part “{part.text}” of the constraint was not imported, because {part.reason}.",
                      node.row, "constraint", node.key)
        for low, high in (("min", "max"), ("minLength", "maxLength"), ("minSelected", "maxSelected")):
            if low in props and high in props and props[low] > props[high]:
                self.note("The constraint's lower limit is above its upper limit, so neither was imported.",
                          node.row, "constraint", node.key)
                props.pop(low), props.pop(high)
        if "pattern" in props:
            problems, _ = self.check(_pattern_probe(node.key, props["pattern"]))
            if problems:
                self.note(f"The pattern “{props.pop('pattern')}” was not imported: {problems[0]}", node.row,
                          "constraint", node.key)
        out.update(props)

    def field_text(self, node, out):
        self.constraint(node, out, "text")
        hint = self.texts(node.row, "constraint_message", node, node.used)
        if hint:
            out["patternHint"] = hint

    def field_number(self, node, out):
        whole = node.xtype in ("integer", "int")
        if whole:
            out["decimals"] = 0
        else:
            self.put(out, "decimals", self.number_column(node, "fimra_decimals", True, 0, 6))
        self.put(out, "unit", self.text_column(node, "fimra_unit", 20))
        if node.xtype == "range":
            self.range(node, out)
        self.constraint(node, out, "number", whole)

    def range(self, node, out):
        text = node.row.get("parameters")
        node.used.add(("parameters", None))
        params = dict(p.split("=", 1) for p in text.split() if "=" in p)
        for name, prop in (("start", "min"), ("end", "max")):
            try:
                value = float(params.get(name, "x"))
                out[prop] = int(value) if value.is_integer() else value
            except ValueError:
                pass
        self.note("The range question became a number question with the same smallest and largest values. "
                  "The slider and its step are not kept.", node.row, "type", node.key)

    def text_column(self, node, column, longest):
        text = node.row.get(column)
        if not text:
            return None
        node.used.add((column, None))
        if len(text) > longest:
            self.note(f"“{text}” is longer than {longest} characters, so it was not used.", node.row, column, node.key)
            return None
        return text

    def field_date(self, node, out):
        self.constraint(node, out, "date")

    def field_date_time(self, node, out):
        self.constraint(node, out, "date-time")

    def field_time(self, node, out):
        pass

    def field_single_choice(self, node, out):
        self.choices(node, out)

    def field_multiple_choice(self, node, out):
        self.choices(node, out)
        self.constraint(node, out, "multiple-choice")

    def choices(self, node, out):
        text = node.row.get("choice_filter")
        if not text:
            return
        node.used.add(("choice_filter", None))
        try:
            column, parent = _choice_filter(text, self.resolver(node))
            column = column.lower()
            if parent.item_type != "single-choice":
                raise Untranslatable(f"it filters by “{parent.key}”, which is not a single choice question")
            values = {}
            for choice in self.lists[node.list_name][0]:
                value = choice["_row"].get(column)
                if value not in (parent.choices or {}):
                    raise Untranslatable(f"the choice “{choice['key']}” has “{value}” in the column “{column}”, "
                                         f"which is not a choice of “{parent.key}”" if value else
                                         f"the choice “{choice['key']}” has nothing in the column “{column}”")
                values[choice["key"]] = parent.choices[value]
        except Untranslatable as exc:
            self.note(f"The choice filter “{text}” was not imported, because {exc}. The question offers every "
                      "choice.", node.row, "choice_filter", node.key)
            return
        out["parent"] = parent.key
        node.item["_filter"] = (column, values)

    def field_point(self, node, out):
        self.put(out, "maxAccuracyMetres", self.number_column(node, "fimra_max_accuracy", False, 1, 10000))

    field_track = field_polygon = field_point

    def counts(self, node, out, low, high, upto):
        self.put(out, low, self.number_column(node, "fimra_min_count", True, 0, upto))
        self.put(out, high, self.number_column(node, "fimra_max_count", True, 1, upto))
        if high not in out and not self.fimra_counts:
            out[high] = 1

    def field_photo(self, node, out):
        self.counts(node, out, "minCount", "maxCount", 20)

    def field_signature(self, node, out):
        node.item["_appearance_used"] = {"signature"}

    def field_file(self, node, out):
        self.counts(node, out, "minCount", "maxCount", 20)
        self.put(out, "maxSizeMegabytes", self.number_column(node, "fimra_max_size_mb", False, 1, 100))
        text = node.row.get("fimra_accept")
        if text:
            node.used.add(("fimra_accept", None))
            endings = [e if e.startswith(".") else "." + e for e in re.split(r"[\s,]+", text.lower()) if e]
            good = [e for e in endings if re.match(r"^\.[a-z0-9]{1,10}$", e)]
            if len(good) != len(endings):
                self.note(f"Some of the file endings in “{text}” are not like .pdf, so they were not used.",
                          node.row, "fimra_accept", node.key)
            if good:
                out["accept"] = good

    def field_calculated(self, node, out):
        self.put(out, "unit", self.text_column(node, "fimra_unit", 20))

    def field_alert_value(self, node, out):
        out["source"] = node.row.get("fimra_alert_value")

    def build_repeat(self, node, out):
        label = self.texts(node.row, "fimra_item_label", node, node.used)
        if label:
            out["itemLabel"] = label
        self.put(out, "minRepeats", self.number_column(node, "fimra_min_count", True, 0, 500))
        self.put(out, "maxRepeats", self.number_column(node, "fimra_max_count", True, 1, 500))
        count = node.row.get("repeat_count")
        if count:
            node.used.add(("repeat_count", None))
            if count.isdigit() and 1 <= int(count) <= 500 and "minRepeats" not in out and "maxRepeats" not in out:
                out["minRepeats"] = out["maxRepeats"] = int(count)
                self.note(f"The repeat count {count} became at least and at most {count} entries. The phone does "
                          "not add them by itself.", node.row, "repeat_count", node.key)
            else:
                self.note(f"The repeat count “{count}” was not imported, because the form format sets the fewest "
                          "and most entries, not a count worked out from answers.", node.row, "repeat_count",
                          node.key)
        if out.get("minRepeats", 0) > out.get("maxRepeats", 500):
            self.note("The fewest entries is more than the most entries, so neither was imported.", node.row,
                      "fimra_min_count", node.key)
            out.pop("minRepeats"), out.pop("maxRepeats")

    def build_layout(self, node, out):
        layout = {}
        widget = node.row.get("fimra_widget")
        item_type = out["type"]
        used_appearance = node.item.pop("_appearance_used", set())
        if widget:
            node.used.add(("fimra_widget", None))
            if widget in WIDGETS.get(item_type, ()):
                layout["widget"] = widget
            else:
                self.note(f"The control “{widget}” does not suit this question, so it was not used.", node.row,
                          "fimra_widget", node.key)
        appearance = node.row.get("appearance")
        if appearance:
            node.used.add(("appearance", None))
            for token in appearance.split():
                mapped = APPEARANCE_WIDGETS.get((item_type, token.lower()))
                if token.lower() in used_appearance:
                    continue
                if mapped and not widget:
                    layout["widget"] = mapped
                elif not mapped:
                    self.note(f"The appearance “{token}” was not imported, because the form format has no such "
                              "display setting for this question. Open FIMRA lays out the form itself.", node.row,
                              "appearance", node.key)
        width = node.row.get("fimra_width")
        if width:
            node.used.add(("fimra_width", None))
            if width in ("full", "half"):
                layout["width"] = width
            else:
                self.note(f"The width “{width}” is not full or half, so it was not used.", node.row, "fimra_width",
                          node.key)
        collapsed = node.row.get("fimra_collapsed")
        if collapsed:
            node.used.add(("fimra_collapsed", None))
            if node.kind == "field":
                self.note("Only sections and groups can be folded away.", node.row, "fimra_collapsed", node.key)
            elif _yes(collapsed):
                layout["collapsed"] = True
        if layout:
            node.item["_layout"] = layout

    def unused_columns(self, node):
        for (base, lang), value in node.row.cells.items():
            if (base, lang) in node.used or not value or base not in SURVEY_COLUMNS and \
                    not base.startswith(("media::", "bind::", "body::", "instance::")):
                continue
            column = base if lang is None else f"{base}::{lang}"
            self.note(f"“{value}” in the {column} column was not imported, because {_column_reason(base)}.",
                      node.row, column, node.key)

    # -- putting the form together

    def items_of(self, nodes):
        items = []
        for node in nodes:
            if node.dropped:
                continue
            if node.kind == "field":
                items.append(node.item)
                continue
            children = self.items_of(node.children)
            if not children:
                self.note(f"The {node.kind} has no questions that could be imported, so it was left out.", node.row,
                          item=node.key)
                node.dropped = True
                continue
            node.item["items"] = children
            items.append(node.item)
        return items

    def make_sections(self, roots):
        sections, loose = [], []
        for node in roots:
            if node.kind == "group" and not node.dropped:
                if loose:
                    sections.append(("loose", loose))
                    loose = []
                sections.append(("group", node))
            else:
                loose.append(node)
        if loose:
            sections.append(("loose", loose))
        return sections

    def assemble(self, settings, extras, sections, nodes):
        keys = Keys()
        keys.taken = {n.key for n in nodes if n.key}
        out_sections, first_loose = [], True
        for kind, value in sections:
            if kind == "group":
                items = self.items_of(value.children)
                if not items:
                    self.note("The group has no questions that could be imported, so it was left out.", value.row,
                              item=value.key)
                    value.dropped = True
                    continue
                section = {k: v for k, v in value.item.items() if k != "type" and not k.startswith("_")}
                section["items"] = items
                section["_node"] = value
                out_sections.append(section)
            else:
                items = self.items_of(value)
                if not items:
                    continue
                key = keys.take("questions", "questions")
                live = [n for n in value if not n.dropped]
                if first_loose:
                    self.note("Questions outside any group were put in a section called “Questions”, because "
                              "every question in the form format belongs to a section.", live[0].row, item=key)
                    first_loose = False
                out_sections.append({"key": key, "title": {"en": "Questions"}, "items": items})
        if not out_sections:
            raise XLSFormError("The survey sheet has no questions that Open FIMRA can use.")
        form = {"formatVersion": 1}
        form["id"] = self.form_id(settings)
        form["version"] = self.version(settings)
        form["title"] = self.title(settings, extras)
        if "description" in extras:
            form["description"] = extras["description"][1]
        stages = self.stages(extras, out_sections)
        lists = self.shared_lists(nodes, form)
        languages = self.languages(extras)
        if languages:
            form["languages"] = languages
        if stages:
            form["stages"] = stages
        if lists:
            form["lists"] = lists
        form["sections"] = out_sections
        layout = self.layout(extras, out_sections, nodes)
        for section in out_sections:
            section.pop("_node", None)
        self.strip(form)
        if layout:
            form["layout"] = layout
        if self.no_english:
            self.note("Some texts have no English version. Open FIMRA needs English, so another language was "
                      "copied in its place. Translate these texts before publishing.")
        return form

    def form_id(self, settings):
        source = settings.get("form_id") or (self.file_name or "")
        ident = _form_id(re.sub(r"\.xlsx$", "", source, flags=re.I))
        if not ident:
            ident = "imported-form"
            self.note("The form has no usable form_id, so it was called “imported-form”.", sheet="settings",
                      column="form_id")
        elif settings.get("form_id") and ident != settings["form_id"]:
            self.note(f"The form id “{settings['form_id']}” became “{ident}”, because form ids use small letters "
                      "and digits with single hyphens between words.", sheet="settings", column="form_id")
        return ident

    def version(self, settings):
        text = settings.get("version", "")
        if text.isdigit() and 1 <= int(text) <= 2_000_000_000:
            return int(text)
        if text:
            self.note(f"The version “{text}” is not a whole number from 1, so the draft is version 1. The server "
                      "numbers published versions itself.", sheet="settings", column="version")
        return 1

    def title(self, settings, extras):
        title = dict(extras["title"][1]) if "title" in extras else {}
        if settings.get("form_title"):
            title["en"] = settings["form_title"][:MAX_TEXT]
        if not title:
            title = {"en": _humanise(_item_key(self.file_name or "") or "imported form")}
            self.note("The form has no form_title, so it was given a title from its file name.", sheet="settings",
                      column="form_title")
        return title

    def languages(self, extras):
        if "languages" not in extras:
            return None
        row, codes = extras["languages"]
        if "en" not in codes or not all(_LANG_CODE.match(c) for c in codes) or len(set(codes)) != len(codes):
            self.note("The languages row must list language codes such as “en hi”, including en, each once, so it "
                      "was not used.", row, "name")
            return None
        missing = self.languages_seen - set(codes)
        if missing:
            self.note(f"The form has texts in {', '.join(sorted(missing))}, which the languages row does not list, "
                      "so the list of languages was not used.", row, "name")
            return None
        if any(set(codes) - texts for texts in self.text_sets):
            self.note("Some texts are not given in every language on the languages row, so the list of languages "
                      "was not used.", row, "name")
            return None
        return codes

    def stages(self, extras, sections):
        stages, keys, steps = [], set(), set()
        for row, key, step, label in extras["stages"]:
            if not re.match(r"^[a-z][a-z0-9_]{0,39}$", key) or key in keys:
                self.note(f"The stage “{key}” does not have a unique key of small letters, digits and underscores, so "
                          "it was not used.", row, "name")
                continue
            if not re.match(r"^[a-z][a-z0-9_]{0,59}$", step) or step in steps:
                self.note(f"The stage “{key}” does not name its own workflow step, such as team_sent, so it was not "
                          "used.", row, "workflow_step")
                continue
            keys.add(key)
            steps.add(step)
            stages.append({"key": key, "title": label or {"en": _humanise(key)}, "workflowStep": step})
        current = stages[0]["key"] if stages else None
        for section in sections:
            node = section.get("_node")
            wanted = node.row.get("fimra_stage") if node else ""
            if node and wanted:
                node.used.add(("fimra_stage", None))
            if not stages:
                if wanted:
                    self.note(f"The section names the stage “{wanted}”, but the form has no stages, so it was not "
                              "used.", node.row, "fimra_stage", section["key"])
                continue
            if wanted in keys:
                current = wanted
            else:
                place = node.row if node else None
                self.note(f"The section has no known stage, so it was given the stage “{current}”.", place,
                          "fimra_stage", section["key"])
            section["stage"] = current
        used = {s.get("stage") for s in sections}
        for stage in list(stages):
            if stage["key"] not in used:
                self.note(f"No section uses the stage “{stage['key']}”, so it was left out.", sheet=FIMRA_SHEET)
                stages.remove(stage)
        return stages

    def shared_lists(self, nodes, form):
        """Give each choice question its choices, or a shared list when several questions use one."""
        users = {}
        for node in nodes:
            if node.kind == "field" and not node.dropped and node.list_name:
                users.setdefault(node.list_name, []).append(node)
        lists, keys = [], Keys()
        for name, fields in users.items():
            choices = self.lists[name][0]
            variants = {}
            for node in fields:
                column, values = node.item.get("_filter", (None, {}))
                variants.setdefault(column, (values, []))[1].append(node)
            for column, (values, members) in variants.items():
                made = [{"key": c["key"], "label": c["label"],
                         **({"parent": values[c["key"]]} if column else {})} for c in choices]
                if len(members) == 1 and _item_key(name) == members[0].key:
                    members[0].item["choices"] = made
                    continue
                wanted = _item_key(name) + (f"_by_{_item_key(column)}" if column and len(variants) > 1 else "")
                key = keys.take(wanted[:40], "list")
                lists.append({"key": key, "choices": made})
                for node in members:
                    node.item["list"] = key
        return lists

    def layout(self, extras, sections, nodes):
        layout = {}
        items = []
        for section in sections:
            node = section.get("_node")
            if node and "_layout" in node.item:
                items.append({"item": section["key"], **node.item["_layout"]})
            self._layout_items(section["items"], items)
        if extras["pages"]:
            pages = self.pages(extras["pages"], sections)
            if pages:
                layout["pages"] = pages
        if items:
            layout["items"] = items
        return layout

    def _layout_items(self, items, out):
        for item in items:
            if "_layout" in item:
                out.append({"item": item["key"], **item["_layout"]})
            if "items" in item:
                self._layout_items(item["items"], out)

    def pages(self, rows, sections):
        by_key = {s["key"]: s for s in sections}
        pages, placed, keys = [], set(), set()
        for row, key, names, label in rows:
            if not re.match(r"^[a-z][a-z0-9_]{0,39}$", key) or key in keys:
                self.note(f"The page “{key}” does not have a unique key of small letters, digits and underscores, so "
                          "the pages were not used.", row, "name")
                return None
            keys.add(key)
            known = [n for n in names if n in by_key and n not in placed]
            if len(known) != len(names):
                self.note("The page lists a section that is not in the form or is on another page, so the pages were "
                          "not used.", row, "sections")
                return None
            if len({by_key[n].get("stage") for n in known}) > 1:
                self.note("The page holds sections from more than one stage, so the pages were not used.", row,
                          "sections")
                return None
            if not known:
                continue
            placed.update(known)
            page = {"key": key, "sections": known}
            if label:
                page["title"] = label
            pages.append(page)
        if placed != set(by_key):
            self.note("Some sections are not on any page, so the pages were not used.", rows[0][0], "sections")
            return None
        return pages

    def strip(self, value):
        """Remove the working notes kept on items while converting."""
        if isinstance(value, dict):
            for key in [k for k in value if k.startswith("_")]:
                del value[key]
            for v in value.values():
                self.strip(v)
        elif isinstance(value, list):
            for v in value:
                self.strip(v)


def _choice_filter(text, res):
    """column = ${parent}, in either order: the usual XLSForm cascade."""
    node = bare(parse(text))
    if node.kind == "bin" and node.value == "=":
        left, right = (bare(a) for a in node.args)
        if left.kind == "ref":
            left, right = right, left
        if left.kind == "name" and right.kind == "ref":
            return left.value, res.answer(right)
    raise Untranslatable("the form format can only filter choices by one column that matches the answer to one "
                         "single choice question, written as column = ${question}")


def _pattern_probe(key, pattern):
    """A one-question form for asking the validator whether a pattern is allowed."""
    return {"formatVersion": 1, "id": "pattern-check", "version": 1, "title": {"en": "Pattern check"},
            "sections": [{"key": "s", "title": {"en": "s"},
                          "items": [{"key": key, "type": "text", "label": {"en": "x"}, "pattern": pattern}]}]}


def import_xlsform(data, file_name=None, check=formcheck.check):
    """Read an .xlsx XLSForm into a draft form, a review report and the validator's problems.

    Raises XLSFormError when the workbook cannot be converted at all.
    """
    return _Importer(read_workbook(data), file_name, check).run()


# ---------------------------------------------------------------------------
# Export


def _texts_in(form):
    """Every language code used in the form's texts."""
    codes = set()

    def visit(value, key=None):
        if isinstance(value, dict):
            if key in ("title", "description", "label", "hint", "itemLabel", "patternHint"):
                codes.update(value)
                return
            for k, v in value.items():
                visit(v, k)
        elif isinstance(value, list):
            for v in value:
                visit(v, key)
    visit(form)
    return codes


class _Exporter:
    def __init__(self, form):
        self.form = form
        codes = form.get("languages") or sorted(_texts_in(form) | {"en"}, key=lambda c: (c != "en", c))
        self.languages = list(codes)
        self.multi = self.languages != ["en"]
        self.survey, self.choices, self.notes = [], [], []
        self.refs = {}
        self.layout = {i["item"]: i for i in form.get("layout", {}).get("items", [])}
        self.lists = {lst["key"]: lst for lst in form.get("lists", [])}
        self.list_names = {}

    def text_cells(self, base, text, row):
        if not text:
            return
        for code, value in text.items():
            column = f"{base}::{language_header(code)}" if self.multi else base
            row[column] = value

    def run(self):
        self.collect_refs(self.form["sections"], ())
        self.name_lists()
        for section in self.form["sections"]:
            self.group(section, "group", top=True)
        self.write_choices()
        return self.workbook()

    def collect_refs(self, items, repeats):
        for item in items:
            kind = item.get("type", "group")
            if kind in ("group", "repeat"):
                inner = repeats + ((item["key"],) if kind == "repeat" else ())
                self.refs[item["key"]] = Ref(item["key"], kind, kind, inner)
                self.collect_refs(item["items"], inner)
                continue
            vt = ALERT_SOURCES[item["source"]][1] if kind == "alert-value" else VALUE_TYPES[kind]
            self.refs[item["key"]] = Ref(item["key"], kind, vt, repeats)

    def name_lists(self):
        taken = set(self.lists)

        def visit(items):
            for item in items:
                if "items" in item:
                    visit(item["items"])
                elif "choices" in item:
                    name, n = item["key"], 2
                    while name in taken:
                        name = f"{item['key']}_choices" if n == 2 else f"{item['key']}_choices_{n}"
                        n += 1
                    taken.add(name)
                    self.list_names[item["key"]] = name
        for section in self.form["sections"]:
            visit(section["items"])

    def base_row(self, item, kind):
        row = {"type": kind, "name": item["key"]}
        self.text_cells("label", item.get("label") or item.get("title"), row)
        self.text_cells("hint", item.get("hint") or item.get("description"), row)
        if "showWhen" in item:
            row["relevant"] = condition_text(item["showWhen"], self.refs)
        layout = self.layout.get(item["key"], {})
        if layout.get("width"):
            row["fimra_width"] = layout["width"]
        if layout.get("collapsed"):
            row["fimra_collapsed"] = "yes"
        return row

    def group(self, item, kind, top=False):
        begin = self.base_row(item, f"begin_{kind}")
        if top and item.get("stage"):
            begin["fimra_stage"] = item["stage"]
        if kind == "repeat":
            self.text_cells("fimra_item_label", item.get("itemLabel"), begin)
            for prop, column in (("minRepeats", "fimra_min_count"), ("maxRepeats", "fimra_max_count")):
                if prop in item:
                    begin[column] = item[prop]
        self.add(begin, item["key"])
        for child in item["items"]:
            kind_of = child["type"]
            if kind_of in ("group", "repeat"):
                self.group(child, kind_of)
            else:
                self.field(child)
        self.survey.append({"type": f"end_{kind}"})

    def add(self, row, key):
        self.survey.append(row)
        extra = [FIMRA_NAMES[c.split("::")[0]] for c in row if c.startswith("fimra_")]
        if extra:
            self.notes.append(ReportItem("ODK tools ignore the Open FIMRA columns for "
                                         f"{', '.join(dict.fromkeys(extra))}. Open FIMRA reads them back on import.",
                                         "survey", len(self.survey) + 1, item=key))

    def field(self, item):
        kind = item["type"]
        layout = self.layout.get(item["key"], {})
        constraint = []
        if kind == "alert-value":
            row = self.base_row(item, ALERT_SOURCES[item["source"]][0])
            row["read_only"] = "yes"
            row["fimra_alert_value"] = item["source"]
        elif kind == "text":
            row = self.base_row(item, "text")
            if "minLength" in item:
                constraint.append(f"string-length(.) >= {item['minLength']}")
            if "maxLength" in item:
                constraint.append(f"string-length(.) <= {item['maxLength']}")
            if "pattern" in item:
                constraint.append(pattern_text(item["pattern"]))
            self.text_cells("constraint_message", item.get("patternHint"), row)
        elif kind == "number":
            row = self.base_row(item, "integer" if item.get("decimals") == 0 else "decimal")
            if item.get("decimals"):
                row["fimra_decimals"] = item["decimals"]
            self.limits(item, constraint, ".", "min", "max")
        elif kind in ("date", "date-time"):
            row = self.base_row(item, "date" if kind == "date" else "dateTime")
            if item.get("notInFuture"):
                constraint.append(". <= today()" if kind == "date" else ". <= now()")
        elif kind == "time":
            row = self.base_row(item, "time")
        elif kind in ("single-choice", "multiple-choice"):
            list_name = item.get("list") or self.list_names[item["key"]]
            row = self.base_row(item, f"{'select_one' if kind == 'single-choice' else 'select_multiple'} {list_name}")
            if item.get("parent"):
                row["choice_filter"] = f"{_filter_column(item['parent'])} = ${{{item['parent']}}}"
            self.limits(item, constraint, "count-selected(.)", "minSelected", "maxSelected")
        elif kind in ("point", "track", "polygon"):
            row = self.base_row(item, {"point": "geopoint", "track": "geotrace", "polygon": "geoshape"}[kind])
            if "maxAccuracyMetres" in item:
                row["fimra_max_accuracy"] = item["maxAccuracyMetres"]
        elif kind in ("photo", "file"):
            row = self.base_row(item, "image" if kind == "photo" else "file")
            for prop, column in (("minCount", "fimra_min_count"), ("maxCount", "fimra_max_count")):
                if prop in item:
                    row[column] = item[prop]
            if "maxSizeMegabytes" in item:
                row["fimra_max_size_mb"] = item["maxSizeMegabytes"]
            if "accept" in item:
                row["fimra_accept"] = " ".join(item["accept"])
        elif kind == "signature":
            row = self.base_row(item, "image")
            row["appearance"] = "signature"
        elif kind == "calculated":
            row = self.base_row(item, "calculate")
            text = calculation_text(item["calculate"])
            row["calculation"] = f"round({text}, {item['decimals']})" if "decimals" in item else text
        else:
            raise ValueError(f"unknown field type {kind}")
        if kind in ("number", "calculated") and item.get("unit"):
            row["fimra_unit"] = item["unit"]
        if item.get("required"):
            row["required"] = "yes"
        if "requireWhen" in item:
            row["required"] = condition_text(item["requireWhen"], self.refs)
        if constraint:
            row["constraint"] = " and ".join(constraint)
        widget = layout.get("widget")
        if widget:
            appearance = WIDGET_APPEARANCES.get((kind, widget))
            if appearance:
                row["appearance"] = appearance
            else:
                row["fimra_widget"] = widget
        self.add(row, item["key"])
        if kind == "alert-value":
            self.notes.append(ReportItem("ODK cannot fill in values from the alert, so ODK shows an empty "
                                         "read-only question here.", "survey", len(self.survey) + 1,
                                         item=item["key"]))

    def limits(self, item, constraint, subject, low, high):
        if low in item:
            constraint.append(f"{subject} >= {number_text(item[low])}")
        if high in item:
            constraint.append(f"{subject} <= {number_text(item[high])}")

    def write_choices(self):
        """One choices row per choice, with the parent choice in a column named after the parent question."""
        parents = {}  # list name -> parent question keys

        def visit(items):
            for item in items:
                if "items" in item:
                    visit(item["items"])
                elif item["type"] in ("single-choice", "multiple-choice"):
                    name = item.get("list") or self.list_names[item["key"]]
                    if name not in done:
                        done[name] = item.get("choices") or self.lists[item["list"]]["choices"]
                    if item.get("parent"):
                        parents.setdefault(name, []).append(item["parent"])
        done = {}
        for section in self.form["sections"]:
            visit(section["items"])
        for lst in self.form.get("lists", []):
            done.setdefault(lst["key"], lst["choices"])
        for name, choices in done.items():
            for choice in choices:
                row = {"list_name": name, "name": choice["key"]}
                self.text_cells("label", choice["label"], row)
                for parent in dict.fromkeys(parents.get(name, [])):
                    if "parent" in choice:
                        row[_filter_column(parent)] = choice["parent"]
                self.choices.append(row)

    def fimra_rows(self):
        rows = []
        title = self.form["title"]
        if set(title) - {"en"}:
            row = {"type": "title"}
            self.text_cells("label", title, row)
            rows.append(row)
        if "description" in self.form:
            row = {"type": "description"}
            self.text_cells("label", self.form["description"], row)
            rows.append(row)
        if "languages" in self.form:
            rows.append({"type": "languages", "name": " ".join(self.form["languages"])})
        for stage in self.form.get("stages", []):
            row = {"type": "stage", "name": stage["key"], "workflow_step": stage["workflowStep"]}
            self.text_cells("label", stage["title"], row)
            rows.append(row)
        for page in self.form.get("layout", {}).get("pages", []):
            row = {"type": "page", "name": page["key"], "sections": " ".join(page["sections"])}
            self.text_cells("label", page.get("title"), row)
            rows.append(row)
        return rows

    def workbook(self):
        book = openpyxl.Workbook(write_only=True)
        survey_columns = ["type", "name"] + self.text_columns("label") + self.text_columns("hint") + \
            ["required", "relevant", "constraint"] + self.text_columns("constraint_message") + \
            ["calculation", "choice_filter", "appearance", "read_only"] + \
            FIMRA_COLUMNS + \
            self.text_columns("fimra_item_label")
        has_counts = any(r.get("type") in ("image", "file") for r in self.survey)
        keep = {c for r in self.survey for c in r} | ({"fimra_max_count"} if has_counts else set())
        survey_columns = [c for c in survey_columns if c in keep or c in ("type", "name")]
        self.sheet(book, "survey", survey_columns, self.survey)
        choice_columns = ["list_name", "name"] + self.text_columns("label")
        choice_columns += sorted({c for r in self.choices for c in r} - set(choice_columns))
        self.sheet(book, "choices", choice_columns, self.choices)
        settings = {"form_title": self.form["title"]["en"], "form_id": self.form["id"],
                    "version": str(self.form["version"])}
        if self.multi:
            settings["default_language"] = language_header("en")
        self.sheet(book, "settings", list(settings), [settings])
        extra = self.fimra_rows()
        if extra:
            columns = ["type", "name", "workflow_step", "sections"] + self.text_columns("label")
            columns = [c for c in columns if c == "type" or any(c in r for r in extra)]
            self.sheet(book, FIMRA_SHEET, columns, extra)
            self.notes.append(ReportItem("Stages, pages, translated titles, the description and the list of "
                                         "languages are on this sheet. ODK tools ignore it, and Open FIMRA reads it "
                                         "back on import.", FIMRA_SHEET))
        out = io.BytesIO()
        book.save(out)
        return out.getvalue()

    def text_columns(self, base):
        if not self.multi:
            return [base]
        return [f"{base}::{language_header(code)}" for code in self.languages]

    @staticmethod
    def sheet(book, name, columns, rows):
        ws = book.create_sheet(name)
        ws.append(columns)
        for row in rows:
            ws.append([row.get(c) for c in columns])


_RESERVED_CHOICE_COLUMNS = {"list_name", "name", "label", "image", "audio", "video", "media"}


def _filter_column(parent):
    return f"{parent}_parent" if parent in _RESERVED_CHOICE_COLUMNS else parent


def export_xlsform(form, check=formcheck.check):
    """Write a valid form as an .xlsx XLSForm. Returns the workbook bytes and notes for the person exporting.

    Raises XLSFormError when the form is not valid.
    """
    problems, _ = check(form)
    if problems:
        raise XLSFormError("The form is not valid, so it was not exported: " + " ".join(problems))
    exporter = _Exporter(form)
    try:
        data = exporter.run()
    except ValueError as exc:
        raise XLSFormError(f"The form could not be written as an XLSForm: {exc}.") from exc
    return data, exporter.notes
