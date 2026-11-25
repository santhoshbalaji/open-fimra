import tempfile
import unittest
import zipfile
from datetime import datetime, timezone
from pathlib import Path

from osgeo import gdal

from export import FORMATS, Level, export_fields, readme_text, note_items, short_name, write_export
from readback import describe, open_path

LEVELS = [Level("territorial", slug, slug.title(), "") for slug in ("state", "circle", "division", "range", "beat")]
SEEN = datetime(2026, 1, 7, 9, 45, tzinfo=timezone.utc)
SUBJECT = "subject-that-must-not-appear"


def meta(kind, fmt):
    filters = {"hours": 168, "from": "2025-12-31T09:45:00Z", "to": "2026-01-07T09:45:00Z",
               "forestClasses": ["inside", "near_edge"], "queue": None, "unitPath": "ds.c1.d1", "step": None}
    return {"kind": kind, "format": fmt, "workspace_slug": "demo", "workspace_name": "Demo State",
            "filters": filters, "requested_by": SUBJECT, "row_count": 2, "created_at": SEEN,
            "requester_roles": [{"role": "division_officer", "level": "division",
                                 "unitCode": "D1", "unitName": "Hill Division"}]}


def rows(kind):
    # IDs above the 32-bit range keep their 64-bit type in formats that guess types.
    full = {"detection_id": 12_345_678_901, "observed_at": SEEN, "source": "demo", "product": "VIIRS_SNPP_NRT",
            "satellite": "N", "forest_class": "inside", "triage_reason": None, "forest_feature_code": "F07",
            "hierarchy": {"territorial": {"state": {"code": "DS", "name": "Demo State"},
                                          "circle": {"code": "C1", "name": "North Circle"},
                                          "division": {"code": "D1", "name": "Hill Division"},
                                          "range": {"code": "R1", "name": "Range 1"},
                                          "beat": {"code": "B01", "name": "Beat 01"}}},
            "frp_mw": 3.25, "confidence": "n", "longitude": 86.457995, "latitude": 16.418498}
    empty = {**full, "detection_id": 12_345_678_902, "forest_class": "near_edge", "triage_reason": "no_unit",
             "satellite": None, "forest_feature_code": None, "hierarchy": {"territorial": {}}, "frp_mw": None,
             "confidence": None, "longitude": 86.5, "latitude": 16.5}
    if kind == "alerts":
        full |= {"alert_id": 23_456_789_012, "queue": "unit", "alert_created_at": SEEN,
                 "step": "acknowledged", "step_label": "Acknowledged", "step_entered_at": SEEN}
        empty |= {"alert_id": 23_456_789_013, "queue": "triage", "alert_created_at": SEEN,
                  "step": "new", "step_label": "New", "step_entered_at": SEEN}
    return [full, empty]


# What OGR reports when it reads each field type back, per format.
NATIVE_TIME = {"gpkg", "geojson", "flatgeobuf"}
GEOMETRY = {"gpkg": "Point", "shapefile": "Point", "kml": "Point", "geojson": "Point", "flatgeobuf": "Point",
            "csv_wkt": "Unknown (any), rows hold Point", "csv_lonlat": "Point", "xlsx": "None"}
NO_CRS = {"csv_lonlat", "xlsx"}


def expected_type(fmt, kind):
    if kind == "datetime":
        return "DateTime" if fmt in NATIVE_TIME else "String"
    return {"int64": "Integer64", "real": "Real", "string": "String"}[kind]


class ExportFormatTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)

    def export(self, kind, fmt):
        path = self.directory / f"{kind}-{fmt}{FORMATS[fmt][2]}"
        count = write_export(self.directory / f"{kind}-{fmt}.part", path, meta(kind, fmt), LEVELS, rows(kind), SEEN)
        self.assertEqual(count, 2)
        return path

    def test_every_format_opens_with_the_expected_layer_and_fields(self):
        for kind in ("detections", "alerts"):
            fields = export_fields(kind, LEVELS)
            for fmt in FORMATS:
                with self.subTest(kind=kind, format=fmt):
                    info = describe(self.export(kind, fmt))
                    [layer] = [layer for layer in info["layers"] if layer["name"] == kind]
                    self.assertEqual(layer["geometry"], GEOMETRY[fmt])
                    self.assertEqual(layer["crs"], None if fmt in NO_CRS else "EPSG:4326")
                    self.assertEqual(layer["count"], 2)
                    want = [(f.short if fmt == "shapefile" else f.name, expected_type(fmt, f.kind)) for f in fields]
                    if fmt == "csv_wkt":
                        want = [("WKT", "String")] + want
                    if fmt == "kml":
                        # This GDAL build has no LIBKML driver, so OGR reads the
                        # placemark name and description, and the schema lists the rest.
                        self.assertEqual(layer["fields"], [("Name", "String"), ("Description", "String")])
                        types = {"int64": "string", "datetime": "string", "string": "string", "real": "float"}
                        self.assertEqual(info["kml_schema"], [(f.name, types[f.kind]) for f in fields])
                    else:
                        self.assertEqual(layer["fields"], want)
                    if fmt in ("gpkg", "xlsx"):
                        names = {layer["name"] for layer in info["layers"]}
                        self.assertEqual(names, {kind, "export_note", "export_fields"})
                    else:
                        self.assertIn("README.txt", info["members"])

    def test_values_survive_each_format(self):
        for fmt in FORMATS:
            with self.subTest(format=fmt):
                path, options, _ = open_path(self.export("alerts", fmt))
                data = gdal.OpenEx(path, gdal.OF_VECTOR, open_options=options)
                layer = data.GetLayerByName("alerts") or data.GetLayer(0)
                first, second = list(layer)
                values = first.items()
                if fmt == "kml":
                    self.assertEqual(values, {"Name": "Alert 23456789012",
                                              "Description": "inside, Beat 01, observed 2026-01-07T09:45:00Z"})
                    self.assertEqual(first.GetGeometryRef().GetX(), 86.457995)
                    continue
                short = fmt == "shapefile"
                self.assertEqual(values["det_id" if short else "detection_id"], 12_345_678_901)
                self.assertEqual(values["divisio_cd" if short else "division_code"], "D1")
                self.assertEqual(values["divisio_nm" if short else "division_name"], "Hill Division")
                self.assertEqual(values["step_label"], "Acknowledged")
                observed = values["obs_time" if short else "observed_at"]
                self.assertIn(observed, ("2026-01-07T09:45:00Z", "2026/01/07 09:45:00+00"))
                self.assertAlmostEqual(values["longitude"], 86.457995, places=6)
                self.assertAlmostEqual(values["frp_mw"], 3.25)
                empty = second.items()
                # CSV has no empty value apart from empty text.
                blank = "" if fmt.startswith("csv") else None
                self.assertEqual(empty["satellite"], blank)
                self.assertEqual(empty["divisio_cd" if short else "division_code"], blank)
                self.assertEqual(empty["triage" if short else "triage_reason"], "no_unit")
                if fmt not in ("xlsx", "csv_lonlat"):
                    self.assertEqual(second.GetGeometryRef().GetY(), 16.5)

    def test_note_names_filters_and_role_but_no_person(self):
        for fmt in FORMATS:
            with self.subTest(format=fmt):
                path = self.export("alerts", fmt)
                if path.suffix in (".zip", ".kmz"):
                    with zipfile.ZipFile(path) as archive:
                        note = archive.read("README.txt").decode()
                        content = b"".join(archive.read(name) for name in archive.namelist())
                    for text in ("Unit path: ds.c1.d1", "Forest classes: inside, near_edge",
                                 "Coordinate system: WGS 84 (EPSG:4326)", "Row count: 2",
                                 "Requested by role: division_officer at division D1 (Hill Division)",
                                 "Time window: Observed from 2025-12-31T09:45:00Z to 2026-01-07T09:45:00Z",
                                 "Generated at: 2026-01-07T09:45:00Z", "Sources in this file: demo (VIIRS_SNPP_NRT)",
                                 "divisio_cd", "not an official Government of India system"):
                        self.assertIn(text, note)
                else:
                    content = path.read_bytes()
                    data = gdal.OpenEx(str(path), gdal.OF_VECTOR)
                    note = {f["key"]: f["value"] for f in data.GetLayerByName("export_note")}
                    self.assertEqual(note["row_count"], "2")
                    self.assertEqual(note["coordinate_system"], "WGS 84 (EPSG:4326). Longitude and latitude in decimal degrees.")
                    self.assertEqual(note["requested_by_role"], "division_officer at division D1 (Hill Division)")
                self.assertNotIn(SUBJECT.encode(), content)

    def test_note_reports_rows_removed_after_the_request(self):
        items = dict(note_items(meta("detections", "gpkg"), [], rows("detections")[:1], SEEN))
        self.assertEqual(items["row_count"], "1")
        self.assertIn("1 of the 2 rows", items["rows_removed"])

    def test_shapefile_names_are_short_unique_and_documented(self):
        levels = LEVELS + [Level("revenue", "district", "District", "revenue_"),
                           Level("revenue-2", "district", "District", "revenue_2_")]
        fields = export_fields("alerts", levels)
        shorts = [f.short for f in fields]
        self.assertTrue(all(len(name) <= 10 for name in shorts), shorts)
        self.assertEqual(len(shorts), len(set(shorts)))
        self.assertEqual(short_name("division_code", set()), "divisio_cd")
        self.assertEqual(short_name("division_code", {"divisio_cd"}), "divisi2_cd")
        readme = readme_text([], fields)
        self.assertIn("revenue_district_code (revenue_cd, text)", readme)
        self.assertIn("revenue_2_district_code (revenu2_cd, text)", readme)


if __name__ == "__main__":
    unittest.main()
