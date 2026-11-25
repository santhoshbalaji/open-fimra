"""Write export files from the rows the API froze for each request.

The worker never decides which rows a user may see. The API saves the
permitted rows in export_rows when the user asks for the export, and the
database rejects any later change to that list. This module reads only those
rows, by their keys, and writes them to a file.
"""

import logging
import os
import shutil
import zipfile
from dataclasses import dataclass
from datetime import datetime, timezone
from pathlib import Path

import psycopg
from osgeo import gdal, ogr, osr

gdal.UseExceptions()
ogr.UseExceptions()
osr.UseExceptions()

logger = logging.getLogger("geo-worker")


def error_cause(error):
    if isinstance(error, psycopg.Error) and error.sqlstate:
        # PostgreSQL error details can include whole rows.
        return f"{error.diag.message_primary} (SQLSTATE {error.sqlstate})"
    return str(error)

CRS_TEXT = "WGS 84 (EPSG:4326). Longitude and latitude in decimal degrees."
INDEPENDENCE = ("Open FIMRA is independent open-source software. It is not an official Government of "
                "India system and is not affiliated with NASA, the Forest Survey of India or the National "
                "Remote Sensing Centre.")
PERSONAL_DATA = "This file contains no names, phone numbers or other personal details of staff."

# Each format: driver, data file name in the download, download extension,
# whether it stores date and time values natively, and whether it has geometry.
FORMATS = {
    "gpkg": ("GPKG", "{base}.gpkg", ".gpkg", True, True),
    "shapefile": ("ESRI Shapefile", "{base}.shp", ".zip", False, True),
    "kml": ("KML", "doc.kml", ".kmz", False, True),
    "geojson": ("GeoJSON", "{base}.geojson", ".zip", True, True),
    "flatgeobuf": ("FlatGeobuf", "{base}.fgb", ".zip", True, True),
    "csv_wkt": ("CSV", "{base}.csv", ".zip", False, True),
    "csv_lonlat": ("CSV", "{base}.csv", ".zip", False, False),
    "xlsx": ("XLSX", "{base}.xlsx", ".xlsx", False, False),
}

LAYER_OPTIONS = {
    "gpkg": ["FID=fid"],
    "shapefile": ["ENCODING=UTF-8", "RESIZE=YES"],
    "geojson": ["RFC7946=YES", "WRITE_NAME=NO"],
    # A spatial index would reorder the rows. Keep them newest first, as in other formats.
    "flatgeobuf": ["SPATIAL_INDEX=NO"],
    "csv_wkt": ["GEOMETRY=AS_WKT", "CREATE_CSVT=YES"],
    "csv_lonlat": ["CREATE_CSVT=YES"],
}

OGR_TYPES = {"int64": ogr.OFTInteger64, "real": ogr.OFTReal, "string": ogr.OFTString, "datetime": ogr.OFTDateTime}


@dataclass(frozen=True)
class Field:
    name: str
    short: str
    kind: str
    description: str


@dataclass(frozen=True)
class Level:
    hierarchy: str
    level: str
    label: str
    prefix: str


def _field(name, short, kind, description):
    return Field(name, short, kind, description)


DETECTION_FIELDS = [
    _field("detection_id", "det_id", "int64", "Open FIMRA detection ID."),
    _field("observed_at", "obs_time", "datetime", "When the satellite saw the fire, in UTC (ISO 8601)."),
    _field("source", "source", "string", "Where the record came from, such as firms."),
    _field("product", "product", "string", "The source's product, such as VIIRS_SNPP_NRT."),
    _field("satellite", "satellite", "string", "The satellite, as the source names it."),
    _field("forest_class", "forest_cls", "string", "inside, near_edge, outside or fixed_heat_source."),
    _field("triage_reason", "triage", "string",
           "Why the point needs review: shared_boundary, overlapping_units or no_unit. Empty when it does not."),
]
ALERT_FIELDS = [
    _field("alert_id", "alert_id", "int64", "Open FIMRA alert ID."),
    _field("queue", "queue", "string", "The alert queue: unit, edge_review or triage."),
    _field("alert_created_at", "alert_time", "datetime", "When the alert was created, in UTC (ISO 8601)."),
    _field("step", "step", "string", "The alert's current workflow step, such as new or acknowledged."),
    _field("step_label", "step_label", "string", "The current workflow step as the workflow names it."),
    _field("step_entered_at", "step_time", "datetime", "When the alert reached its current step, in UTC (ISO 8601)."),
    _field("incident_id", "incident", "int64",
           "The incident the alert belongs to. Empty for alerts created before incidents."),
]
TAIL_FIELDS = [
    _field("forest_feature_code", "forest_cd", "string",
           "Code of the forest feature the point is in or nearest to. Empty when the forest has no code."),
    _field("frp_mw", "frp_mw", "real", "Fire radiative power in megawatts."),
    _field("confidence", "confidence", "string", "Confidence as the source gives it: l, n or h for VIIRS, 0 to 100 for MODIS."),
    _field("longitude", "longitude", "real", "Longitude in WGS 84 decimal degrees."),
    _field("latitude", "latitude", "real", "Latitude in WGS 84 decimal degrees."),
]


def short_name(name, taken):
    """Shapefile field names have at most 10 characters. Hierarchy fields keep
    the start of the level name and end in _cd (code) or _nm (name)."""
    stem, _, part = name.rpartition("_")
    ending = {"code": "_cd", "name": "_nm"}[part]
    candidate = stem[:7] + ending
    number = 2
    while candidate in taken:
        tag = str(number)
        candidate = stem[:7 - len(tag)] + tag + ending
        number += 1
    taken.add(candidate)
    return candidate


def export_fields(kind, levels):
    fields = DETECTION_FIELDS[:1] + (ALERT_FIELDS if kind == "alerts" else []) + DETECTION_FIELDS[1:]
    taken = {f.short for f in fields + TAIL_FIELDS}
    for level in levels:
        for part, word in (("code", "code"), ("name", "name")):
            name = f"{level.prefix}{level.level}_{part}"
            fields.append(Field(name, short_name(name, taken), "string",
                                f"{level.label} {word} when the detection was matched ({level.hierarchy} hierarchy)."))
    return fields + TAIL_FIELDS


def iso(value):
    return value.astimezone(timezone.utc).strftime("%Y-%m-%dT%H:%M:%SZ") if value else None


def row_values(row, levels):
    """Flattens one exported row into plain values by field name."""
    values = dict(row)
    snapshot = row.get("hierarchy") or {}
    for level in levels:
        unit = (snapshot.get(level.hierarchy) or {}).get(level.level) or {}
        values[f"{level.prefix}{level.level}_code"] = unit.get("code")
        values[f"{level.prefix}{level.level}_name"] = unit.get("name")
    return values


def note_items(meta, fields, rows, generated_at):
    """The export note as (key, value) pairs: source, filters, coordinate
    system, time, row count and role. It names roles and units, never people."""
    filters = meta["filters"]
    sources = sorted({f"{r['source']} ({r['product']})" for r in rows})
    roles = "; ".join(f"{r['role']} at {r['level']} {r['unitCode']} ({r['unitName']})"
                      for r in sorted(meta["requester_roles"] or [], key=lambda r: (r["role"], r["unitCode"])))
    items = [
        ("title", f"Open FIMRA {meta['kind']} export"),
        ("workspace", f"{meta['workspace_name']} ({meta['workspace_slug']})"),
        ("source", "Satellite fire detections matched by Open FIMRA to the workspace's forest and unit layers. "
                   f"Sources in this file: {', '.join(sources) if sources else 'none'}."),
        ("time_window", f"Observed from {filters['from']} to {filters['to']} "
                        f"(the past {filters['hours']} hours when the export was requested)."),
        ("forest_classes", ", ".join(filters["forestClasses"])),
    ]
    if meta["kind"] == "alerts":
        items += [("queue", filters.get("queue") or "all queues the role may see"),
                  ("workflow_step", filters.get("step") or "any")]
    items += [
        ("unit_path", filters.get("unitPath") or "all units the role may see"),
        ("coordinate_system", CRS_TEXT),
        ("requested_at", iso(meta["created_at"])),
        ("generated_at", iso(generated_at)),
        ("row_count", str(len(rows))),
    ]
    if len(rows) != meta["row_count"]:
        items.append(("rows_removed", f"{meta['row_count'] - len(rows)} of the {meta['row_count']} rows saved "
                                      "at request time were deleted before the file was written."))
    items += [
        ("requested_by_role", roles or "none"),
        ("access", "Only rows the requesting role could see when the export was requested. "
                   "Values such as the workflow step are as they were when the file was written."),
        ("personal_data", PERSONAL_DATA),
        ("independence", INDEPENDENCE),
    ]
    return items


def readme_text(items, fields):
    lines = ["Open FIMRA export note", "======================", ""]
    lines += [f"{key.replace('_', ' ').capitalize()}: {value}" for key, value in items]
    lines += ["", "Fields", "------", "Name (Shapefile name, type): description", ""]
    for f in fields:
        kind = {"int64": "integer", "real": "decimal", "string": "text", "datetime": "date and time"}[f.kind]
        lines.append(f"{f.name} ({f.short}, {kind}): {f.description}")
    lines += ["", "Shapefile field names are limited to 10 characters, so the Shapefile uses the short names above.",
              "Date and time fields are UTC text in ISO 8601 form, such as 2026-01-31T09:45:00Z, in formats "
              "without a date and time type.", ""]
    return "\n".join(lines)


def placemark_name(values):
    if values.get("alert_id") is not None:
        return f"Alert {values['alert_id']}"
    return f"Detection {values['detection_id']}"


def placemark_description(values):
    place = values.get("beat_name") or "unknown beat"
    return f"{values['forest_class']}, {place}, observed {iso(values['observed_at'])}"


def spatial_reference():
    srs = osr.SpatialReference()
    srs.ImportFromEPSG(4326)
    srs.SetAxisMappingStrategy(osr.OAMS_TRADITIONAL_GIS_ORDER)
    return srs


def _set(feature, index, kind, value, native_time):
    if value is None:
        feature.SetFieldNull(index)
    elif kind == "datetime" and native_time:
        utc = value.astimezone(timezone.utc)
        feature.SetField(index, utc.year, utc.month, utc.day, utc.hour, utc.minute, utc.second, 100)
    elif kind == "datetime":
        feature.SetField(index, iso(value))
    elif kind == "int64":
        feature.SetField(index, int(value))
    elif kind == "real":
        feature.SetField(index, float(value))
    else:
        feature.SetField(index, str(value))


def _write_table(data, name, columns, records):
    """Writes a table without geometry. The first column numbers the rows,
    which also lets spreadsheet readers tell the header row from the data."""
    layer = data.CreateLayer(name, None, ogr.wkbNone)
    layer.CreateField(ogr.FieldDefn("position", ogr.OFTInteger))
    for column in columns:
        layer.CreateField(ogr.FieldDefn(column, ogr.OFTString))
    for position, record in enumerate(records, 1):
        feature = ogr.Feature(layer.GetLayerDefn())
        feature.SetField(0, position)
        for index, value in enumerate(record, 1):
            feature.SetField(index, value)
        layer.CreateFeature(feature)


def write_data(fmt, path, layer_name, fields, rows, levels, items):
    """Writes the data file in one format and returns the number of features."""
    driver, _, _, native_time, has_geometry = FORMATS[fmt]
    short = fmt == "shapefile"
    data = ogr.GetDriverByName(driver).CreateDataSource(str(path))
    try:
        options = list(LAYER_OPTIONS.get(fmt, []))
        if fmt == "gpkg":
            options += [f"DESCRIPTION={dict(items)['title']}: {dict(items)['time_window']}"]
        geometry = ogr.wkbPoint if has_geometry else ogr.wkbNone
        layer = data.CreateLayer(layer_name, spatial_reference() if has_geometry else None, geometry, options)
        for f in fields:
            kind = f.kind if native_time or f.kind != "datetime" else "string"
            definition = ogr.FieldDefn(f.short if short else f.name, OGR_TYPES[kind])
            if kind == "string" and short:
                definition.SetWidth(254)
            layer.CreateField(definition)
        if fmt == "gpkg":
            layer.SetMetadata({key.upper(): value for key, value in items})
        definition = layer.GetLayerDefn()
        # Look fields up by name: some drivers add their own, such as KML's Name.
        indexes = [definition.GetFieldIndex(f.short if short else f.name) for f in fields]
        count = 0
        for row in rows:
            values = row_values(row, levels)
            feature = ogr.Feature(definition)
            for index, f in zip(indexes, fields):
                _set(feature, index, f.kind, values.get(f.name), native_time)
            if fmt == "kml":
                feature.SetField("Name", placemark_name(values))
                feature.SetField("Description", placemark_description(values))
            if has_geometry:
                point = ogr.Geometry(ogr.wkbPoint)
                point.AddPoint_2D(float(values["longitude"]), float(values["latitude"]))
                feature.SetGeometry(point)
            layer.CreateFeature(feature)
            count += 1
        if fmt in ("gpkg", "xlsx"):
            # The note lives inside the file as two plain tables.
            _write_table(data, "export_note", ["key", "value"], items)
            _write_table(data, "export_fields", ["field", "shapefile_field", "field_type", "description"],
                         [(f.name, f.short, f.kind, f.description) for f in fields])
    finally:
        data.Close()
    return count


def write_export(work_dir, final_path, meta, levels, rows, generated_at):
    """Writes the download for one export into final_path, through work_dir.
    Returns the number of features written."""
    fmt = meta["format"]
    _, data_name, extension, _, _ = FORMATS[fmt]
    if final_path.suffix != extension:
        raise ValueError(f"{fmt} exports end in {extension}, not {final_path.suffix}")
    fields = export_fields(meta["kind"], levels)
    items = note_items(meta, fields, rows, generated_at)
    # The data file is named after its layer, so every format shows the same layer name.
    base = meta["kind"]
    work_dir.mkdir(parents=True)
    data_path = work_dir / data_name.format(base=base)
    count = write_data(fmt, data_path, meta["kind"], fields, rows, levels, items)
    if extension in (".zip", ".kmz"):
        packed = work_dir / f"packed{extension}"
        with zipfile.ZipFile(packed, "w", zipfile.ZIP_DEFLATED) as archive:
            # KMZ readers expect the KML first.
            for part in sorted(work_dir.glob(f"{data_path.stem}.*")):
                archive.write(part, part.name)
            archive.writestr("README.txt", readme_text(items, fields))
        os.replace(packed, final_path)
    else:
        os.replace(data_path, final_path)
    return count


ROWS_SQL = """
    SELECT r.ordinal, r.detection_id, r.alert_id, a.queue, a.created_at AS alert_created_at,
           r.observed_at, d.source, d.product, d.satellite,
           -- Alerts keep the class, reason and units they were routed with.
           COALESCE(a.forest_class, m.forest_class) AS forest_class,
           CASE WHEN a.id IS NULL THEN m.triage_reason
                ELSE COALESCE(a.triage_reason, CASE WHEN a.queue = 'triage' THEN 'no_unit' END) END AS triage_reason,
           COALESCE(a.hierarchy, m.hierarchy) AS hierarchy, f.code AS forest_feature_code,
           d.frp_mw, d.confidence, ST_X(d.geom) AS longitude, ST_Y(d.geom) AS latitude,
           i.current_step AS step, s.label AS step_label, i.entered_at AS step_entered_at,
           im.incident_id
    FROM export_rows r
    JOIN detection_matches m ON (m.workspace_id, m.detection_id, m.observed_at)
                              = (r.workspace_id, r.detection_id, r.observed_at)
    JOIN detections d ON (d.id, d.observed_at) = (r.detection_id, r.observed_at)
    LEFT JOIN alerts a ON a.id = r.alert_id
    LEFT JOIN layer_features f ON f.id = m.forest_feature_id
    LEFT JOIN workflow_instances i ON i.alert_id = r.alert_id
    LEFT JOIN workflow_steps s ON (s.template_id, s.slug) = (i.template_id, i.current_step)
    LEFT JOIN incident_members im ON im.alert_id = r.alert_id
    WHERE r.export_id = %s
    ORDER BY r.ordinal
"""


def read_levels(conn, workspace_id, rows):
    """Every level of the main territorial hierarchy, plus the levels of any
    other hierarchy that has a value in the rows. Other hierarchies' fields
    start with the hierarchy name."""
    used = {name for row in rows for name, units in (row["hierarchy"] or {}).items() if units}
    found = conn.execute("""
        SELECT h.slug AS hierarchy, l.slug AS level, l.labels->>'en' AS label
        FROM hierarchies h JOIN hierarchy_levels l ON l.hierarchy_id = h.id
        WHERE h.workspace_id = %s
        ORDER BY (h.kind = 'territorial') DESC, h.created_at, h.slug, l.depth
    """, (workspace_id,)).fetchall()
    main = found[0]["hierarchy"] if found else None
    return [Level(r["hierarchy"], r["level"], r["label"],
                  "" if r["hierarchy"] == main else r["hierarchy"].replace("-", "_") + "_")
            for r in found if r["hierarchy"] == main or r["hierarchy"] in used]


def process_next_export(conn, export_dir):
    with conn.transaction():
        job = conn.execute("""
            SELECT e.id, e.workspace_id, e.kind, e.format, e.filters, e.requester_roles, e.row_count,
                   e.created_at, w.slug AS workspace_slug, w.name AS workspace_name
            FROM exports e JOIN workspaces w ON w.id = e.workspace_id
            WHERE e.status = 'pending' AND e.expires_at > now()
            ORDER BY e.created_at, e.id LIMIT 1 FOR UPDATE OF e SKIP LOCKED
        """).fetchone()
        if job is None:
            return False
        export_id = str(job["id"])
        final_path = export_dir / f"{export_id}{FORMATS[job['format']][2]}"
        work_dir = export_dir / f"{export_id}.part"
        try:
            # A savepoint keeps the job lock if saving the result fails.
            with conn.transaction():
                rows = conn.execute(ROWS_SQL, (job["id"],)).fetchall()
                levels = read_levels(conn, job["workspace_id"], rows)
                count = write_export(work_dir, final_path, job, levels, rows, datetime.now(timezone.utc))
                conn.execute("""
                    UPDATE exports SET status = 'ready', file_key = %s, file_bytes = %s, feature_count = %s,
                           finished_at = now() WHERE id = %s
                """, (final_path.name, final_path.stat().st_size, count, job["id"]))
            logger.info("Export %s finished with %d rows in %s", export_id, count, job["format"])
        except (RuntimeError, OSError, ValueError, KeyError, psycopg.DataError, psycopg.IntegrityError) as exc:
            logger.warning("Export %s failed: %s", export_id, error_cause(exc))
            final_path.unlink(missing_ok=True)
            conn.execute("""
                UPDATE exports SET status = 'failed', finished_at = now(),
                       error_message = 'The worker could not write the file. Request the export again.'
                WHERE id = %s
            """, (job["id"],))
        finally:
            shutil.rmtree(work_dir, ignore_errors=True)
    return True
