# /// script
# requires-python = ">=3.11"
# dependencies = ["numpy>=2,<3", "pandas>=2.2,<3", "rasterio>=1.4,<2", "shapely>=2,<3", "pyshp>=2.3,<4", "h3>=4,<5", "pillow>=11,<13"]
# ///
"""Build the rolling tree-cover detection snapshot from the two public FIRMS feeds."""

from collections import Counter
from concurrent.futures import ThreadPoolExecutor
from datetime import UTC, datetime, timedelta
from io import BytesIO
from pathlib import Path
from urllib.request import Request, urlopen
from zipfile import ZipFile

import h3
import numpy as np
import pandas as pd
import shapefile
from shapely import intersects_xy, prepare
from shapely.geometry import shape

from forest import sample_tile, write_json

SITE = Path(__file__).resolve().parents[1]
FIRMS = "https://firms.modaps.eosdis.nasa.gov/data/active_fire"
NATURAL_EARTH = "https://naturalearth.s3.amazonaws.com/10m_cultural/ne_10m_admin_0_countries_ind.zip"
COLUMNS = ["latitude", "longitude", "acq_date", "acq_time", "confidence"]


def download(url):
    with urlopen(Request(url, headers={"User-Agent": "Open-FIMRA public data build"}), timeout=35) as response:
        return response.read()


def read_feed(sensor, now, start):
    url = f"{FIRMS}/noaa-{sensor + 19}-viirs-c2/csv/J{sensor}_VIIRS_C2_South_Asia_7d.csv"
    frame = pd.read_csv(BytesIO(download(url)), usecols=COLUMNS, dtype={"acq_time": str})
    frame["time"] = pd.to_datetime(frame.acq_date + " " + frame.acq_time.str.zfill(4),
                                   format="%Y-%m-%d %H%M", utc=True)
    latest = frame.time.max()
    if frame.empty or not now - timedelta(hours=36) <= latest <= now + timedelta(minutes=10):
        raise ValueError(f"Missing or stale NOAA-{sensor + 19} input (latest {latest})")
    if not frame.latitude.between(-90, 90).all() or not frame.longitude.between(-180, 180).all():
        raise ValueError(f"Invalid coordinates in NOAA-{sensor + 19} input")
    return frame[frame.time.between(start, now) & frame.confidence.isin(["nominal", "high"])].drop_duplicates(
        subset=["latitude", "longitude", "time"]
    )


def india_geometry():
    with ZipFile(BytesIO(download(NATURAL_EARTH))) as archive:
        name = "ne_10m_admin_0_countries_ind"
        with shapefile.Reader(shp=BytesIO(archive.read(f"{name}.shp")),
                              dbf=BytesIO(archive.read(f"{name}.dbf"))) as reader:
            india = next(shape(record.shape.__geo_interface__).buffer(0)
                         for record in reader.iterShapeRecords(fields=["ADM0_A3"])
                         if record.record["ADM0_A3"] == "IND")
    prepare(india)
    return india


def iso(value):
    return value.isoformat(timespec="seconds").replace("+00:00", "Z")


def collect():
    now = datetime.now(UTC)
    start = now - timedelta(days=7)
    with ThreadPoolExecutor(max_workers=3) as pool:
        feeds = [pool.submit(read_feed, sensor, now, start) for sensor in (1, 2)]
        india = pool.submit(india_geometry)
        points = pd.concat([feed.result() for feed in feeds], ignore_index=True)
        points = points[intersects_xy(india.result(), points.longitude.values, points.latitude.values)].reset_index(drop=True)
    points["tile_lat"] = ((np.ceil(points.latitude / 3) - 1) * 3).astype(int)
    points["tile_lon"] = (np.floor(points.longitude / 3) * 3).astype(int)
    tree = np.zeros(len(points), dtype=bool)
    with ThreadPoolExecutor(max_workers=12) as pool:
        for index, flag in pool.map(lambda group: sample_tile(*group), points.groupby(["tile_lat", "tile_lon"])):
            tree[index] = flag
    cells = Counter(h3.latlng_to_cell(point.latitude, point.longitude, 4) for point in points[tree].itertuples())
    return {
        "version": 1,
        "updatedAt": iso(now),
        "start": iso(start),
        "latestPass": iso(points.time.max()) if not points.empty else None,
        "latestTreePass": iso(points[tree].time.max()) if tree.any() else None,
        "count": int(tree.sum()),
        "cells": [
            {"count": count, "vertices": [[round(lon, 4), round(lat, 4)] for lat, lon in h3.cell_to_boundary(cell)]}
            for cell, count in sorted(cells.items())
        ],
    }


if __name__ == "__main__":
    snapshot = collect()
    destination = SITE / "public/data/live.json"
    temporary = destination.with_suffix(".tmp")
    write_json(temporary, snapshot)
    temporary.replace(destination)
    print(f"{snapshot['count']:,} tree-cover detections in {len(snapshot['cells']):,} cells")
