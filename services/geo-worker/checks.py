"""Build draft hierarchy paths and find spatial problems to review."""

import re
import unicodedata
from collections import Counter, defaultdict
from dataclasses import dataclass

from shapely import STRtree
from shapely.geometry import Polygon
from shapely.ops import unary_union

from source import Problem, polygons, square_metres

# Ignore tiny artifacts from decimal and projection rounding.
MIN_AREA_M2 = 1.0
MAX_PROBLEMS = 2000


class Review(list):
    def append(self, problem):
        if len(self) < MAX_PROBLEMS:
            super().append(problem)
        elif len(self) == MAX_PROBLEMS:
            super().append(Problem(
                "review_limit", None,
                "The layer has more than 2,000 problems. Fix the reported problems and upload it again.",
                True,
            ))

    @property
    def full(self):
        return len(self) > MAX_PROBLEMS


@dataclass
class Parent:
    code: str
    path: str
    geom: object


def path_label(code):
    folded = unicodedata.normalize("NFKD", code).encode("ascii", "ignore").decode().lower()
    return re.sub(r"[^a-z0-9_-]+", "_", folded).strip("_-")[:255]


def propose_code(feature, used_codes):
    name = feature.name or ""
    # Short labels such as "Beat 16" give a readable first suggestion, "B16".
    numbered = re.fullmatch(r"([A-Za-z])[A-Za-z]*\s+(\d+)", name)
    if numbered:
        candidate = "".join(numbered.groups()).upper()
    else:
        candidate = path_label(name).upper()
    if not candidate or candidate.casefold() in used_codes:
        candidate = f"UNIT-{feature.number}"
        while candidate.casefold() in used_codes:
            candidate += "-DRAFT"
    return candidate


def hierarchy_paths(features, depth, parents, problems, area_of):
    used_codes = {feature.code.casefold() for feature in features if feature.code}
    by_code = defaultdict(list)
    for i, parent in enumerate(parents):
        by_code[parent.code.casefold()].append(i)
    parent_tree = STRtree([parent.geom for parent in parents])

    for feature in features:
        ref = feature.ref
        if not feature.code:
            feature.code = propose_code(feature, used_codes)
            used_codes.add(feature.code.casefold())
            problems.append(Problem("missing_code", ref, f"This unit has no code. Suggested code: {feature.code}.", True))
        if not feature.name:
            feature.name = feature.code
            problems.append(Problem("missing_name", ref, "This unit has no name. Its code is shown as the draft name.", True))
        label = path_label(feature.code)
        if not label:
            problems.append(Problem("invalid_code", ref, "This unit's code cannot be used in a hierarchy path.", True))

        if depth == 0:
            feature.parent_code = None
            feature.path = label or None
            continue

        matches = by_code.get(feature.parent_code.casefold(), []) if feature.parent_code else []
        if len(matches) == 1:
            parent = parents[matches[0]]
            if parent.geom is not None and area_of(feature.geom.intersection(parent.geom)) <= MIN_AREA_M2:
                problems.append(Problem("parent_outside", ref, f"This unit does not overlap its mapped parent {parent.code}.", True))
        else:
            candidates = matches or parent_tree.query(feature.geom, predicate="intersects")
            ranked = sorted(
                ((area_of(feature.geom.intersection(parents[i].geom)), parents[i].path, parents[i])
                 for i in candidates if parents[i].geom is not None),
                key=lambda item: (-item[0], item[1]),
            )
            parent = ranked[0][2] if ranked and ranked[0][0] > MIN_AREA_M2 else None
            kind = "missing_parent_code" if not feature.parent_code else ("ambiguous_parent_code" if matches else "unknown_parent_code")
            reason = {
                "missing_parent_code": "This unit has no parent code.",
                "ambiguous_parent_code": "This unit's parent code matches more than one unit.",
                "unknown_parent_code": "This unit's parent code does not match a unit.",
            }[kind]
            suggestion = f" Suggested parent by largest overlap: {parent.code}." if parent else " No parent could be inferred from the map."
            problems.append(Problem(kind, ref, reason + suggestion, True))
        if parent:
            feature.parent_code = parent.code
            feature.parent_path = parent.path
            if label:
                feature.path = f"{parent.path}.{label}"

    paths = Counter(feature.path for feature in features if feature.path)
    for feature in features:
        if paths[feature.path] > 1:
            problems.append(Problem("duplicate_path", feature.ref, f"More than one unit has the path {feature.path}.", True))
            feature.path = None


def unit_topology(features, footprints, problems, area_of):
    if not features:
        return
    shapes = [feature.geom for feature in features]
    index = STRtree(shapes)
    for i, feature in enumerate(features):
        for j in sorted(index.query(feature.geom, predicate="intersects")):
            if problems.full:
                return
            if j <= i:
                continue
            other = features[j]
            overlap = area_of(feature.geom.intersection(other.geom))
            if overlap > MIN_AREA_M2:
                problems.append(Problem(
                    "overlapping_units", feature.ref,
                    f"Unit {feature.code} overlaps unit {other.code} ({other.ref}) by {overlap:,.0f} square metres.",
                    True,
                ))

    if footprints:
        for ref, footprint in footprints:
            if problems.full:
                return
            if footprint is None:
                continue
            covered = unary_union([shapes[i] for i in index.query(footprint, predicate="intersects")])
            add_gaps(footprint.difference(covered), ref, problems, area_of)
    else:
        # Without a parent boundary only fully enclosed gaps can be measured.
        for polygon in polygons(unary_union(shapes)):
            for ring in polygon.interiors:
                add_gaps(Polygon(ring), None, problems, area_of)


def add_gaps(geom, ref, problems, area_of):
    for gap in polygons(geom):
        if problems.full:
            return
        area = area_of(gap)
        if area > MIN_AREA_M2:
            problems.append(Problem("unit_gap", ref, f"Neighbouring units leave a gap of {area:,.0f} square metres.", True))


def review_layer(role, features, problems, coverage=None):
    area_of = square_metres()
    index = STRtree(coverage or [])
    if role == "forest" and not coverage:
        problems.append(Problem("coverage_unchecked", None, "There are no territorial units available to check forest coverage."))
    for feature in features:
        if not feature.code and role != "exclusion" and role != "analysis":
            problems.append(Problem("missing_code", feature.ref, "This feature has no code."))
        if role != "forest":
            continue
        if any(poly.interiors for poly in polygons(feature.geom)):
            problems.append(Problem("forest_hole", feature.ref, "This forest contains a hole. Check that the clearing belongs here."))
        if coverage:
            nearby = index.query(feature.geom, predicate="intersects")
            covered = unary_union([coverage[i] for i in nearby])
            outside = area_of(feature.geom.difference(covered))
            if outside > MIN_AREA_M2:
                problems.append(Problem(
                    "forest_outside_units", feature.ref,
                    f"This forest has {outside:,.0f} square metres outside all units at the selected level.",
                ))
