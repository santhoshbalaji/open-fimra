#!/usr/bin/env python3
"""Generate the synthetic demo state in seed/demo-state/.

Everything here is made up. "Demo State" is a fictional state drawn over open
sea in the Bay of Bengal, so no shape can be mistaken for a real forest or
administrative boundary. It has a five-level territorial hierarchy
(state, circle, division, range, beat), forest and exclusion layers, and a set
of reference detections with the result each one should get.

The data deliberately includes the awkward cases that real layers contain:
a forest with a hole, a detection exactly on a shared beat boundary, two beats
that overlap, forest outside every beat, a beat without a code, a beat without
a parent code and a forest without a code.

The output is reproducible: the same script always writes the same bytes.
It uses only the Python standard library.

Usage:
    python3 seed/generate.py           write seed/demo-state/
    python3 seed/generate.py --check   fail if seed/demo-state/ is out of date
"""

import argparse
import hashlib
import json
import math
import pathlib
import sys

OUT = pathlib.Path(__file__).resolve().parent / "demo-state"

# Grid of beats. Each range is one column of three beats.
LON0, LAT0 = 86.20, 16.30   # south-west corner, open sea
DX, DY = 0.06, 0.08         # one beat cell, about 6.4 km by 8.9 km
COLS, ROWS = 8, 3
SEG = 4                     # segments per cell edge, to make boundaries irregular
LAT_SCALE = 1 / math.cos(math.radians(LAT0 + ROWS * DY / 2))  # keeps round shapes round

NEAR_EDGE_M = 500
AREA_MARGIN_M = 5000
VALID_FROM = "2026-01-01T00:00:00Z"

WORKSPACE_ID = "d0000000-0000-4000-8000-000000000001"
HIERARCHY_ID = "d0000000-0000-4000-8000-000000000010"
LAYERS = {
    # slug: (layer id, version id, name, role)
    "territorial-units": ("d0000000-0000-4000-8000-000000000020", "d0000000-0000-4000-8000-000000000021",
                          "Territorial units", "hierarchy"),
    "forest": ("d0000000-0000-4000-8000-000000000030", "d0000000-0000-4000-8000-000000000031",
               "Forest", "forest"),
    "exclusions": ("d0000000-0000-4000-8000-000000000040", "d0000000-0000-4000-8000-000000000041",
                   "Known heat sources", "exclusion"),
}

LEVELS = [  # depth, slug, English label, Hindi label
    (0, "state", "State", "राज्य"),
    (1, "circle", "Circle", "वृत्त"),
    (2, "division", "Division", "प्रभाग"),
    (3, "range", "Range", "रेंज"),
    (4, "beat", "Beat", "बीट"),
]

CIRCLE_NAMES = ["West Circle", "East Circle"]
DIVISION_NAMES = ["Hill Division", "Valley Division", "River Division", "Lake Division"]

# Deliberate defects in the beat layer.
OVERLAP_CELL = (5, 1)          # this beat bulges into its eastern neighbour
OVERLAP_SHIFT = 0.004          # degrees of longitude, about 430 m
UNCODED_BEAT = 16              # beat with no code in the source layer
UNPARENTED_BEAT = 22           # beat with no parent code in the source layer


def h(*key):
    """A deterministic number in [-1, 1) for the given key."""
    digest = hashlib.sha256(repr(key).encode()).digest()
    return int.from_bytes(digest[:8], "big") / 2**63 - 1


def r6(v):
    return round(v, 6)


def node(i, j):
    """Grid node (i, j), moved slightly so the cells look less like a grid."""
    x = LON0 + i * DX + 0.12 * DX * h("node-x", i, j)
    y = LAT0 + j * DY + 0.12 * DY * h("node-y", i, j)
    return (x, y)


def edge(a, b, bulge=0.0):
    """Points from node a to node b along their shared, slightly wavy edge.

    Both cells that share an edge build it from the same canonical key, so
    shared boundaries match vertex for vertex. `bulge` shifts the inner points
    east, which is how the overlapping beat is made.
    """
    start, end = (a, b) if a <= b else (b, a)
    (x0, y0), (x1, y1) = node(*start), node(*end)
    length = math.hypot(x1 - x0, y1 - y0)
    nx, ny = -(y1 - y0) / length, (x1 - x0) / length
    pts = [(x0, y0)]
    for k in range(1, SEG):
        t = k / SEG
        off = 0.05 * length * h("edge", start, end, k) * math.sin(math.pi * t)
        pts.append((x0 + (x1 - x0) * t + nx * off + bulge, y0 + (y1 - y0) * t + ny * off))
    pts.append((x1, y1))
    return pts if a <= b else pts[::-1]


def outline(c0, c1, r0, r1, bulge_east=0.0):
    """Counter-clockwise outline of the block of cells c0..c1-1, r0..r1-1."""
    path = []

    def walk(nodes, bulge=0.0):
        for a, b in zip(nodes, nodes[1:]):
            seg = edge(a, b, bulge)
            path.extend(seg if not path else seg[1:])

    walk([(i, r0) for i in range(c0, c1 + 1)])
    walk([(c1, j) for j in range(r0, r1 + 1)], bulge_east)
    walk([(i, r1) for i in range(c1, c0 - 1, -1)])
    walk([(c0, j) for j in range(r1, r0 - 1, -1)])
    return [[r6(x), r6(y)] for x, y in path]


def blob(cx, cy, r, key, n=28):
    """A counter-clockwise, irregular round shape."""
    pts = []
    for k in range(n):
        a = 2 * math.pi * k / n
        rr = r * (1 + 0.15 * h("blob", key, k))
        pts.append([r6(cx + rr * math.cos(a) * LAT_SCALE), r6(cy + rr * math.sin(a))])
    return pts + [pts[0]]


def cell_center(c, r):
    pts = [node(c, r), node(c + 1, r), node(c + 1, r + 1), node(c, r + 1)]
    return (sum(p[0] for p in pts) / 4, sum(p[1] for p in pts) / 4)


def feature(props, geometry):
    return {"type": "Feature", "properties": props, "geometry": geometry}


def polygon(*rings):
    return {"type": "Polygon", "coordinates": list(rings)}


def point(x, y):
    return {"type": "Point", "coordinates": [r6(x), r6(y)]}


def build():
    units = {d: [] for d, *_ in LEVELS}

    units[0].append({"code": "DS", "name": "Demo State", "parent": None, "path": "ds",
                     "ring": outline(0, COLS, 0, ROWS)})
    for ci in range(2):
        units[1].append({"code": f"C{ci + 1}", "name": CIRCLE_NAMES[ci], "parent": "DS",
                         "path": f"ds.c{ci + 1}", "ring": outline(ci * 4, ci * 4 + 4, 0, ROWS)})
    for di in range(4):
        ci = di // 2
        units[2].append({"code": f"D{di + 1}", "name": DIVISION_NAMES[di], "parent": f"C{ci + 1}",
                         "path": f"ds.c{ci + 1}.d{di + 1}", "ring": outline(di * 2, di * 2 + 2, 0, ROWS)})
    for ri in range(COLS):
        di, ci = ri // 2, ri // 4
        units[3].append({"code": f"R{ri + 1}", "name": f"Range {ri + 1}", "parent": f"D{di + 1}",
                         "path": f"ds.c{ci + 1}.d{di + 1}.r{ri + 1}", "ring": outline(ri, ri + 1, 0, ROWS)})
    for c in range(COLS):
        for r in range(ROWS):
            n = c * ROWS + r + 1
            di, ci = c // 2, c // 4
            bulge = OVERLAP_SHIFT if (c, r) == OVERLAP_CELL else 0.0
            units[4].append({
                "code": f"B{n:02d}", "name": f"Beat {n:02d}", "parent": f"R{c + 1}",
                "path": f"ds.c{ci + 1}.d{di + 1}.r{c + 1}.b{n:02d}",
                "ring": outline(c, c + 1, r, r + 1, bulge_east=bulge),
                "cell": (c, r), "number": n,
            })

    def beat_path(c, r):
        return units[4][c * ROWS + r]["path"]

    # Forests.
    f01 = (node(2, 1)[0], (node(2, 1)[1] + node(2, 2)[1]) / 2)   # straddles beats in columns 1 and 2
    f02 = cell_center(3, 1)
    hole = (f02[0] + 0.004, f02[1] - 0.006)
    f03 = (LON0 + COLS * DX + 0.012, cell_center(7, 2)[1])       # crosses the eastern state edge
    f04 = cell_center(6, 0)
    f05 = (node(6, 1)[0], (node(6, 1)[1] + node(6, 2)[1]) / 2)   # covers the overlapping beats
    hole_ring = blob(*hole, 0.012, "F02-hole")[::-1]             # clockwise
    forests = [
        ("F01", "Demo Reserve One", "reserved", [blob(*f01, 0.030, "F01")]),
        ("F02", "Demo Reserve Two", "reserved", [blob(*f02, 0.045, "F02"), hole_ring]),
        ("F03", "Demo Reserve Three", "protected", [blob(*f03, 0.035, "F03")]),
        (None, "Uncoded Forest", "unclassed", [blob(*f04, 0.025, "F04")]),
        ("F05", "Demo Reserve Five", "reserved", [blob(*f05, 0.025, "F05")]),
    ]

    # A known heat source just outside forest F02, to the north.
    kx, ky = f02[0], f02[1] + 0.045 * 1.15 + 0.010
    kiln = [[r6(kx - 0.002), r6(ky - 0.002)], [r6(kx + 0.002), r6(ky - 0.002)],
            [r6(kx + 0.002), r6(ky + 0.002)], [r6(kx - 0.002), r6(ky + 0.002)], [r6(kx - 0.002), r6(ky - 0.002)]]
    exclusions = [("E01", "Brick kiln", "kiln", [kiln])]

    # Reference detections: where each one is, and the result it should get.
    shared_vertex = edge((2, 1), (2, 2))[SEG // 2]
    f02_edge = blob(*f02, 0.045, "F02")[0]                       # vertex at angle 0, due east
    overlap_mid = edge((6, 1), (6, 2), OVERLAP_SHIFT)[SEG // 2]
    kiln_beat_cell = (3, 2) if ky > node(3, 2)[1] + 0.003 else (3, 1)
    refs = [
        ("inside", "Inside forest F01, in one beat", (f01[0] - 0.015 * LAT_SCALE, f01[1]),
         "inside", [beat_path(1, 1)], None, "F01"),
        ("shared-boundary", "Inside forest F01, exactly on the boundary between two beats", shared_vertex,
         "inside", [beat_path(1, 1), beat_path(2, 1)], "shared_boundary", "F01"),
        ("hole", "In the clearing inside forest F02, more than 500 m from the forest", hole,
         "outside", [beat_path(3, 1)], None, None),
        ("ring", "Inside forest F02, between its hole and its edge", (f02[0] - 0.030 * LAT_SCALE, f02[1]),
         "inside", [beat_path(3, 1)], None, "F02"),
        # F02 is wider than its beat, so its eastern edge lies in the next column.
        ("near-edge", "About 300 m outside forest F02, to the east", (f02_edge[0] + 0.0028 * LAT_SCALE, f02_edge[1]),
         "near_edge", [beat_path(4, 1)], None, None),
        ("outside", "Far from any forest", cell_center(0, 2),
         "outside", [beat_path(0, 2)], None, None),
        ("overlap", "Inside forest F05, where two beats overlap", (overlap_mid[0] - OVERLAP_SHIFT / 2, overlap_mid[1]),
         "inside", [beat_path(5, 1), beat_path(6, 1)], "overlapping_units", "F05"),
        ("no-unit", "Inside forest F03, east of the state edge where there is no beat", (f03[0] + 0.008 * LAT_SCALE, f03[1]),
         "inside", [], "no_unit", "F03"),
        ("kiln", "Inside the brick kiln exclusion", (kx, ky),
         "fixed_heat_source", [beat_path(*kiln_beat_cell)], None, None),
        ("uncoded-forest", "Inside the forest that has no code", f04,
         "inside", [beat_path(6, 0)], None, None),
        ("far-away", "Far outside the workspace, so it is never matched", (LON0 - 1.5, LAT0 - 1.0),
         None, [], None, None),
    ]
    return units, forests, exclusions, refs


def sql_str(v):
    if v is None:
        return "NULL"
    return "'" + str(v).replace("'", "''") + "'"


def sql_geom(geometry, multi=False):
    g = f"ST_GeomFromGeoJSON({sql_str(json.dumps(geometry, separators=(',', ':')))})"
    g = f"ST_SetSRID({g}, 4326)"
    return f"ST_Multi({g})" if multi else g


def outputs():
    units, forests, exclusions, refs = build()
    files = {}

    # Source layers, as an agency would upload them, defects included.
    level_files = {0: "state", 1: "circles", 2: "divisions", 3: "ranges", 4: "beats"}
    for depth, stem in level_files.items():
        feats = []
        for u in units[depth]:
            props = {"code": u["code"], "name": u["name"], "parent_code": u["parent"]}
            if depth == 4 and u["number"] == UNCODED_BEAT:
                props["code"] = None
            if depth == 4 and u["number"] == UNPARENTED_BEAT:
                props["parent_code"] = None
            feats.append(feature(props, polygon(u["ring"])))
        files[f"{stem}.geojson"] = ("geojson", stem, feats)

    files["forest.geojson"] = ("geojson", "forest", [
        feature({"code": code, "name": name, "legal_status": status}, polygon(*rings))
        for code, name, status, rings in forests])
    files["exclusions.geojson"] = ("geojson", "exclusions", [
        feature({"code": code, "name": name, "kind": kind}, polygon(*rings))
        for code, name, kind, rings in exclusions])
    files["reference-detections.geojson"] = ("geojson", "reference-detections", [
        feature({"id": rid, "description": desc, "expected_forest_class": cls,
                 "expected_beats": beats, "expected_triage": triage, "expected_forest_code": fcode},
                point(*xy))
        for rid, desc, xy, cls, beats, triage, fcode in refs])

    # Database load script for the demo workspace.
    lines = [
        "-- Generated by seed/generate.py. Do not edit by hand.",
        "-- Loads the synthetic demo state. Everything here is made up.",
        "",
        "INSERT INTO workspaces (id, slug, name, boundary, settings) VALUES",
        f"  ({sql_str(WORKSPACE_ID)}, 'demo', 'Demo State', {sql_geom(polygon(units[0][0]['ring']), True)},",
        f"   '{json.dumps({'nearEdgeMetres': NEAR_EDGE_M, 'areaMarginMetres': AREA_MARGIN_M})}');",
        "",
        "INSERT INTO hierarchies (id, workspace_id, slug, name, kind) VALUES",
        f"  ({sql_str(HIERARCHY_ID)}, {sql_str(WORKSPACE_ID)}, 'territorial', 'Territorial', 'territorial');",
        "",
        "INSERT INTO hierarchy_levels (hierarchy_id, depth, slug, labels) VALUES",
        ",\n".join(f"  ({sql_str(HIERARCHY_ID)}, {d}, '{s}', {sql_str(json.dumps({'en': en, 'hi': hi}, ensure_ascii=False))})"
                   for d, s, en, hi in LEVELS) + ";",
        "",
        "INSERT INTO layers (id, workspace_id, slug, name, role, hierarchy_id) VALUES",
        ",\n".join(f"  ({sql_str(lid)}, {sql_str(WORKSPACE_ID)}, '{slug}', {sql_str(name)}, '{role}', "
                   f"{sql_str(HIERARCHY_ID) if role == 'hierarchy' else 'NULL'})"
                   for slug, (lid, _, name, role) in LAYERS.items()) + ";",
        "",
        "INSERT INTO layer_versions (id, layer_id, version, status, source_file, published_at) VALUES",
        ",\n".join(f"  ({sql_str(vid)}, {sql_str(lid)}, 1, 'published', 'seed/demo-state', {sql_str(VALID_FROM)})"
                   for lid, vid, _, _ in LAYERS.values()) + ";",
        "",
        "-- The source beat layer has one beat without a code. An import would send it",
        "-- to review; the demo uses the code its position implies so the demo works.",
        "INSERT INTO units (hierarchy_id, depth, code, name, path, geom, layer_version_id, valid_from) VALUES",
    ]
    rows = []
    for depth in range(5):
        for u in units[depth]:
            rows.append(f"  ({sql_str(HIERARCHY_ID)}, {depth}, {sql_str(u['code'])}, {sql_str(u['name'])}, "
                        f"'{u['path']}', {sql_geom(polygon(u['ring']), True)}, "
                        f"{sql_str(LAYERS['territorial-units'][1])}, {sql_str(VALID_FROM)})")
    lines.append(",\n".join(rows) + ";")
    lines += ["", "INSERT INTO layer_features (layer_version_id, code, name, attributes, geom) VALUES"]
    rows = []
    for code, name, status, rings in forests:
        rows.append(f"  ({sql_str(LAYERS['forest'][1])}, {sql_str(code)}, {sql_str(name)}, "
                    f"'{json.dumps({'legal_status': status})}', {sql_geom(polygon(*rings), True)})")
    for code, name, kind, rings in exclusions:
        rows.append(f"  ({sql_str(LAYERS['exclusions'][1])}, {sql_str(code)}, {sql_str(name)}, "
                    f"'{json.dumps({'kind': kind})}', {sql_geom(polygon(*rings), True)})")
    lines.append(",\n".join(rows) + ";")
    files["load.sql"] = ("text", None, "\n".join(lines) + "\n")

    # Reference detections as SQL, for the reference check.
    ref_rows = []
    for rid, desc, (x, y), cls, beats, triage, fcode in refs:
        beat_arr = "ARRAY[" + ", ".join(f"'{b}'" for b in beats) + "]::ltree[]" if beats else "'{}'::ltree[]"
        ref_rows.append(f"  ({sql_str(rid)}, ST_SetSRID(ST_Point({r6(x)}, {r6(y)}), 4326), {sql_str(cls)}, "
                        f"{beat_arr}, {sql_str(triage)}, {sql_str(fcode)})")
    files["reference.sql"] = ("text", None, "\n".join([
        "-- Generated by seed/generate.py. Do not edit by hand.",
        "-- Reference detections and the result each one should get.",
        "",
        "CREATE TEMP TABLE reference_detections (",
        "    id text PRIMARY KEY,",
        "    geom geometry(Point, 4326) NOT NULL,",
        "    expected_forest_class text,",
        "    expected_beats ltree[] NOT NULL,",
        "    expected_triage text,",
        "    expected_forest_code text",
        ") ON COMMIT DROP;",
        "",
        "INSERT INTO reference_detections VALUES",
        ",\n".join(ref_rows) + ";",
    ]) + "\n")
    return files


def render(kind, name, content):
    if kind == "geojson":
        doc = {"type": "FeatureCollection", "name": name, "features": content}
        return json.dumps(doc, ensure_ascii=False, indent=1) + "\n"
    return content


def main():
    parser = argparse.ArgumentParser(description=__doc__.split("\n\n")[0])
    parser.add_argument("--check", action="store_true", help="fail if seed/demo-state/ is out of date")
    args = parser.parse_args()

    files = {name: render(*spec) for name, spec in outputs().items()}
    if args.check:
        stale = [n for n, text in files.items()
                 if not (OUT / n).exists() or (OUT / n).read_text(encoding="utf-8") != text]
        extra = sorted(p.name for p in OUT.glob("*") if p.name not in files and p.name != "README.md")
        if stale or extra:
            for n in stale:
                print(f"out of date: seed/demo-state/{n}", file=sys.stderr)
            for n in extra:
                print(f"not generated: seed/demo-state/{n}", file=sys.stderr)
            print("Run `make seed` and commit the result.", file=sys.stderr)
            return 1
        print("Demo state is up to date.")
        return 0

    OUT.mkdir(exist_ok=True)
    for name, text in files.items():
        (OUT / name).write_text(text, encoding="utf-8")
    print(f"Wrote {len(files)} files to {OUT.relative_to(OUT.parent.parent)}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
