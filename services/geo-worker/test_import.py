import json
import tempfile
import unittest
from pathlib import Path
from zipfile import ZipFile

from osgeo import ogr
from shapely.geometry import Point, Polygon, box

from checks import Parent, Review, hierarchy_paths, review_layer, unit_topology
from source import Feature, ImportErrorWithProblem, archive_source, coordinate_system, read_source, square_metres, transform_to


class SourceTests(unittest.TestCase):
    def setUp(self):
        temporary = tempfile.TemporaryDirectory()
        self.addCleanup(temporary.cleanup)
        self.directory = Path(temporary.name)

    def vector_file(self, driver, path, reference, geometry):
        data = ogr.GetDriverByName(driver).CreateDataSource(str(path))
        layer = data.CreateLayer("units", reference, ogr.wkbPolygon)
        layer.CreateField(ogr.FieldDefn("label", ogr.OFTString))
        row = ogr.Feature(layer.GetLayerDefn())
        row.SetField("label", "Test area")
        row.SetGeometry(ogr.CreateGeometryFromWkb(geometry.wkb))
        layer.CreateFeature(row)
        data.Close()

    def zipped_shapefile(self, reference):
        shape = self.directory / "units.shp"
        archive = self.directory / "units.zip"
        self.vector_file("ESRI Shapefile", shape, reference, box(86, 16, 86.01, 16.01))
        with ZipFile(archive, "w") as zipped:
            for entry in self.directory.glob("units.*"):
                if entry != archive:
                    zipped.write(entry, f"folder/{entry.name}")
        return archive

    def test_geopackage_reprojects_and_maps_attributes(self):
        path = self.directory / "units.gpkg"
        original = box(86, 16, 86.01, 16.01)
        projected = transform_to(coordinate_system(4326), coordinate_system(7755))(original)
        self.vector_file("GPKG", path, coordinate_system(7755), projected)
        problems = []
        crs, [feature] = read_source(path, "hierarchy", {"nameField": "label"}, problems)
        self.assertEqual(crs, "EPSG:7755")
        self.assertEqual(feature.name, "Test area")
        self.assertEqual(feature.attributes, {})
        self.assertLess(feature.geom.hausdorff_distance(original), 1e-7)
        self.assertAlmostEqual(feature.area, projected.area, delta=1)
        self.assertEqual(problems, [])

    def test_zipped_shapefile_with_projection(self):
        crs, [feature] = read_source(self.zipped_shapefile(coordinate_system(4326)), "forest", {}, [])
        self.assertEqual(crs, "EPSG:4326")
        self.assertEqual(feature.attributes["label"], "Test area")

    def test_shapefile_without_projection(self):
        with self.assertRaises(ImportErrorWithProblem) as error:
            read_source(self.zipped_shapefile(None), "hierarchy", {}, [])
        self.assertEqual(error.exception.problem.kind, "missing_crs")
        self.assertTrue(error.exception.problem.blocks_publish)

    def test_kml_and_kmz_use_wgs84(self):
        kml = self.directory / "unit.kml"
        kml.write_text(
            '<kml xmlns="http://www.opengis.net/kml/2.2"><Placemark><name>Test area</name>'
            '<Point><coordinates>86,16,5</coordinates></Point></Placemark></kml>'
        )
        kmz = self.directory / "unit.kmz"
        with ZipFile(kmz, "w") as zipped:
            zipped.write(kml, "doc.kml")
        for file in [kml, kmz]:
            with self.subTest(file=file.suffix):
                crs, [feature] = read_source(file, "analysis", {"nameField": "Name"}, [])
                self.assertTrue(crs)
                self.assertEqual(feature.name, "Test area")
                self.assertTrue(feature.geom.equals(Point(86, 16)))
                self.assertEqual(feature.area, 0)

    def test_rejects_unsafe_archive_member(self):
        archive = self.directory / "units.zip"
        with ZipFile(archive, "w") as zipped:
            zipped.writestr("../units.shp", "")
        with self.assertRaises(ImportErrorWithProblem) as error:
            archive_source(archive, ".shp")
        self.assertEqual(error.exception.problem.kind, "invalid_archive")

    def test_invalid_geojson_geometry_is_repaired(self):
        geojson = self.directory / "units.geojson"
        geojson.write_text(json.dumps({
            "type": "Feature",
            "geometry": {"type": "Polygon", "coordinates": [[[86, 16], [86.01, 16.01], [86, 16.01], [86.01, 16], [86, 16]]]},
            "properties": {"CODE": "T1"},
        }))
        problems = []
        crs, [feature] = read_source(geojson, "forest", {"codeField": "CODE"}, problems)
        self.assertEqual(crs, "EPSG:4326")
        self.assertEqual(feature.code, "T1")
        self.assertTrue(feature.geom.is_valid)
        self.assertEqual(len(feature.geom.geoms), 2)
        self.assertEqual(problems[0].kind, "geometry_repaired")
        with self.assertRaises(ImportErrorWithProblem) as error:
            read_source(geojson, "forest", {"codeField": "absent"}, [])
        self.assertEqual(error.exception.problem.kind, "unknown_field")


class ReviewTests(unittest.TestCase):
    def setUp(self):
        self.area_of = square_metres()

    def test_missing_code_and_parent_use_a_proposal_and_largest_overlap(self):
        parents = [Parent("R1", "root.r1", box(86, 16, 86.01, 16.01)),
                   Parent("R2", "root.r2", box(86.01, 16, 86.02, 16.01))]
        unit = Feature(1, box(86.008, 16, 86.018, 16.01), 0, None, "Beat 16", None)
        problems = []
        hierarchy_paths([unit], 2, parents, problems, self.area_of)
        self.assertEqual((unit.code, unit.parent_code, unit.parent_path, unit.path),
                         ("B16", "R2", "root.r2", "root.r2.b16"))
        self.assertEqual([p.kind for p in problems], ["missing_code", "missing_parent_code"])
        self.assertTrue(all(p.blocks_publish and p.feature_ref == "feature:1" for p in problems))

    def test_duplicate_paths_are_blocking(self):
        units = [Feature(i, box(86, 16, 86.01, 16.01), 0, code, code, None)
                 for i, code in enumerate(["Unit 1", "unit_1"], 1)]
        problems = []
        hierarchy_paths(units, 0, [], problems, self.area_of)
        self.assertEqual([unit.path for unit in units], [None, None])
        self.assertEqual([p.kind for p in problems], ["duplicate_path", "duplicate_path"])

    def test_overlap_and_gap_are_found_without_flagging_shared_edges(self):
        units = [
            Feature(1, box(86, 16, 86.01, 16.01), 0, "A", "A", None),
            Feature(2, box(86.009, 16, 86.02, 16.01), 0, "B", "B", None),
            Feature(3, box(86.02, 16, 86.03, 16.01), 0, "C", "C", None),
        ]
        problems = Review()
        unit_topology(units, [("parent:root", box(86, 16, 86.031, 16.01))], problems, self.area_of)
        self.assertEqual([(p.kind, p.feature_ref) for p in problems],
                         [("overlapping_units", "feature:1"), ("unit_gap", "parent:root")])

    def test_forest_reports_hole_missing_code_and_uncovered_area(self):
        outer = box(86, 16, 86.01, 16.01)
        hole = box(86.003, 16.003, 86.004, 16.004)
        geom = Polygon(outer.exterior, [hole.exterior])
        forest = Feature(1, geom, 0, None, "Forest", None)
        problems = []
        review_layer("forest", [forest], problems, [box(86, 16, 86.009, 16.01)])
        self.assertEqual([p.kind for p in problems], ["missing_code", "forest_hole", "forest_outside_units"])
        self.assertIsNone(forest.code)
        self.assertTrue(all(not p.blocks_publish for p in problems))


if __name__ == "__main__":
    unittest.main()
