# /// script
# requires-python = ">=3.11"
# dependencies = ["numpy>=2,<3", "pandas>=2.2,<3", "rasterio>=1.4,<2", "shapely>=2,<3", "pyshp>=2.3,<4", "h3>=4,<5", "pillow>=11,<13"]
# ///
"""Publish forest-filtered FIRMS counts and a coarse WorldCover display layer."""

import argparse
from collections import defaultdict
from concurrent.futures import ThreadPoolExecutor
from pathlib import Path
import json

import h3
import numpy as np
import pandas as pd
from PIL import Image
import rasterio
from rasterio.enums import Resampling
from rasterio.errors import RasterioIOError
from rasterio.windows import Window
from rasterio.transform import rowcol
import shapefile
from shapely import intersects_xy
from shapely.geometry import shape
from shapely.ops import unary_union

WORLD_COVER = "https://esa-worldcover.s3.eu-central-1.amazonaws.com/v200/2021/map"
FIRST_YEAR, LAST_YEAR = 2021, 2025
MONTHS = ["2025-11", "2025-12"] + [f"2026-{m:02}" for m in range(1, 7)]
WEST, SOUTH, EAST, NORTH = 63, 3, 102, 42
DISPLAY_PX = 72


def read_archive(folder, first_year, india):
    columns = ["latitude", "longitude", "acq_date", "acq_time", "type"]
    frames = []
    for file in sorted(folder.glob("*.csv")):
        frame = pd.read_csv(file, usecols=columns, dtype={"acq_date": str})
        month = frame.acq_date.str[5:7].astype(int)
        frame["season"] = frame.acq_date.str[:4].astype(int) - (month <= 6)
        selected = frame[(frame.type == 0) & ((month >= 11) | (month <= 6))
                         & frame.season.between(first_year, LAST_YEAR)]
        if not selected.empty:
            frames.append(selected)
    if not frames:
        raise ValueError(f"No input CSVs in {folder}")
    points = pd.concat(frames, ignore_index=True).drop_duplicates(subset=columns[:-1])
    points = points[intersects_xy(india, points.longitude.values, points.latitude.values)].copy()
    if sorted(points.season.unique()) != list(range(first_year, LAST_YEAR + 1)):
        raise ValueError(f"Missing seasons in {folder}")
    points["tile_lat"] = ((np.ceil(points.latitude / 3) - 1) * 3).astype(int)
    points["tile_lon"] = (np.floor(points.longitude / 3) * 3).astype(int)
    return points.reset_index(drop=True)


def tile_url(key):
    lat, lon = key
    tile_id = f"{'N' if lat >= 0 else 'S'}{abs(lat):02}{'E' if lon >= 0 else 'W'}{abs(lon):03}"
    return f"{WORLD_COVER}/ESA_WorldCover_10m_2021_v200_{tile_id}_Map.tif"


def remote():
    return rasterio.Env(GDAL_DISABLE_READDIR_ON_OPEN="EMPTY_DIR", GDAL_HTTP_MAX_RETRY="3",
                        GDAL_HTTP_RETRY_DELAY="1", GDAL_HTTP_TIMEOUT="25")


def sample_tile(key, points):
    with remote(), rasterio.open(tile_url(key)) as tile:
        if tile.crs.to_epsg() != 4326:
            raise ValueError(f"Unexpected CRS for {key}")
        row, col = rowcol(tile.transform, points.longitude.values, points.latitude.values)
        row, col = row.clip(0, tile.height - 1), col.clip(0, tile.width - 1)
        height, width = tile.block_shapes[0]
        blocks = defaultdict(list)
        for i, (y, x) in enumerate(zip(row // height, col // width)):
            blocks[y, x].append(i)
        tree = np.zeros(len(points), dtype=bool)
        for (y, x), idx in sorted(blocks.items()):
            off_y, off_x = y * height, x * width
            block = tile.read(1, window=Window(off_x, off_y, width, height))
            tree[idx] = block[row[idx] - off_y, col[idx] - off_x] == 10
    print(f"{key}: {len(points):,} detections, {len(blocks):,} windows", flush=True)
    return points.index, tree


def display_tile(key):
    try:
        with remote(), rasterio.open(tile_url(key)) as tile:
            overview = tile.read(1, out_shape=(DISPLAY_PX, DISPLAY_PX), resampling=Resampling.nearest)
            return key, overview == 10
    except RasterioIOError as error:
        # WorldCover does not publish empty ocean tiles.
        if "404" not in str(error):
            raise
        return key, np.zeros((DISPLAY_PX, DISPLAY_PX), dtype=bool)


def write_display(output, workers):
    display = np.zeros(((NORTH - SOUTH) // 3 * DISPLAY_PX, (EAST - WEST) // 3 * DISPLAY_PX, 4), dtype=np.uint8)
    grid = [(lat, lon) for lat in range(SOUTH, NORTH, 3) for lon in range(WEST, EAST, 3)]
    with ThreadPoolExecutor(max_workers=workers) as pool:
        for (lat, lon), tree in pool.map(display_tile, grid):
            x = (lon - WEST) // 3 * DISPLAY_PX
            y = (NORTH - lat - 3) // 3 * DISPLAY_PX
            display[y:y + DISPLAY_PX, x:x + DISPLAY_PX][tree] = [69, 135, 112, 190]
    Image.fromarray(display).save(output / "tree-cover.webp", lossless=True)


def write_json(path, payload):
    path.write_text(json.dumps(payload, separators=(",", ":")) + "\n")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    for name in ("noaa20", "snpp", "india", "summary"):
        parser.add_argument(f"--{name}", type=Path)
    parser.add_argument("--output", type=Path, required=True)
    parser.add_argument("--display-only", action="store_true")
    parser.add_argument("--workers", type=int, default=8)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    if args.display_only:
        write_display(args.output, args.workers)
        return
    if None in (args.noaa20, args.snpp, args.india, args.summary):
        parser.error("--noaa20, --snpp, --india and --summary are required for archive processing")
    with shapefile.Reader(str(args.india)) as source:
        india = unary_union([shape(record.shape.__geo_interface__).buffer(0)
                             for record in source.iterShapeRecords(fields=["ADM0_A3"])
                             if record.record["ADM0_A3"] == "IND"])
    noaa = read_archive(args.noaa20, LAST_YEAR, india)
    snpp = read_archive(args.snpp, FIRST_YEAR, india)
    noaa["sensor"], snpp["sensor"] = "NOAA-20", "Suomi NPP"
    points = pd.concat([noaa, snpp], ignore_index=True)
    groups = list(points.groupby(["tile_lat", "tile_lon"]))
    print(f"{len(points):,} vegetation detections across {len(groups)} tiles", flush=True)

    forest = np.zeros(len(points), dtype=bool)
    with ThreadPoolExecutor(max_workers=args.workers) as pool:
        for idx, tree in pool.map(lambda group: sample_tile(*group), groups):
            forest[idx] = tree
    write_display(args.output, args.workers)
    noaa_forest = points[forest & (points.sensor == "NOAA-20")].copy()
    month_totals = noaa_forest.acq_date.str[:7].value_counts().reindex(MONTHS, fill_value=0)
    cells = defaultdict(lambda: [0] * len(MONTHS))
    for item in noaa_forest.itertuples():
        cell = h3.latlng_to_cell(item.latitude, item.longitude, 4)
        cells[cell][MONTHS.index(item.acq_date[:7])] += 1
    features = []
    poster = ['<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 780 780">']
    for cell, counts in sorted(cells.items()):
        vertices = [[round(lon, 4), round(lat, 4)] for lat, lon in h3.cell_to_boundary(cell)]
        features.append({"type": "Feature", "properties": {"cell": cell, "counts": counts},
                         "geometry": {"type": "Polygon", "coordinates": [vertices + vertices[:1]]}})
        polygon = " ".join(f"{(lon - WEST)*20:.1f},{(NORTH - lat)*20:.1f}" for lon, lat in vertices)
        hue = "#ffe0ae" if sum(counts) >= 150 else "#ffb69b" if sum(counts) >= 45 else "#e87f77" if sum(counts) >= 10 else "#b25765"
        poster.append(f'<polygon points="{polygon}" fill="{hue}" stroke="#ffbda3" stroke-opacity=".38" stroke-width=".65"/>')
    poster.append("</svg>")
    (args.output / "season-map.svg").write_text("".join(poster))
    write_json(args.output / "hexagons.json", {"type": "FeatureCollection", "features": features})
    snpp_forest = points[forest & (points.sensor == "Suomi NPP")]
    trend = snpp_forest.season.value_counts().reindex(range(FIRST_YEAR, LAST_YEAR + 1), fill_value=0)
    summary = {"season": len(noaa_forest), "vegetation": len(noaa), "months": MONTHS,
               "monthTotals": month_totals.tolist(), "trend": trend.tolist(),
               "trendVegetation": snpp.season.value_counts().reindex(trend.index).tolist()}
    args.summary.parent.mkdir(parents=True, exist_ok=True)
    write_json(args.summary, summary)
    print(summary)


if __name__ == "__main__":
    main()
