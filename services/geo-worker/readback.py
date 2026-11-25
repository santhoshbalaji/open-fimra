"""Open export files with OGR, the library QGIS uses, and describe them.

Usage: python readback.py FILE [FILE ...]

For each file it prints every layer with its geometry type, coordinate system,
field names and types, and feature count. It exits with an error if a file
cannot be opened or has no data layer.

The KML driver in this GDAL build reads only each placemark's name and
description. QGIS builds with the LIBKML driver also read the typed fields
in ExtendedData, so for KML files this tool lists those from the file's schema.
"""

import sys
import zipfile
from pathlib import Path
from xml.etree import ElementTree

from osgeo import gdal, ogr

gdal.UseExceptions()
ogr.UseExceptions()

DATA_SUFFIXES = (".shp", ".geojson", ".fgb", ".csv", ".kml")
KML = "{http://www.opengis.net/kml/2.2}"


def open_path(path):
    """Returns the OGR path, open options and archive members for an export."""
    path = Path(path)
    if path.suffix in (".zip", ".kmz"):
        with zipfile.ZipFile(path) as archive:
            names = archive.namelist()
        [name] = [n for n in names if n.endswith(DATA_SUFFIXES)]
        options = []
        if name.endswith(".csv"):
            # The CSV with longitude and latitude columns has no geometry column.
            options = ["X_POSSIBLE_NAMES=longitude", "Y_POSSIBLE_NAMES=latitude"]
        return f"/vsizip/{path}/{name}", options, names
    return str(path), [], []


def kml_schema(path):
    with zipfile.ZipFile(path) as archive:
        root = ElementTree.fromstring(archive.read("doc.kml"))
    return [(field.get("name"), field.get("type")) for field in root.iter(f"{KML}SimpleField")]


def describe(path):
    ogr_path, options, members = open_path(path)
    data = gdal.OpenEx(ogr_path, gdal.OF_VECTOR | gdal.OF_READONLY, open_options=options)
    layers = []
    for index in range(data.GetLayerCount()):
        layer = data.GetLayer(index)
        definition = layer.GetLayerDefn()
        srs = layer.GetSpatialRef()
        geometry = ogr.GeometryTypeToName(layer.GetGeomType())
        if layer.GetGeomType() == ogr.wkbUnknown:
            # A CSV WKT column has no declared type: report what the rows hold.
            kinds = {f.GetGeometryRef().GetGeometryName().title() for f in layer if f.GetGeometryRef()}
            layer.ResetReading()
            geometry = f"{geometry}, rows hold {', '.join(sorted(kinds)) or 'nothing'}"
        layers.append({
            "name": layer.GetName(),
            "geometry": geometry,
            "crs": (f"{srs.GetAuthorityName(None)}:{srs.GetAuthorityCode(None)}"
                    if srs is not None and srs.GetAuthorityCode(None) else None),
            "fields": [(definition.GetFieldDefn(i).GetName(), definition.GetFieldDefn(i).GetTypeName())
                       for i in range(definition.GetFieldCount())],
            "count": layer.GetFeatureCount(),
        })
    info = {"path": str(path), "driver": data.GetDriver().ShortName, "members": members, "layers": layers}
    if Path(path).suffix == ".kmz":
        info["kml_schema"] = kml_schema(path)
    return info


def main(paths):
    if not paths:
        sys.exit("Give one or more export files.")
    for path in paths:
        info = describe(path)
        print(f"{Path(info['path']).name}: driver {info['driver']}")
        if info["members"]:
            print(f"  files inside: {', '.join(info['members'])}")
        for layer in info["layers"]:
            print(f"  layer {layer['name']}: {layer['geometry']}, coordinate system {layer['crs'] or 'none'}, "
                  f"{layer['count']} features")
            print("    fields: " + ", ".join(f"{name} {kind}" for name, kind in layer["fields"]))
        if "kml_schema" in info:
            print("    KML schema fields: " + ", ".join(f"{name} {kind}" for name, kind in info["kml_schema"]))
        if not any(layer["name"] in ("detections", "alerts", "submissions") for layer in info["layers"]):
            sys.exit(f"{path} has no detections, alerts or submissions layer")


if __name__ == "__main__":
    main(sys.argv[1:])
