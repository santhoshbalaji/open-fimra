"""Form conversion jobs on a migrated database. Run with `make xlsform-test-integration`.

Each test works inside a transaction that is rolled back, so it leaves the
database as it found it. All content is made up.
"""

import json
import os
import unittest

import psycopg
from psycopg.rows import dict_row
from psycopg.types.json import Jsonb

from form_jobs import process_next_form_conversion
from test_xlsform import EXAMPLES, equivalent, workbook
from xlsform import import_xlsform

ENABLED = os.environ.get("FIMRA_FORM_JOB_TEST") == "1"


@unittest.skipUnless(ENABLED, "set FIMRA_FORM_JOB_TEST=1 and a database; see make xlsform-test-integration")
class FormJobTests(unittest.TestCase):
    def setUp(self):
        self.conn = psycopg.connect(autocommit=True, row_factory=dict_row, connect_timeout=5)
        self.tx = self.conn.transaction(force_rollback=True)
        self.tx.__enter__()
        self.workspace = self.conn.execute("""
            INSERT INTO workspaces (slug, name) VALUES ('form-job-test', 'Demo State form job test') RETURNING id
        """).fetchone()["id"]

    def tearDown(self):
        self.tx.__exit__(None, None, None)
        self.conn.close()

    def queue(self, **columns):
        # An old timestamp puts the test's job first in the queue.
        names = ["workspace_id", "requested_by", "created_at", *columns]
        values = [self.workspace, "form-job-test-subject", "2000-01-01T00:00Z", *columns.values()]
        return self.conn.execute(
            f"INSERT INTO form_conversions ({', '.join(names)}) VALUES ({', '.join(['%s'] * len(names))}) RETURNING id",
            values).fetchone()["id"]

    def job(self, job_id):
        self.assertTrue(process_next_form_conversion(self.conn))
        return self.conn.execute("SELECT * FROM form_conversions WHERE id = %s", (job_id,)).fetchone()

    def test_export_then_import_gives_the_same_form(self):
        form = json.loads((EXAMPLES / "first-report.json").read_text())
        exported = self.job(self.queue(direction="export", source_form=Jsonb(form)))
        self.assertEqual(exported["status"], "ready", exported["error_message"])
        self.assertTrue(exported["report"])
        imported = self.job(self.queue(direction="import", source_name="first-report.xlsx",
                                       source_file=bytes(exported["result_file"])))
        self.assertEqual(imported["status"], "ready", imported["error_message"])
        self.assertEqual(imported["report"], [])
        self.assertEqual(equivalent(imported["draft_form"]), equivalent(form))
        self.assertIsNotNone(imported["finished_at"])

    def test_review_report_is_saved_with_the_draft(self):
        data = workbook({"survey": [["type", "name", "label", "default"],
                                    ["integer", "demo_count", "Demo count", "3"],
                                    ["note", "demo_note", "Demo note", None]]})
        done = self.job(self.queue(direction="import", source_name="demo-count.xlsx", source_file=data))
        self.assertEqual(done["status"], "ready")
        self.assertEqual(done["draft_form"]["id"], "demo-count")
        places = {(r["sheet"], r.get("row"), r.get("column")) for r in done["report"]}
        self.assertEqual(places, {("survey", 2, None), ("survey", 2, "default"), ("survey", 3, "type"),
                                  ("settings", None, "form_title")})
        self.assertEqual(done["draft_form"], import_xlsform(data, "demo-count.xlsx").form)

    def test_a_file_that_is_not_a_workbook_fails_with_a_reason(self):
        done = self.job(self.queue(direction="import", source_name="notes.xlsx", source_file=b"not a workbook"))
        self.assertEqual(done["status"], "failed")
        self.assertIn("not an Excel workbook", done["error_message"])
        self.assertIsNone(done["draft_form"])
        self.assertEqual(done["report"][0]["message"], done["error_message"])

    def test_an_invalid_form_is_not_exported(self):
        done = self.job(self.queue(direction="export", source_form=Jsonb({"formatVersion": 1, "id": "Bad"})))
        self.assertEqual(done["status"], "failed")
        self.assertIn("not valid", done["error_message"])
        self.assertIsNone(done["result_file"])
