"""Draw situation reports as PDF files.

The core API works out each report with the requester's grants and saves it in
situation_reports, in the request's transaction. This module only draws that
saved report: it never reads alerts, detections or grants, so the file cannot
hold anything the requester may not see. The map is drawn from the unit and
forest shapes saved in the report, so no outside map service is needed.

ReportLab writes the PDF with embedded Noto fonts for Latin, Devanagari and Bengali.
"""

import json
import logging
import math
import re
from datetime import datetime, timedelta, timezone
from io import BytesIO
from pathlib import Path as FilePath

import psycopg
from reportlab.graphics.shapes import Circle, Drawing, Line, Path, Polygon, Rect, String
from reportlab.lib import colors
from reportlab.lib.enums import TA_RIGHT
from reportlab.lib.pagesizes import A4
from reportlab.lib.styles import ParagraphStyle
from reportlab.lib.units import mm
from reportlab.pdfbase import pdfmetrics
from reportlab.pdfbase.ttfonts import TTFont, shapeStr
from reportlab.pdfgen import canvas
from reportlab.platypus import (CondPageBreak, KeepTogether, Paragraph, SimpleDocTemplate, Spacer, Table,
                                TableStyle)

logger = logging.getLogger("geo-worker")

INDIA = timezone(timedelta(hours=5, minutes=30))
FAILED = "The worker could not draw this report. Make it again, and tell an administrator if it fails again."
INDEPENDENCE = ("Open FIMRA is independent open-source software. It is not an official Government of India system "
                "and is not affiliated with NASA, the Forest Survey of India or the National Remote Sensing Centre.")
PERSONAL_DATA = "This report contains no names, phone numbers or other personal details of staff."

# Light-theme colours from docs/design-system/tokens.json. test_situation_report
# checks them against that file.
TOKENS = {
    "text": "#141816", "textSecondary": "#56615b", "border": "#dde2de", "brand": "#1f5a70",
    "brandSoft": "#e2ebee", "card": "#ffffff",
}
C = {name: colors.HexColor(value) for name, value in TOKENS.items()}

ASSETS = FilePath(__file__).parent
with (ASSETS / "situation-report-symbols.json").open(encoding="utf-8") as file:
    SYMBOLS = json.load(file)
with (ASSETS / "tokens.json").open(encoding="utf-8") as file:
    map_tokens = json.load(file)["map"]
MAP_COLOURS = {key: colors.HexColor(value) for key, value in map_tokens["light"].items()}
AGE_COLOURS = [colors.HexColor(value) for value in map_tokens["age"]["light"]]
AGE_LABELS = [*map_tokens["age"]["steps"][:-1], "3 days or more"]

FONT = "NotoSans"
BOLD = "NotoSans-Bold"
for name, filename in ((FONT, "NotoSans-Regular"), (BOLD, BOLD),
                       ("NotoSansDevanagari", "NotoSansDevanagari-Regular"),
                       ("NotoSansBengali", "NotoSansBengali-Regular")):
    pdfmetrics.registerFont(TTFont(name, str(ASSETS / "fonts" / (filename + ".ttf"))))
pdfmetrics.registerFontFamily(FONT, normal=FONT, bold=BOLD)
SCRIPT_RUNS = re.compile(r"([\u0900-\u097f\ua8e0-\ua8ff]+|[\u0980-\u09ff]+)")

PAGE_W, PAGE_H = A4
MARGIN = 15 * mm
WIDTH = PAGE_W - 2 * MARGIN

BODY = ParagraphStyle("body", fontName=FONT, fontSize=9, leading=12, textColor=C["text"])
SMALL = ParagraphStyle("small", parent=BODY, fontSize=8, leading=10.5, textColor=C["textSecondary"])
NOTE = ParagraphStyle("note", parent=SMALL, spaceAfter=3)
TITLE = ParagraphStyle("title", parent=BODY, fontName=BOLD, fontSize=18, leading=22)
SUBTITLE = ParagraphStyle("subtitle", parent=BODY, fontName=BOLD, fontSize=12, leading=16)
HEADING = ParagraphStyle("heading", parent=BODY, fontName=BOLD, fontSize=11, leading=14,
                         spaceBefore=8, spaceAfter=4, textColor=C["brand"])
CELL = ParagraphStyle("cell", parent=BODY, fontSize=8.5, leading=10.5)
CELL_RIGHT = ParagraphStyle("cellRight", parent=CELL, alignment=TA_RIGHT)
FIGURE = ParagraphStyle("figure", parent=BODY, fontName=BOLD, fontSize=16, leading=19)

MILESTONE_HELP = {"alerted": "satellite observation to alert"}
SYMBOL_SCALE = 0.65


def font_runs(text, base=FONT):
    """Keep script clusters together and use the matching embedded font."""
    for index, run in enumerate(SCRIPT_RUNS.split(text)):
        if run:
            font = base
            if index % 2:
                font = "NotoSansBengali" if "\u0980" <= run[0] <= "\u09ff" else "NotoSansDevanagari"
            yield run, font


def report_paragraph(value, style, bold=False):
    """Escape ReportLab paragraph text and select the font for each script."""
    value = str(value)
    if SCRIPT_RUNS.search(value):
        style = ParagraphStyle(style.name + "Shaped", parent=style, shaping=True)
    parts = []
    for run, font in font_runs(value):
        text = run.replace("&", "&amp;").replace("<", "&lt;").replace(">", "&gt;")
        parts.append(text if font == FONT else f'<font name="{font}">{text}</font>')
    text = "".join(parts)
    return Paragraph(f"<b>{text}</b>" if bold else text, style)


def shaped_runs(text, size):
    for run, font in font_runs(text):
        shaped = run if font == FONT else shapeStr(run, font, size, force=True)
        yield shaped, font, pdfmetrics.stringWidth(shaped, font, size)


def when(value):
    """A time from the report, in India Standard Time."""
    if not value:
        return ""
    moment = datetime.fromisoformat(value.replace("Z", "+00:00")).astimezone(INDIA)
    return moment.strftime("%d %b %Y, %H:%M").lstrip("0")


def day(value):
    return datetime.strptime(value, "%Y-%m-%d").strftime("%d %B %Y").lstrip("0")


def duration(seconds):
    """A response time in plain words, such as 45 s, 12 min or 3 h 5 min."""
    if seconds is None:
        return "No data"
    seconds = max(0, float(seconds))
    if seconds < 60:
        return f"{math.floor(seconds + 0.5)} s"
    minutes = math.floor(seconds / 60 + 0.5)
    if minutes < 60:
        return f"{minutes} min"
    hours, minutes = divmod(minutes, 60)
    if hours < 48:
        return f"{hours} h {minutes} min" if minutes else f"{hours} h"
    days, hours = divmod(hours, 24)
    return f"{days} days {hours} h" if hours else f"{days} days"


def plural(count, one, many):
    return f"{count} {one if count == 1 else many}"


def short_period(period):
    """The period in a few words, for the page footer."""
    if period.get("hours"):
        return f"past {period['hours']} hours to {when(period['end'])}"
    first = datetime.strptime(period["from"], "%Y-%m-%d").strftime("%d %b %Y").lstrip("0")
    last = datetime.strptime(period["to"], "%Y-%m-%d").strftime("%d %b %Y").lstrip("0")
    return first if first == last else f"{first} to {last}"


def period_text(period):
    if period.get("hours"):
        text = f"Past {period['hours']} hours: {when(period['start'])} to {when(period['end'])}"
    elif period["from"] == period["to"]:
        text = datetime.strptime(period["from"], "%Y-%m-%d").strftime("%A ") + day(period["from"])
    else:
        text = f"{day(period['from'])} to {day(period['to'])} ({period['days']} days)"
    if period.get("partial") and not period.get("hours"):
        text += f", up to {when(period['end'])}"
    return text + ", India Standard Time"


def label(report, group, key):
    return report.get("labels", {}).get(group, {}).get(key, key.replace("_", " ").capitalize())


def table(header, rows, widths, numeric_from=1, empty="None in this period."):
    """A plain table with a heading row. Columns from numeric_from are right-aligned."""
    if not rows:
        return report_paragraph(empty, SMALL)
    data = [[report_paragraph(h, CELL_RIGHT if i >= numeric_from else CELL, bold=True) for i, h in enumerate(header)]]
    for row in rows:
        data.append([report_paragraph(v, CELL_RIGHT if i >= numeric_from else CELL) for i, v in enumerate(row)])
    t = Table(data, colWidths=[w * WIDTH for w in widths], repeatRows=1, hAlign="LEFT")
    t.setStyle(TableStyle([
        ("LINEBELOW", (0, 0), (-1, 0), 0.8, C["text"]),
        ("LINEBELOW", (0, 1), (-1, -1), 0.3, C["border"]),
        ("VALIGN", (0, 0), (-1, -1), "TOP"),
        ("TOPPADDING", (0, 0), (-1, -1), 2.5),
        ("BOTTOMPADDING", (0, 0), (-1, -1), 2.5),
        ("LEFTPADDING", (0, 0), (-1, -1), 3),
        ("RIGHTPADDING", (0, 0), (-1, -1), 3),
    ]))
    return t


def figures(report):
    """The key numbers across the top of the first page."""
    counts = report["counts"]
    incidents = report.get("incidents") or {}
    fields = report.get("fieldReports") or {}
    items = [
        ("Detections", sum(counts.get("detections", {}).values())),
        ("Alerts", report["alerts"]["total"]),
        ("Active alerts", report["alerts"]["active"]),
        ("Overdue alerts", counts.get("overdue", 0)),
        ("Incidents", incidents.get("total", 0)),
        ("Forms missing", fields.get("missing", 0)),
    ]
    cells = [[Paragraph(f"{value}", FIGURE) for _, value in items],
             [report_paragraph(name, SMALL) for name, _ in items]]
    t = Table(cells, colWidths=[WIDTH / len(items)] * len(items))
    t.setStyle(TableStyle([
        ("BACKGROUND", (0, 0), (-1, -1), C["brandSoft"]),
        ("LINEAFTER", (0, 0), (-2, -1), 2, C["card"]),
        ("TOPPADDING", (0, 0), (-1, 0), 6),
        ("BOTTOMPADDING", (0, -1), (-1, -1), 6),
        ("LEFTPADDING", (0, 0), (-1, -1), 6),
    ]))
    return t


class Projection:
    """Longitude and latitude to drawing points, with north up and true shape near the centre."""

    def __init__(self, bounds, width, height, pad=6):
        west, south, east, north = bounds
        self.west, self.south = west, south
        self.kx = math.cos(math.radians((south + north) / 2))
        span_x = max((east - west) * self.kx, 1e-6)
        span_y = max(north - south, 1e-6)
        self.scale = min((width - 2 * pad) / span_x, (height - 2 * pad) / span_y)
        self.dx = (width - span_x * self.scale) / 2
        self.dy = (height - span_y * self.scale) / 2

    def __call__(self, lon, lat):
        return self.dx + (lon - self.west) * self.kx * self.scale, self.dy + (lat - self.south) * self.scale


def polygons(geometry):
    if not geometry:
        return []
    if geometry["type"] == "Polygon":
        return [geometry["coordinates"]]
    if geometry["type"] == "MultiPolygon":
        return geometry["coordinates"]
    return []


def shape_path(geometry, project, **style):
    path = Path(fillMode=0, **style)  # Even-odd fill keeps holes open.
    for polygon in polygons(geometry):
        for ring in polygon:
            for i, (lon, lat) in enumerate(ring):
                x, y = project(lon, lat)
                if i == 0:
                    path.moveTo(x, y)
                else:
                    path.lineTo(x, y)
            path.closePath()
    return path


def report_age(observed, end):
    if not observed:
        return None
    hours = (datetime.fromisoformat(end) - datetime.fromisoformat(observed)).total_seconds() / 3600
    return next((i for i, limit in enumerate(SYMBOLS["ageHours"]) if hours < limit), len(SYMBOLS["ageHours"]))


def forest_key(value):
    return value if value in SYMBOLS["forestClasses"] else "outside"


def symbol(drawing, x, y, forest_class, active, age=None):
    """Draw the same mark in the map and its key. A slash marks closure."""
    mark = SYMBOLS["incident"] if forest_class == "incident" else SYMBOLS["forestClasses"][forest_key(forest_class)]
    radius = mark["radius"] * SYMBOL_SCALE
    palette = mark["palette"]
    color = AGE_COLOURS[age] if palette == "age" and age is not None else MAP_COLOURS.get(palette, MAP_COLOURS["neutralMark"])
    outline = MAP_COLOURS["ageOutline"]
    style = {"fillColor": color, "strokeColor": outline, "strokeWidth": 0.65}
    if mark["shape"] == "square":
        drawing.add(Rect(x - radius, y - radius, radius * 2, radius * 2, **style))
    elif mark["shape"] == "diamond":
        drawing.add(Polygon([x, y + radius, x + radius, y, x, y - radius, x - radius, y], **style))
    elif mark["shape"] == "ring":
        drawing.add(Circle(x, y, radius, fillColor=None, strokeColor=outline, strokeWidth=2.34))
        drawing.add(Circle(x, y, radius, fillColor=None, strokeColor=color, strokeWidth=1.3))
    else:
        drawing.add(Circle(x, y, radius, **style))
    if not active:
        slash = SYMBOLS["closedStroke"]
        sx, sy = slash["start"]
        ex, ey = slash["end"]
        line = (x + sx * radius, y - sy * radius, x + ex * radius, y - ey * radius)
        drawing.add(Line(*line, strokeColor=MAP_COLOURS["land"], strokeWidth=(slash["width"] + 1.6) * SYMBOL_SCALE))
        drawing.add(Line(*line, strokeColor=outline, strokeWidth=slash["width"] * SYMBOL_SCALE))


def map_legend(report, has_ages):
    classes = [(key, True, 0, label(report, "forestClasses", key)) for key in SYMBOLS["forestClasses"]]
    entries = classes + [("inside", True, 0, "Active alert or incident (no slash)"),
                         ("inside", False, 0, "Closed alert or incident (slash)"),
                         ("incident", True, None, "Incident"), ("forest", True, None, "Forest")]
    rows = [entries]
    if has_ages:
        rows.append([("inside", True, i, text) for i, text in enumerate(AGE_LABELS)])
    legend = Drawing(WIDTH, 0)
    x, y = 0, 0
    for row in rows:
        for key, active, age, text in row:
            space = 22 + pdfmetrics.stringWidth(text, FONT, 7.5)
            if x + space > WIDTH:
                x, y = 0, y - 13
            if key == "forest":
                legend.add(Rect(x, y - 2.6, 9, 5.2, fillColor=MAP_COLOURS["forest"],
                                fillOpacity=map_tokens["forestOpacity"], strokeColor=C["textSecondary"], strokeWidth=0.3))
            else:
                symbol(legend, x + 5, y, key, active, age)
            legend.add(String(x + 13, y - 2.5, text, fontName=FONT, fontSize=7.5, fillColor=C["text"]))
            x += space
        x, y = 0, y - 13
    legend.height = -y
    legend.shift(0, legend.height - 6.5)
    return legend


def map_rows(report):
    data = report["map"]
    classes = SYMBOLS["forestClasses"]
    tally = {key: [0, 0] for key in (*classes, "incident")}
    for _, _, key, active, *_ in data.get("alerts", []):
        tally[forest_key(key)][0 if active else 1] += 1
    for incident in data.get("incidents", []):
        tally["incident"][0 if incident["active"] else 1] += 1
    return [[label(report, "forestClasses", key) if key in classes else "Incidents", active, closed, active + closed]
            for key, (active, closed) in tally.items()]


def report_map(report, width=WIDTH, height=105 * mm):
    """The unit, the units below it, forest and the period's alerts and incidents."""
    data = report.get("map") or {}
    drawing = Drawing(width, height)
    drawing.add(Rect(0, 0, width, height, fillColor=MAP_COLOURS["canvas"], strokeColor=C["border"], strokeWidth=0.5))
    bounds = data.get("bounds")
    if not bounds:
        drawing.add(String(width / 2, height / 2, "No map shapes for this unit.", textAnchor="middle",
                           fontName=FONT, fontSize=9, fillColor=C["textSecondary"]))
        return drawing
    project = Projection(bounds, width, height)
    if data.get("outline"):
        drawing.add(shape_path(data["outline"], project, fillColor=C["card"], strokeColor=None))
    for forest in data.get("forest", []):
        drawing.add(shape_path(forest, project, fillColor=MAP_COLOURS["forest"], fillOpacity=map_tokens["forestOpacity"],
                               strokeColor=C["textSecondary"], strokeWidth=0.3))
    for unit in data.get("units", []):
        drawing.add(shape_path(unit["geometry"], project, fillColor=None, strokeColor=C["textSecondary"],
                               strokeWidth=0.6, strokeDashArray=[3, 2]))
    if data.get("outline"):
        drawing.add(shape_path(data["outline"], project, fillColor=None, strokeColor=C["text"], strokeWidth=1.4))
    labels = []

    def place_label(x, y, text, size, color, anchor="start"):
        # A label that would cover another is left out; the tables name everything.
        runs = list(shaped_runs(text, size))
        text_width = sum(advance for _, _, advance in runs)
        left = x - text_width / 2 if anchor == "middle" else x
        left = min(max(2, left), width - text_width - 2)
        y = min(max(2, y), height - size - 1)
        if left < 2:
            return
        box = (left - 1, y - 1, left + text_width + 1, y + size)
        if any(box[0] < b[2] and b[0] < box[2] and box[1] < b[3] and b[1] < box[3] for b in labels):
            return
        labels.append(box)
        for text_run, font, advance in runs:
            drawing.add(String(left, y, text_run, fontName=font, fontSize=size, fillColor=color))
            left += advance

    for lon, lat, forest_class, active, *observed in data.get("alerts", []):
        age = report_age(observed[0], report["period"]["end"]) if observed else None
        symbol(drawing, *project(lon, lat), forest_class, active, age)
    incident_mark = SYMBOLS["incident"]
    offset_x, offset_y = incident_mark["offset"]
    margin = incident_mark["radius"] * SYMBOL_SCALE + 1
    for incident in data.get("incidents", []):
        west, south, east, north = incident["bounds"]
        x, y = project((west + east) / 2, (south + north) / 2)
        x = min(max(x + offset_x * SYMBOL_SCALE, margin), width - margin)
        y = min(max(y - offset_y * SYMBOL_SCALE, margin), height - margin)
        symbol(drawing, x, y, "incident", incident["active"])
        place_label(x + 7, y + 4, f"Incident {incident['id']}", 6.5, C["text"])
    for unit in data.get("units", []):
        rings = polygons(unit["geometry"])
        if rings and rings[0]:
            ring = rings[0][0]
            lon = sum(p[0] for p in ring) / len(ring)
            lat = sum(p[1] for p in ring) / len(ring)
            place_label(*project(lon, lat), unit["name"], 7, C["textSecondary"], "middle")
    return drawing


def story(report):
    """The report's content, in reading order."""
    counts = report["counts"]
    heading = report["scope"]
    if not (report.get("unit") or {}).get("name") == report["workspace"]["name"]:
        heading += f" · {report['workspace']['name']}"
    parts = [Paragraph("Situation report", TITLE), report_paragraph(heading, SUBTITLE),
             report_paragraph(period_text(report["period"]), BODY)]
    made = f"Made {when(report['generatedAt'])}."
    grants = [f"{label(report, 'roles', g['role'])}, {g['unitName']} ({g['levelLabel']})"
              for g in report.get("preparedFor", [])]
    if grants:
        made += " Shows what is visible to: " + "; ".join(grants) + "."
    parts += [report_paragraph(made, SMALL), Spacer(1, 4 * mm), figures(report), Spacer(1, 4 * mm)]

    data = report["map"]
    if data.get("bounds"):
        has_ages = any(len(point) > 4 and point[4] for point in data.get("alerts", []))
        map_note = "The map shows alerts and incidents in the period, unit boundaries and forest."
        if has_ages:
            map_note += " Alert age is measured at the end of the period."
        shown = data.get("alertsShown", 0)
        total = report["alerts"]["total"]
        if shown < total:
            map_note += f" It shows the latest {shown} of {total} alerts."
        parts += [KeepTogether([Paragraph("Map", HEADING), report_map(report), Spacer(1, 1.5 * mm),
                                map_legend(report, has_ages), report_paragraph(map_note, SMALL),
                                Spacer(1, 1.5 * mm), Paragraph("Markers shown on the map", SMALL),
                                table(["Marker", "Active", "Closed", "Total"], map_rows(report), [0.55, 0.15, 0.15, 0.15])])]
    else:
        parts += [Paragraph("Map", HEADING), Paragraph("No map shapes for this unit.", SMALL)]

    classes = SYMBOLS["forestClasses"]
    detections = table(["Forest class", "Detections"],
                       [[label(report, "forestClasses", c), counts["detections"].get(c, 0)] for c in classes],
                       [0.3, 0.17])
    queues = table(["Queue", "Alerts"],
                   [[label(report, "queues", q), counts["queues"].get(q, 0)] for q in ("unit", "edge_review", "triage")],
                   [0.3, 0.17])
    side = Table([[detections, queues]], colWidths=[0.5 * WIDTH, 0.5 * WIDTH])
    side.setStyle(TableStyle([("VALIGN", (0, 0), (-1, -1), "TOP"), ("LEFTPADDING", (0, 0), (-1, -1), 0)]))
    parts += [Paragraph("Detections and alerts", HEADING), side, Spacer(1, 2 * mm),
              report_paragraph(f"{plural(report['alerts']['total'], 'alert', 'alerts')} in the period: "
                            f"{report['alerts']['active']} active and {report['alerts']['closed']} closed. "
                            f"Waiting for a report: {counts.get('pendingReports', 0)}. "
                            f"Past their time limit: {counts.get('overdue', 0)}.", BODY)]
    parts += [Paragraph("Alerts by current step", HEADING),
              table(["Step", "Alerts"], [[s["label"], s["count"]] for s in report.get("steps", [])], [0.4, 0.15],
                    empty="No alerts in this period.")]
    parts += [CondPageBreak(50 * mm), Paragraph("Response times", HEADING),
              table(["Step", "Median", "90th percentile", "Alerts that reached it"],
                    [[m["label"] + (f" ({MILESTONE_HELP[m['step']]})" if m["step"] in MILESTONE_HELP else ""),
                      duration(m.get("median")), duration(m.get("p90")), m.get("count", 0)]
                     for m in report.get("responseTimes", [])], [0.4, 0.17, 0.2, 0.23])]

    incidents = report.get("incidents") or {}
    parts += [CondPageBreak(40 * mm), Paragraph("Incidents", HEADING),
              report_paragraph(f"{plural(incidents.get('total', 0), 'incident', 'incidents')} with alerts in the period: "
                            f"{incidents.get('opened', 0)} opened in the period, {incidents.get('active', 0)} active, "
                            f"{incidents.get('closed', 0)} closed and {incidents.get('overdue', 0)} overdue.", BODY),
              Spacer(1, 1.5 * mm),
              table(["Incident", "Place", "Step", "First seen", "Last seen", "Alerts"],
                    [[i["id"], ", ".join(i.get("places") or []) or "No unit",
                      i["stepLabel"] + (" (overdue)" if i.get("overdue") else ""), when(i["firstObserved"]),
                      when(i["lastObserved"]), i["alertCount"]] for i in incidents.get("items", [])],
                    [0.1, 0.3, 0.16, 0.16, 0.16, 0.12], numeric_from=5)]

    fields = report.get("fieldReports") or {}
    parts += [CondPageBreak(40 * mm), Paragraph("Field reports", HEADING),
              report_paragraph(f"Forms submitted for the period's alerts: {fields.get('submitted', 0)}. "
                            f"Approved: {fields.get('approved', 0)}. Returned: {fields.get('returned', 0)}. "
                            f"Waiting for review: {fields.get('awaitingReview', 0)}. "
                            f"Missing: {fields.get('missing', 0)}, on "
                            f"{plural(fields.get('alertsMissing', 0), 'alert', 'alerts')}.", BODY),
              Spacer(1, 1.5 * mm),
              table(["Form", "Submitted", "Approved", "Returned", "Waiting for review", "Missing"],
                    [[f["title"], f["submitted"], f["approved"], f["returned"], f["awaitingReview"], f["missing"]]
                     for f in fields.get("forms", [])], [0.35, 0.13, 0.13, 0.13, 0.13, 0.13],
                    empty="No forms are assigned to the period's alerts.")]

    levels = " › ".join(report.get("placeLevels") or []) or "Place"
    parts += [CondPageBreak(40 * mm), Paragraph("Alerts past their time limit", HEADING),
              table(["Alert", levels, "Step", "Entered step", "Overdue by"],
                    [[a["id"], a.get("place") or "No unit", a["stepLabel"], when(a["enteredAt"]),
                      duration(a["overdueSeconds"])] for a in report.get("overdueAlerts", [])],
                    [0.1, 0.36, 0.18, 0.2, 0.16], numeric_from=4, empty="No alerts are past their time limit.")]

    units = report.get("units") or []
    if units:
        parts += [CondPageBreak(40 * mm), report_paragraph(f"By {(report.get('unitLevel') or 'unit').lower()}", HEADING),
                  table([report.get("unitLevel") or "Unit", "Alerts", "Active", "Overdue", "Waiting for a report",
                         "Forms missing"],
                        [[u["name"], u["alerts"], u["active"], u["overdue"], u["pendingReports"], u["formsMissing"]]
                         for u in units], [0.3, 0.12, 0.12, 0.12, 0.18, 0.16])]

    parts += [CondPageBreak(40 * mm), Paragraph("How the numbers are worked out", HEADING)]
    parts += [report_paragraph(note, NOTE) for note in report.get("notes", [])]
    parts += [Spacer(1, 3 * mm), report_paragraph(PERSONAL_DATA, SMALL), report_paragraph(INDEPENDENCE, SMALL)]
    return parts


def numbered_canvas(footer):
    """A canvas that writes the footer and 'Page n of m' once every page is known."""

    class NumberedCanvas(canvas.Canvas):
        def __init__(self, *args, **kwargs):
            super().__init__(*args, **kwargs)
            self.pages = []

        def showPage(self):
            self.pages.append(dict(self.__dict__))
            self._startPage()

        def save(self):
            total = len(self.pages)
            for state in self.pages:
                self.__dict__.update(state)
                self.setFillColor(C["textSecondary"])
                left = MARGIN
                for run, font, advance in shaped_runs(footer, 7.5):
                    self.setFont(font, 7.5)
                    self.drawString(left, 9 * mm, run)
                    left += advance
                self.setFont(FONT, 7.5)
                self.drawRightString(PAGE_W - MARGIN, 9 * mm, f"Page {self._pageNumber} of {total}")
                super().showPage()
            super().save()

    return NumberedCanvas


def render(report):
    """Returns the PDF bytes and page count for one saved report."""
    out = BytesIO()
    footer = f"Open FIMRA situation report · {report['scope']} · {short_period(report['period'])}"
    if len(footer) > 110:
        footer = footer[:107] + "..."
    doc = SimpleDocTemplate(out, pagesize=A4, leftMargin=MARGIN, rightMargin=MARGIN, topMargin=MARGIN,
                            bottomMargin=16 * mm, title=f"Situation report: {report['scope']}",
                            author="Open FIMRA", subject=period_text(report["period"]), creator="Open FIMRA",
                            invariant=1)
    doc.build(story(report), canvasmaker=numbered_canvas(footer))
    return out.getvalue(), doc.page


def save(conn, report_id, result):
    conn.execute("""
        UPDATE situation_reports SET status = %s, pdf = %s, page_count = %s, error_message = %s, finished_at = now()
        WHERE id = %s
    """, (result["status"], result.get("pdf"), result.get("page_count"), result.get("error_message"), report_id))


def process_next_situation_report(conn):
    with conn.transaction():
        job = conn.execute("""
            SELECT id, content FROM situation_reports
            WHERE status = 'pending' AND expires_at > now()
            ORDER BY created_at, id LIMIT 1 FOR UPDATE SKIP LOCKED
        """).fetchone()
        if job is None:
            return False
        try:
            pdf, pages = render(job["content"])
            result = {"status": "ready", "pdf": pdf, "page_count": pages}
        except (KeyError, TypeError, ValueError, ArithmeticError) as exc:
            logger.warning("Situation report %s could not be drawn: %s", job["id"], exc)
            result = {"status": "failed", "error_message": FAILED}
        try:
            with conn.transaction():
                save(conn, job["id"], result)
        except (psycopg.DataError, psycopg.IntegrityError) as exc:
            logger.warning("Situation report %s could not be saved: %s", job["id"], exc.diag.message_primary)
            result = {"status": "failed", "error_message": FAILED}
            save(conn, job["id"], result)
    logger.info("Situation report %s finished with status %s", job["id"], result["status"])
    return True
