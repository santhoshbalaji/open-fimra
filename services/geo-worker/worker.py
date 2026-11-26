"""Import, export, form conversion and situation report jobs from PostgreSQL. Each job and its result share a
transaction."""

import logging
import os
import time
from pathlib import Path

import psycopg
from psycopg.rows import dict_row
from psycopg.types.json import Jsonb
from shapely import from_wkb, to_wkb
from shapely.errors import ShapelyError

from checks import Parent, Review, hierarchy_paths, review_layer, unit_topology
from export import process_next_export
from form_jobs import process_next_form_conversion
from situation_report import process_next_situation_report
from source import ImportErrorWithProblem, Problem, read_source, square_metres

logger = logging.getLogger("geo-worker")


def error_cause(error):
    if isinstance(error, psycopg.Error) and error.sqlstate:
        # PostgreSQL error details can include whole rows and uploaded attributes.
        return f"{error.diag.message_primary} (SQLSTATE {error.sqlstate})"
    return str(error)


def read_parents(conn, job, depth):
    ref = job["import_settings"].get("parentVersionId")
    rows = conn.execute("""
        WITH draft AS (
            SELECT code, path, geom, depth FROM draft_units
            WHERE layer_version_id = %s AND path IS NOT NULL
        )
        SELECT code, path::text, ST_AsBinary(geom) AS geom FROM draft
        UNION ALL
        SELECT code, path::text, ST_AsBinary(geom) AS geom FROM units
        WHERE hierarchy_id = %s AND valid_to IS NULL
          AND hierarchy_parent_allowed(hierarchy_id, %s::smallint, depth)
          AND depth NOT IN (SELECT depth FROM draft)
        ORDER BY path
    """, (ref, job["hierarchy_id"], depth))
    return [Parent(row["code"], row["path"], from_wkb(row["geom"])) for row in rows]


def read_coverage(conn, job):
    ref = job["import_settings"].get("coverageVersionId")
    if ref:
        rows = conn.execute("""
            SELECT ST_AsBinary(geom) AS geom FROM draft_units WHERE layer_version_id = %s
        """, (ref,))
    else:
        rows = conn.execute("""
            WITH selected_level AS (
                SELECT h.id, u.depth FROM hierarchies h JOIN units u ON u.hierarchy_id = h.id
                WHERE h.workspace_id = %s AND h.kind = 'territorial'
                  AND u.valid_to IS NULL AND u.geom IS NOT NULL
                ORDER BY u.depth DESC, h.slug LIMIT 1
            )
            SELECT ST_AsBinary(u.geom) AS geom FROM units u JOIN selected_level s
              ON u.hierarchy_id = s.id AND u.depth = s.depth
            WHERE u.valid_to IS NULL AND u.geom IS NOT NULL
        """, (job["workspace_id"],))
    return [from_wkb(row["geom"]) for row in rows]


def save_import(conn, upload_dir, job, problems):
    version = job["layer_version_id"]
    role = job["role"]
    settings = job["import_settings"]
    crs, features = read_source(upload_dir / job["file_key"], role, settings, problems)
    with conn.cursor() as cursor:
        if role == "hierarchy":
            depth = settings["depth"]
            area_of = square_metres()
            parents = read_parents(conn, job, depth) if depth else []
            hierarchy_paths(features, depth, parents, problems, area_of)
            if depth:
                footprints = [(f"parent:{parent.path}", parent.geom) for parent in parents if parent.geom is not None]
            else:
                boundary = conn.execute("SELECT ST_AsBinary(boundary) AS geom FROM workspaces WHERE id = %s",
                                        (job["workspace_id"],)).fetchone()["geom"]
                footprints = [("workspace", from_wkb(boundary))] if boundary else []
            level = conn.execute("""
                SELECT optional FROM hierarchy_levels WHERE hierarchy_id = %s AND depth = %s
            """, (job["hierarchy_id"], depth)).fetchone()
            if level is None:
                raise ValueError("The selected hierarchy level no longer exists.")
            unit_topology(features, footprints, problems, area_of, require_coverage=not level["optional"])
            cursor.executemany("""
                INSERT INTO draft_units (layer_version_id, feature_number, depth, code, name, path,
                                         parent_code, parent_path, geom, area_m2)
                VALUES (%s, %s, %s, %s, %s, %s::ltree, %s, %s::ltree, ST_GeomFromWKB(%s, 4326), %s)
            """, ((version, f.number, depth, f.code, f.name, f.path, f.parent_code, f.parent_path,
                   to_wkb(f.geom), f.area) for f in features))
        else:
            coverage = read_coverage(conn, job) if role == "forest" else None
            review_layer(role, features, problems, coverage)
            cursor.executemany("""
                INSERT INTO layer_features (layer_version_id, source_feature_number, code, name, attributes, geom, area_m2)
                VALUES (%s, %s, %s, %s, %s, ST_GeomFromWKB(%s, 4326), %s)
            """, ((version, f.number, f.code, f.name, Jsonb(f.attributes), to_wkb(f.geom), f.area) for f in features))
    return crs


def process_next(conn, upload_dir):
    with conn.transaction():
        job = conn.execute("""
            SELECT j.layer_version_id, j.file_key, v.import_settings, l.role, l.hierarchy_id, l.workspace_id
            FROM geo_jobs j JOIN layer_versions v ON v.id = j.layer_version_id JOIN layers l ON l.id = v.layer_id
            WHERE j.status = 'pending' AND v.status = 'draft'
            ORDER BY j.created_at, j.layer_version_id LIMIT 1 FOR UPDATE OF j SKIP LOCKED
        """).fetchone()
        if job is None:
            return False
        version = job["layer_version_id"]
        status, crs, problems = "ready", None, Review()
        try:
            # Roll back partial writes on an input error while keeping the job lock.
            with conn.transaction():
                crs = save_import(conn, upload_dir, job, problems)
        except (ImportErrorWithProblem, RuntimeError, ShapelyError, ValueError, psycopg.DataError, psycopg.IntegrityError) as exc:
            status = "failed"
            logger.warning("Import %s failed: %s", version, error_cause(exc.__cause__ or exc))
            problems = [exc.problem] if isinstance(exc, ImportErrorWithProblem) else [
                Problem("import_failed", None, "The worker could not process the file. Check its contents and upload it again.", True)
            ]

        with conn.cursor() as cursor:
            cursor.executemany("""
                INSERT INTO layer_import_problems (layer_version_id, kind, feature_ref, message, blocks_publish)
                VALUES (%s, %s, %s, %s, %s)
            """, ((version, p.kind, p.feature_ref, p.message, p.blocks_publish) for p in problems))
        conn.execute("""
            UPDATE geo_jobs SET status = %s, source_crs = %s, finished_at = now() WHERE layer_version_id = %s
        """, (status, crs, version))
    logger.info("Import %s finished with status %s and %d review items", version, status, len(problems))
    return True


def main():
    logging.basicConfig(level=logging.INFO, format="%(levelname)s %(message)s")
    upload_dir = Path(os.environ.get("FIMRA_UPLOAD_DIR", "/uploads"))
    export_dir = Path(os.environ.get("FIMRA_EXPORT_DIR", "/exports"))
    while True:
        try:
            with psycopg.connect(autocommit=True, row_factory=dict_row, connect_timeout=5) as conn:
                while True:
                    imported = process_next(conn, upload_dir)
                    exported = process_next_export(conn, export_dir)
                    converted = process_next_form_conversion(conn)
                    reported = process_next_situation_report(conn)
                    if not (imported or exported or converted or reported):
                        time.sleep(0.5)
        except psycopg.Error as exc:
            logger.error("Database error in the geo worker: %s", error_cause(exc))
            time.sleep(3)


if __name__ == "__main__":
    main()
