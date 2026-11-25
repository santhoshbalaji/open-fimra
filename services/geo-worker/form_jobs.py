"""XLSForm conversion jobs from the form_conversions table.

The worker claims one pending row with FOR UPDATE SKIP LOCKED, converts it and
saves the result in the same transaction, like layer imports and exports.
"""

import logging

import psycopg
from psycopg.types.json import Jsonb

from xlsform import ReportItem, XLSFormError, export_xlsform, import_xlsform

logger = logging.getLogger("geo-worker")

FAILED = "The worker could not convert the form. Check the file and try again."


def convert(job):
    """Run one job. Returns the columns to save."""
    if job["direction"] == "import":
        result = import_xlsform(bytes(job["source_file"]), job["source_name"])
        report = [item.as_dict() for item in result.report]
        if result.problems:
            # The validator has the last word: a draft that fails it is never saved.
            report += [ReportItem(f"The converted form does not pass the form check: {p}").as_dict()
                       for p in result.problems]
            return {"status": "failed", "report": report,
                    "error_message": "The converted form does not pass the form check. The review report lists "
                                     "the problems."}
        return {"status": "ready", "draft_form": result.form, "report": report}
    data, notes = export_xlsform(job["source_form"])
    return {"status": "ready", "result_file": data, "report": [n.as_dict() for n in notes]}


def save(conn, job_id, result):
    conn.execute("""
        UPDATE form_conversions SET status = %s, draft_form = %s, result_file = %s, report = %s,
               error_message = %s, finished_at = now()
        WHERE id = %s
    """, (result["status"], Jsonb(result["draft_form"]) if result.get("draft_form") else None,
          result.get("result_file"), Jsonb(result["report"]), result.get("error_message"), job_id))


def process_next_form_conversion(conn):
    with conn.transaction():
        job = conn.execute("""
            SELECT id, direction, source_name, source_file, source_form FROM form_conversions
            WHERE status = 'pending' ORDER BY created_at, id LIMIT 1 FOR UPDATE SKIP LOCKED
        """).fetchone()
        if job is None:
            return False
        try:
            result = convert(job)
        except XLSFormError as exc:
            result = {"status": "failed", "report": [ReportItem(str(exc)).as_dict()], "error_message": str(exc)}
        except (RuntimeError, ValueError, KeyError, TypeError) as exc:
            logger.warning("Form conversion %s failed: %s", job["id"], exc)
            result = {"status": "failed", "report": [], "error_message": FAILED}
        try:
            with conn.transaction():
                save(conn, job["id"], result)
        except (psycopg.DataError, psycopg.IntegrityError) as exc:
            logger.warning("Form conversion %s could not be saved: %s", job["id"], exc.diag.message_primary)
            result = {"status": "failed", "report": [], "error_message": FAILED}
            save(conn, job["id"], result)
    logger.info("Form conversion %s (%s) finished with status %s and %d review items", job["id"], job["direction"],
                result["status"], len(result["report"]))
    return True
