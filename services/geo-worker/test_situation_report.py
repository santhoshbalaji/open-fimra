import copy
import json
import os
import unittest
from io import BytesIO

from pypdf import PdfReader

import situation_report
from situation_report import duration, period_text, render

SUBJECT = "subject-that-must-not-appear"
OUTLINE = {"type": "Polygon", "coordinates": [[[86.20, 16.38], [86.32, 16.38], [86.32, 16.52], [86.20, 16.52],
                                                [86.20, 16.38]]]}
RANGE_1 = {"type": "Polygon", "coordinates": [[[86.20, 16.38], [86.26, 16.38], [86.26, 16.52], [86.20, 16.52],
                                                [86.20, 16.38]]]}
RANGE_2 = {"type": "MultiPolygon", "coordinates": [[[[86.26, 16.38], [86.32, 16.38], [86.32, 16.52],
                                                     [86.26, 16.52], [86.26, 16.38]]]]}
FOREST = {"type": "Polygon", "coordinates": [
    [[86.28, 16.40], [86.31, 16.40], [86.31, 16.44], [86.28, 16.44], [86.28, 16.40]],
    [[86.29, 16.41], [86.30, 16.41], [86.30, 16.42], [86.29, 16.42], [86.29, 16.41]]]}


def synthetic_report():
    """A report as the core API saves it, for the fictional Demo State."""
    milestones = [("alerted", "Alert raised", 1500), ("acknowledged", "Acknowledged", 600),
                  ("team_sent", "Team sent", 2400), ("reached_site", "Reached site", 7300),
                  ("controlled", "Controlled", None), ("extinguished", "Extinguished", None)]
    return {
        "workspace": {"slug": "demo", "name": "Demo State"},
        "unit": {"hierarchy": "territorial", "path": "ds.c1.d1", "code": "D1", "name": "Hill Division",
                 "level": "division", "levelLabel": "Division", "depth": 2},
        "scope": "Hill Division (Division)",
        "period": {"hours": None, "from": "2026-03-09", "to": "2026-03-15", "days": 7,
                   "start": "2026-03-08T18:30:00Z", "end": "2026-03-15T18:29:59.999999Z",
                   "timeZone": "Asia/Kolkata", "partial": False},
        "generatedAt": "2026-03-16T03:15:00Z",
        "preparedFor": [{"role": "division_officer", "levelLabel": "Division", "unitName": "Hill Division"}],
        "counts": {"detections": {"inside": 9, "near_edge": 2, "outside": 4}, "today": {},
                   "queues": {"unit": 6, "edge_review": 1}, "steps": {"new": 2, "first_report": 3, "closed": 2},
                   "active": 5, "overdue": 2, "pendingReports": 3,
                   "responseSeconds": {k: {"median": v, "p90": v and v * 2, "count": 7 if v else 0}
                                       for k, _, v in milestones}},
        "alerts": {"total": 7, "active": 5, "closed": 2},
        "steps": [{"step": "new", "label": "New", "terminal": False, "count": 2},
                  {"step": "first_report", "label": "First report", "terminal": False, "count": 3},
                  {"step": "closed", "label": "Closed", "terminal": True, "count": 2}],
        "responseTimes": [{"step": k, "label": name, "median": v, "p90": v and v * 2, "count": 7 if v else 0}
                          for k, name, v in milestones],
        "placeLevels": ["Range", "Beat"],
        "incidents": {"total": 2, "opened": 2, "active": 1, "closed": 1, "overdue": 1, "items": [
            {"id": "41", "step": "reached_site", "stepLabel": "Reached site", "terminal": False, "overdue": True,
             "firstObserved": "2026-03-10T08:10:00Z", "lastObserved": "2026-03-10T09:40:00Z", "alertCount": 3,
             "alertsInPeriod": 3, "openedInPeriod": True, "places": ["Range 2 › Beat 05"]},
            {"id": "44", "step": "closed", "stepLabel": "Closed", "terminal": True, "overdue": False,
             "firstObserved": "2026-03-12T07:00:00Z", "lastObserved": "2026-03-12T07:00:00Z", "alertCount": 1,
             "alertsInPeriod": 1, "openedInPeriod": True, "places": []}]},
        "fieldReports": {"submitted": 3, "approved": 1, "returned": 1, "awaitingReview": 1, "missing": 2,
                         "alertsMissing": 2, "forms": [{"form": "first-report", "title": "First report",
                                                        "submitted": 3, "approved": 1, "returned": 1,
                                                        "awaitingReview": 1, "missing": 2}]},
        "overdueAlerts": [{"id": "1207", "step": "first_report", "stepLabel": "First report",
                           "enteredAt": "2026-03-11T04:00:00Z", "limitSeconds": 86400, "overdueSeconds": 190000,
                           "forestClass": "inside", "observedAt": "2026-03-10T08:10:00Z",
                           "place": "Range 2 › Beat 05"}],
        "unitLevel": "Range",
        "units": [{"code": "R1", "name": "Range 1", "path": "ds.c1.d1.r1", "alerts": 2, "active": 1, "overdue": 0,
                   "pendingReports": 1, "formsMissing": 0},
                  {"code": "R2", "name": "Range 2", "path": "ds.c1.d1.r2", "alerts": 5, "active": 4, "overdue": 2,
                   "pendingReports": 2, "formsMissing": 2}],
        "map": {"bounds": [86.20, 16.38, 86.32, 16.52], "outline": OUTLINE,
                "units": [{"name": "Range 1", "geometry": RANGE_1}, {"name": "Range 2", "geometry": RANGE_2}],
                "forest": [FOREST],
                "alerts": [[86.295, 16.425, "inside", True], [86.296, 16.426, "inside", False],
                           [86.31, 16.45, "near_edge", True], [86.22, 16.50, "fixed_heat_source", False],
                           [86.23, 16.40, "outside", True]],
                "alertsShown": 5,
                "incidents": [{"id": "41", "active": True, "bounds": [86.295, 16.425, 86.296, 16.426]}]},
        "labels": {"forestClasses": {"inside": "Inside forest", "near_edge": "Near the forest edge",
                                     "outside": "Outside forest", "fixed_heat_source": "Suspected fixed heat source"},
                   "queues": {"unit": "Unit", "edge_review": "Edge review", "triage": "Triage"},
                   "roles": {"division_officer": "Division officer"}},
        "notes": ["Days run from midnight to midnight in India Standard Time."],
    }


def pdf_text(data):
    reader = PdfReader(BytesIO(data))
    return len(reader.pages), "\n".join(page.extract_text() for page in reader.pages)


class SituationReportTest(unittest.TestCase):
    def test_report_renders_with_its_numbers(self):
        data, pages = render(synthetic_report())
        self.assertTrue(data.startswith(b"%PDF-"))
        count, text = pdf_text(data)
        self.assertEqual(count, pages)
        flat = " ".join(text.split())
        for expected in ["Situation report", "Hill Division (Division) · Demo State",
                         "9 March 2026 to 15 March 2026 (7 days), India Standard Time",
                         "Division officer, Hill Division (Division)", "Inside forest", "Near the forest edge",
                         "7 alerts in the period: 5 active and 2 closed", "Past their time limit: 2",
                         "Response times", "Team sent", "40 min", "2 h 2 min", "No data",
                         "2 incidents with alerts in the period", "Range 2 › Beat 05", "Reached site (overdue)",
                         "Forms submitted for the period's alerts: 3", "Missing: 2, on 2 alerts",
                         "Alerts past their time limit", "1207", "2 days 4 h", "By range", "Range 1",
                         "How the numbers are worked out", "not an official Government of India system",
                         f"Page 1 of {pages}"]:
            self.assertIn(expected, flat)
        self.assertNotIn(SUBJECT, flat)

    def test_document_is_described(self):
        data, _ = render(synthetic_report())
        info = PdfReader(BytesIO(data)).metadata
        self.assertEqual(info.title, "Situation report: Hill Division (Division)")
        self.assertEqual(info.author, "Open FIMRA")

    def test_empty_report_renders(self):
        report = synthetic_report()
        report["map"] = {"bounds": None, "outline": None, "units": [], "forest": [], "alerts": [], "alertsShown": 0,
                         "incidents": []}
        report["counts"]["detections"] = {}
        report["alerts"] = {"total": 0, "active": 0, "closed": 0}
        report["steps"], report["overdueAlerts"], report["units"] = [], [], []
        report["incidents"] = {"total": 0, "opened": 0, "active": 0, "closed": 0, "overdue": 0, "items": []}
        report["fieldReports"]["forms"] = []
        report["period"] |= {"from": "2026-03-15", "to": "2026-03-15", "days": 1}
        _, text = pdf_text(render(report)[0])
        flat = " ".join(text.split())
        for expected in ["Sunday 15 March 2026", "No map shapes for this unit.", "No alerts in this period.",
                         "No alerts are past their time limit.", "No forms are assigned to the period's alerts."]:
            self.assertIn(expected, flat)

    def test_large_report_spans_pages(self):
        report = synthetic_report()
        report["map"]["alerts"] = [[86.2 + i % 100 * 0.0012, 16.38 + i // 100 * 0.0028, "inside", i % 3 > 0]
                                   for i in range(5000)]
        report["map"]["alertsShown"] = 5000
        report["alerts"]["total"] = 6200
        item = report["incidents"]["items"][0]
        report["incidents"]["items"] = [{**item, "id": str(i)} for i in range(200)]
        data, pages = render(report)
        count, text = pdf_text(data)
        self.assertGreater(pages, 3)
        self.assertEqual(count, pages)
        self.assertIn("It shows the latest 5000 of 6200 alerts.", " ".join(text.split()))

    def test_hours_period(self):
        period = {"hours": 24, "from": "2026-03-14", "to": "2026-03-15", "days": 2,
                  "start": "2026-03-14T06:00:00Z", "end": "2026-03-15T06:00:00Z", "partial": True}
        self.assertEqual(period_text(period),
                         "Past 24 hours: 14 Mar 2026, 11:30 to 15 Mar 2026, 11:30, India Standard Time")

    def test_durations(self):
        self.assertEqual([duration(v) for v in (None, 0, 59.4, 60, 3599, 3600, 7500, 172800, 190000)],
                         ["No data", "0 s", "59 s", "1 min", "1 h", "1 h", "2 h 5 min", "2 days", "2 days 4 h"])

    def test_bad_content_fails_cleanly(self):
        broken = copy.deepcopy(synthetic_report())
        del broken["counts"]

        class Conn:
            def __init__(self):
                self.saved = None

            def transaction(self):
                return self

            def __enter__(self):
                return self

            def __exit__(self, *exc):
                return False

            def execute(self, query, params=None):
                if query.lstrip().startswith("SELECT"):
                    return self
                self.saved = params
                return self

            def fetchone(self):
                return {"id": "report-1", "content": broken}

        conn = Conn()
        self.assertTrue(situation_report.process_next_situation_report(conn))
        self.assertEqual(conn.saved[0], "failed")
        self.assertEqual(conn.saved[3], situation_report.FAILED)

    @unittest.skipUnless(os.path.exists("/design-tokens.json"), "make geo-test mounts the design tokens")
    def test_colours_follow_the_design_tokens(self):
        with open("/design-tokens.json", encoding="utf-8") as f:
            tokens = json.load(f)["color"]
        for name, value in situation_report.TOKENS.items():
            if name == "forest":
                self.assertEqual(value, tokens["chart"]["light"][2])
            elif name == "active":
                self.assertEqual(value, tokens["fireAge"]["light"][0])
            else:
                self.assertEqual(value, tokens["light"][name])


if __name__ == "__main__":
    unittest.main()
