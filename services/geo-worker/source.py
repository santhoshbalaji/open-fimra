"""Read local vector sources and normalize their geometry."""

import logging
import math
import stat
from dataclasses import dataclass, field
from pathlib import Path, PurePosixPath
from zipfile import BadZipFile, ZipFile

from osgeo import gdal, osr
from shapely import force_2d, from_wkb, get_num_coordinates, is_valid_reason, make_valid
from shapely.errors import ShapelyError
from shapely.geometry import MultiPolygon
from shapely.geometry.base import BaseGeometry
from shapely.ops import transform, unary_union

gdal.UseExceptions()
osr.UseExceptions()

logger = logging.getLogger("geo-worker")

MAX_ARCHIVE_BYTES = 128 << 20
MAX_FEATURES = 20_000
MAX_COORDINATES = 1_000_000


@dataclass
class Problem:
    kind: str
    feature_ref: str | None
    message: str
    blocks_publish: bool = False


class ImportErrorWithProblem(Exception):
    def __init__(self, kind, message):
        super().__init__(message)
        self.problem = Problem(kind, None, message, True)


@dataclass
class Feature:
    number: int
    geom: BaseGeometry
    area: float
    code: str | None
    name: str | None
    parent_code: str | None
    attributes: dict = field(default_factory=dict)
    path: str | None = None
    parent_path: str | None = None

    @property
    def ref(self):
        return f"feature:{self.number}"


def archive_source(path, suffix):
    try:
        with ZipFile(path) as archive:
            entries = archive.infolist()
            if len(entries) > 128 or sum(info.file_size for info in entries) > MAX_ARCHIVE_BYTES:
                raise ImportErrorWithProblem("archive_limit", "The archive exceeds the file count or unpacked size limit.")
            members = []
            for info in entries:
                member = PurePosixPath(info.filename)
                if (member.is_absolute() or ".." in member.parts or "\\" in info.filename
                        or "{" in info.filename or "}" in info.filename
                        or stat.S_ISLNK(info.external_attr >> 16) or info.flag_bits & 1):
                    raise ImportErrorWithProblem("invalid_archive", "The archive contains an unsupported entry.")
                if not info.is_dir():
                    members.append(info.filename)
            if len({name.casefold() for name in members}) != len(members):
                raise ImportErrorWithProblem("invalid_archive", "The archive contains duplicate file names.")
            matches = [member for member in members if PurePosixPath(member).suffix.lower() == suffix]
            # Standard KMZ exports may contain supporting files beside doc.kml.
            if suffix == ".kml" and "doc.kml" in matches:
                return f"/vsizip/{{{path}}}/doc.kml"
            if len(matches) != 1:
                raise ImportErrorWithProblem("invalid_archive", f"The archive must contain one {suffix} dataset.")
            return f"/vsizip/{{{path}}}/{matches[0]}"
    except (BadZipFile, OSError) as exc:
        raise ImportErrorWithProblem("invalid_archive", "The file is not a readable ZIP archive.") from exc


def coordinate_system(epsg):
    reference = osr.SpatialReference()
    reference.ImportFromEPSG(epsg)
    reference.SetAxisMappingStrategy(osr.OAMS_TRADITIONAL_GIS_ORDER)
    return reference


def transform_to(source, target):
    if source.IsSame(target):
        return lambda geom: geom
    projection = osr.CoordinateTransformation(source, target)

    def reproject(geom):
        def coordinates(xs, ys):
            points = projection.TransformPoints(list(zip(xs, ys)))
            return [point[0] for point in points], [point[1] for point in points]

        return transform(coordinates, geom)

    return reproject


def square_metres():
    project = transform_to(coordinate_system(4326), coordinate_system(7755))
    return lambda geom: project(geom).area


def polygons(geom):
    if geom.geom_type == "Polygon":
        yield geom
    if geom.geom_type in {"GeometryCollection", "MultiPolygon"}:
        for part in geom.geoms:
            yield from polygons(part)


def normalize(geom, polygon_only, ref, problems):
    if geom.is_empty or not all(math.isfinite(value) for value in geom.bounds):
        problems.append(Problem("empty_geometry", ref, "This feature has no usable geometry.", True))
        return None
    if not geom.is_valid:
        reason = is_valid_reason(geom).partition("[")[0]
        repaired = make_valid(geom)
        if polygon_only:
            repaired = unary_union(list(polygons(repaired)))
        if repaired.is_empty or not repaired.is_valid:
            problems.append(Problem("invalid_geometry", ref, "This feature could not be repaired.", True))
            return None
        geom = repaired
        problems.append(Problem("geometry_repaired", ref, f"This feature had invalid geometry and was repaired ({reason})."))
    if polygon_only:
        if geom.geom_type == "Polygon":
            return MultiPolygon([geom])
        if geom.geom_type != "MultiPolygon":
            problems.append(Problem("non_polygon", ref, "This role requires an area drawn as a polygon.", True))
            return None
    return geom


def read_source(path, role, settings, problems):
    polygon_only = role in {"forest", "hierarchy"}
    drivers = {
        ".gpkg": "GPKG", ".zip": "ESRI Shapefile", ".geojson": "GeoJSON",
        ".kml": "KML", ".kmz": "KML",
    }
    source = str(path)
    if path.suffix in {".zip", ".kmz"}:
        source = archive_source(path, ".shp" if path.suffix == ".zip" else ".kml")
    try:
        dataset = gdal.OpenEx(source, gdal.OF_VECTOR | gdal.OF_VERBOSE_ERROR, [drivers[path.suffix]])
    except RuntimeError as exc:
        raise ImportErrorWithProblem("unreadable_file", "The file could not be opened in the selected format.") from exc

    layer_name = settings.get("sourceLayer")
    if layer_name:
        layer = dataset.GetLayerByName(layer_name)
    elif dataset.GetLayerCount() == 1:
        layer = dataset.GetLayer(0)
    else:
        raise ImportErrorWithProblem("choose_layer", "Supply a source layer name when the file has more than one layer.")
    if layer is None:
        raise ImportErrorWithProblem("unknown_layer", "The named layer was not found in the file.")

    reference = layer.GetSpatialRef()
    if (reference is None or not (reference.IsGeographic() or reference.IsProjected())
            or (reference.GetName() or "").lower().startswith(("undefined", "unknown"))):
        # WGS 84 GeoJSON and KML have an implicit reference supplied by GDAL.
        raise ImportErrorWithProblem("missing_crs", "The file has no usable coordinate system. Add its projection and upload it again.")
    reference = reference.Clone()
    reference.SetAxisMappingStrategy(osr.OAMS_TRADITIONAL_GIS_ORDER)
    authority, code = reference.GetAuthorityName(None), reference.GetAuthorityCode(None)
    crs = f"{authority}:{code}" if authority and code else reference.GetName()
    to_wgs84 = transform_to(reference, coordinate_system(4326))
    area_of = square_metres()

    definition = layer.GetLayerDefn()
    fields = [definition.GetFieldDefn(i).GetName() for i in range(definition.GetFieldCount())]
    mapped = [settings.get(field) for field in ("nameField", "codeField", "parentCodeField")]
    for name in mapped:
        if name and name not in fields:
            raise ImportErrorWithProblem("unknown_field", f'The mapped field "{name}" is missing from the source layer.')

    result = []
    coordinate_count = 0
    for number, row in enumerate(layer, 1):
        if number > MAX_FEATURES:
            raise ImportErrorWithProblem("feature_limit", "The source layer has more than 20,000 features.")
        ref = f"feature:{number}"
        ogr_geom = row.GetGeometryRef()
        if ogr_geom is None:
            problems.append(Problem("empty_geometry", ref, "This feature has no geometry.", True))
            continue
        try:
            geom = from_wkb(bytes(ogr_geom.ExportToIsoWkb()))
            coordinate_count += get_num_coordinates(geom)
            if coordinate_count > MAX_COORDINATES:
                raise ImportErrorWithProblem("coordinate_limit", "The source layer has more than one million coordinates.")
            geom = to_wgs84(force_2d(geom))
            if not geom.is_empty:
                west, south, east, north = geom.bounds
                if not (-180 <= west <= east <= 180 and -90 <= south <= north <= 90):
                    problems.append(Problem("out_of_bounds", ref, "This feature has coordinates outside WGS 84.", True))
                    continue
            geom = normalize(geom, polygon_only, ref, problems)
            if geom is None:
                continue
            area = area_of(geom)
            if not math.isfinite(area):
                raise ValueError("the projected area is not finite")
        except (RuntimeError, ValueError, ShapelyError) as exc:
            logger.warning("Could not read or project feature %s in %s: %s", number, path.name, exc)
            problems.append(Problem("unusable_geometry", ref, "This feature could not be read or projected.", True))
            continue

        properties = row.items()
        values = [str(properties[name]).strip() or None
                  if name and properties.get(name) is not None else None for name in mapped]
        attributes = {name: value for name, value in properties.items() if name not in mapped}
        result.append(Feature(number, geom, area, values[1], values[0], values[2], attributes))

    if not result:
        problems.append(Problem("no_features", None, "The layer has no usable features.", True))
    return crs, result
