# /// script
# requires-python = ">=3.11"
# dependencies = ["numpy>=2,<3", "pillow>=11,<13", "rasterio>=1.4,<2"]
# ///
"""Make self-hosted WebP tiles, a night globe poster, and two terrain crops."""
import argparse
from pathlib import Path
from concurrent.futures import ThreadPoolExecutor
from urllib.request import urlretrieve

import numpy as np
from PIL import Image
import rasterio
from rasterio.warp import transform
from rasterio.windows import Window

NIGHT = ("https://assets.science.nasa.gov/content/dam/science/esd/eo/images/"
         "imagerecords/144000/144898/BlackMarble_2016_3km.jpg")
DAY = ("https://gibs.earthdata.nasa.gov/wmts/epsg4326/best/BlueMarble_ShadedRelief/"
       "default/2004-07-01/500m/4")
SENTINEL = ("https://sentinel-cogs.s3.us-west-2.amazonaws.com/sentinel-s2-l2a-cogs/"
            "44/Q/NH/2026/3/S2C_44QNH_20260328_0_L2A/TCI.tif")
WEST, SOUTH, EAST, NORTH = 63, 3, 102, 42
PIXEL = 81.335, 20.455


def sample(im, longitude, latitude):
    h, w, _ = im.shape
    x = ((longitude + 180) / 360 * w).astype(int) % w
    y = ((90 - latitude) / 180 * h).astype(int).clip(0, h - 1)
    return im[y, x]


def save(img, path, quality):
    Image.fromarray(img).save(path, quality=quality, method=6)


def night_assets(source, output):
    image = np.asarray(Image.open(source).convert("RGB"))
    for z in range(4):
        count = 2 ** z
        res = count * 256
        position = (np.arange(res) + 0.5) / res
        lats = np.degrees(np.arctan(np.sinh(np.pi * (1 - 2 * position))))
        lons = position * 360 - 180
        raster = sample(image, lons[None, :], lats[:, None])
        for x in range(count):
            folder = output / "night" / str(z) / str(x)
            folder.mkdir(parents=True, exist_ok=True)
            for y in range(count):
                save(raster[y*256:(y+1)*256, x*256:(x+1)*256], folder / f"{y}.webp", 79)

    size = 768
    coord = (np.arange(size) + 0.5) / size * 2 - 1
    x, y = np.meshgrid(coord, -coord)
    radius = np.hypot(x, y)
    z = np.sqrt(np.clip(1 - radius**2, 0, 1))
    tilt = np.radians(18)
    lats = np.degrees(np.arcsin(y * np.cos(tilt) + z * np.sin(tilt)))
    lons = 81 + np.degrees(np.arctan2(x, z * np.cos(tilt) - y * np.sin(tilt)))
    globe = sample(image, lons, lats).astype(np.float32)
    shade = np.clip(0.54 + 0.46 * z + 0.23 * x, 0.35, 1)
    globe = globe * shade[..., None] + z[..., None] * [2, 5, 9]
    rim = np.exp(-((radius - 0.993) * 91)**2) * (0.32 + 0.68 * (y + 1) / 2)
    globe += rim[..., None] * [27, 61, 102]
    alpha = np.clip((1 - radius) * 400, 0, 1) * 255
    save(np.dstack((globe.clip(0, 255), alpha)).astype(np.uint8), output / "earth-night.webp", 82)


def day_assets(cache, output):
    # NASA GIBS EPSG:4326 WMTS, level 4: 512 px tiles spanning 18 degrees.
    tile_size, degrees = 512, 18
    left, top = (WEST + 180) // degrees, (90 - NORTH) // degrees
    right, bottom = (EAST + 180) // degrees, (90 - SOUTH) // degrees
    keys = [(x, y) for x in range(left, right + 1) for y in range(top, bottom + 1)]

    def fetch(key):
        x, y = key
        file = cache / f"blue-marble-{x}-{y}.jpg"
        if not file.exists():
            urlretrieve(f"{DAY}/{y}/{x}.jpeg", file)
        return file, x, y

    mosaic = Image.new("RGB", ((right - left + 1) * tile_size, (bottom - top + 1) * tile_size))
    with ThreadPoolExecutor(max_workers=4) as pool:
        for file, x, y in pool.map(fetch, keys):
            with Image.open(file) as tile:
                mosaic.paste(tile, ((x - left) * tile_size, (y - top) * tile_size))
    x = lambda lon: ((lon + 180) / degrees - left) * tile_size
    y = lambda lat: ((90 - lat) / degrees - top) * tile_size
    mosaic.crop((x(WEST), y(NORTH), x(EAST), y(SOUTH))).resize(
        (640, 640), Image.Resampling.LANCZOS
    ).save(output / "terrain.webp", quality=62, method=6)
    with rasterio.Env(GDAL_DISABLE_READDIR_ON_OPEN="EMPTY_DIR", GDAL_HTTP_MAX_RETRY="3"):
        with rasterio.open(SENTINEL) as tile:
            xs, ys = transform("EPSG:4326", tile.crs, [PIXEL[0]], [PIXEL[1]])
            row, col = tile.index(xs[0], ys[0])
            # 192 × 192 native 10 m pixels = a 1.92 km square.
            patch = tile.read([1, 2, 3], window=Window(col - 96, row - 96, 192, 192))
    rgb = np.moveaxis(patch, 0, 2)
    save(rgb, output / "sentinel.webp", 92)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--cache", type=Path, required=True)
    parser.add_argument("--output", type=Path, required=True)
    args = parser.parse_args()
    args.output.mkdir(parents=True, exist_ok=True)
    args.cache.mkdir(parents=True, exist_ok=True)
    night = args.cache / "black-marble.jpg"
    if not night.exists():
        urlretrieve(NIGHT, night)
    night_assets(night, args.output)
    day_assets(args.cache, args.output)



if __name__ == "__main__":
    main()
