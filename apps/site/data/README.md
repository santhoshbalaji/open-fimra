# Sources and methods

The public site presents an archive of satellite observations across India. A
detection represents an active-fire or thermal-anomaly pixel. Repeated detections
can come from one fire. Surface conditions, clouds and the timing of overpasses
affect which fires are detected. The number of detections measures neither the
number of fires nor the area burned.

## Detection series

| View | NASA FIRMS standard-processing product | Time coverage |
| --- | --- | --- |
| Night globe and season map | `VIIRS_NOAA20_SP` | November 2025–June 2026 |
| Five-season comparison | `VIIRS_SNPP_SP` | November–June in each season from 2021–22 to 2025–26 |

The source CSVs cover a South Asia bounding box (68°E–98°E, 6°N–38°N).
Within each product the pipeline keeps only `type == 0` (presumed vegetation
fire), restricts dates to each season, and deduplicates on numeric latitude and
longitude, acquisition date and UTC acquisition time. The Natural Earth 1:10m
India point-of-view country geometry is used for the spatial filter. It is never
published or rendered. Physical land and coastline come from the NASA terrain
imagery.

The forest proxy is ESA WorldCover 2021 v200 class 10 at the detection centre.
The script groups locations by COG and window, reads native 10 m blocks through
HTTP ranges and queries each centre. The check uses tree cover only and can
include plantations and other managed trees. It uses a single 2021 reference
layer for all five seasons. It does not classify legal forest,
and a centre falling outside a tree-cover pixel does not establish that the
whole satellite footprint was outside tree cover. The vegetation denominator is
computed after the product, time, type, duplicate and country filters.
The map groups detections by month and H3 resolution 4 cell. The downsampled
tree-cover layer is for display and is not used for classification. The 2025–26
NOAA-20 series contains 190,010 detections over tree cover out of 583,798
vegetation-fire detections (32.5%).

We acknowledge the use of data and imagery from LANCE FIRMS operated by NASA's
Earth Science Data and Information System (ESDIS).
[NASA FIRMS product and citation information](https://www.earthdata.nasa.gov/data/tools/firms).
The published national percentages are from the Forest Survey of India, India
State of Forest Report 2023, Volume 1, tables 2.3 and 4.14, and use the report's
definitions. [India State of Forest Report 2023](https://fsi.nic.in/uploads/isfr2023/isfr_book_eng-vol-1_2023.pdf).

ESA WorldCover 2021 is produced by the ESA WorldCover consortium and made
available under [CC BY 4.0](https://creativecommons.org/licenses/by/4.0/).
[Natural Earth](https://www.naturalearthdata.com/about/terms-of-use/) is public
domain. NASA Black Marble 2016 and Blue Marble shaded relief are NASA Earth
Observatory imagery. Black Marble uses VIIRS day/night band observations.
[Black Marble imagery](https://science.nasa.gov/earth/earth-observatory/earth-at-night/maps/).
[Blue Marble via NASA GIBS](https://www.earthdata.nasa.gov/data/projects/gibs).

The ground example contains modified Copernicus Sentinel data 2026, provided
under the [Copernicus Sentinel terms](https://sentinels.copernicus.eu/web/sentinel/terms-conditions).
It uses true colour from Sentinel-2C L2A scene `S2C_44QNH_20260328_0_L2A`, sampled
at native 10 m resolution. The 375 m square and its centre are illustrated
over that imagery. They represent a nominal nadir pixel, independent of the
predicted pass, with no claim that a fire occurred there.

## Rebuild the processed assets

Run the following from the repository root with Python available through uv.
The raw data, shapefile and imagery cache can live outside the repository.

```sh
uv run apps/site/scripts/forest.py \
  --noaa20 "$RESEARCH/firms/VIIRS_NOAA20_SP" \
  --snpp "$RESEARCH/firms/VIIRS_SNPP_SP" \
  --india "$RESEARCH/ne/ne_10m_admin_0_countries_ind.shp" \
  --output apps/site/public/data \
  --summary apps/site/data/season.json

uv run apps/site/scripts/imagery.py \
  --cache "$RESEARCH/imagery" --output apps/site/public/media
```

The imagery asset build cuts the 2016 Black Marble 3 km image into four levels
of tiled Web Mercator WebP, renders a globe poster and assembles the terrain
backdrop. The static Astro build reads the committed outputs. For the orbit it
fetches two-line elements for catalogue number 43013 from
[CelesTrak](https://celestrak.org/), using a committed fallback when unavailable.
Satellite.js propagates the orbit, searches for the first estimated daytime
pass within a nominal half-swath of central India, then samples the track and
its cross-track footprint. If the elements are over seven days from their
epoch, the site depicts a historical simulation anchored to that epoch.
The window, lighting and nominal swath approximations serve story illustration.

`make site-build` installs from the site lockfile and writes static files to
`apps/site/dist`. `make site-dev` starts Astro's local development server.
Both accept the repository's `PNPM` override.

## Weekly update and Pages

The public NOAA-20 and NOAA-21 FIRMS South Asia CSVs use near-real-time
processing. The refresh keeps nominal- and high-confidence records acquired in
the preceding seven days and deduplicates within each sensor. The files omit
the archive's fire type field. The India point-of-view polygon is downloaded
from Natural Earth at build time, used only for clipping, and discarded.
The latest pass timestamp represents the latest qualifying acquisition over
India. Overpasses with no reported observation cannot be timed from these CSVs.

The weekly refresh uses the same native 10 m class-10 lookup as the archive.
It groups points by 3° WorldCover tile and COG block, then samples only blocks
that contain detections. At most twelve tiles are read concurrently. The
27 November 2026 run finished in 77 seconds on a Mac M2 Pro, including
downloads. It retained 1,708 detections in 435 resolution-4 H3 cells. Each cell
publishes its aggregate count and hexagon geometry. Source coordinates and the
country polygon are discarded. Public map colour and opacity step through a
logarithmic scale capped at 150 detections per cell for the archive and 30 for
the week.

To refresh locally, run `uv run --python 3.12 apps/site/scripts/live.py` from
the repository root. Both feeds and all WorldCover lookups must succeed before
the script replaces the snapshot. The workflow runs every three hours, on
changes to the site on main and on manual dispatch. On failure it annotates the
run and uses the newest saved or publicly deployed snapshot, preserving its
original time. The committed sample is the bootstrap fallback. The page shows
the date even with scripting disabled and flags snapshots at least seven hours
old when scripting is available.

After merging, a maintainer selects **Settings → Pages → Build and deployment →
Source → GitHub Actions**, then runs **Public site** from the Actions tab. The
workflow builds with `SITE_BASE=/open-fimra/` and publishes the static output to
the `github-pages` environment. It uses GitHub's built-in Pages permissions and
requires no API keys or configured secrets.
