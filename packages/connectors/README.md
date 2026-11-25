# Connectors

This folder holds connector specifications and synthetic test fixtures. Never commit real observations.

## NASA FIRMS

The [Go connector](../../services/api/internal/firms/) uses the [area API](https://firms.modaps.eosdis.nasa.gov/api/area/) for `VIIRS_SNPP_NRT`, `VIIRS_NOAA20_NRT`, `VIIRS_NOAA21_NRT` and `MODIS_NRT`. Backfill uses the standard-processing products `VIIRS_SNPP_SP`, `VIIRS_NOAA20_SP`, `VIIRS_NOAA21_SP` and `MODIS_SP`, with the area API's date form and at most 5 days per request. Set your own `FIMRA_FIRMS_MAP_KEY` to enable live polling. The key is part of the request URL and is redacted from reported errors.

[The fixtures](firms/fixtures/) use the FIRMS column layout with made-up observations in Demo State. The VIIRS replay file spans two months. The MODIS file covers numeric confidence and a time without leading zeroes. Tests serve these files from memory.

`viirs_snpp_sp.csv` and `modis_sp.csv` stand in for standard-processing files. They add the `type` column, and their `version` values are made up. Their records fall between January 1 and February 15, 2000, across several 5-day chunks and a month boundary. Three of them are the same observations as records in `viirs.csv` and `modis.csv`, less than a metre apart, to test linking. `make backfill-demo` reads them in place of FIRMS.

The demo file stores offsets as acquisition times relative to midnight UTC on January 8, 2000. For instance, January 7 at 22:00 means two hours before the anchor. `make demo-detections` moves the anchor to the start of the current UTC hour. A rerun within that hour reuses the keys. Imports in later hours create fresh demo observations.

See the [core API guide](../../services/api/README.md) for ingest commands.
