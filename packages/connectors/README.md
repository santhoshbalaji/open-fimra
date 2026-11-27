# Connectors

This folder holds connector specifications and synthetic test fixtures. Never commit real observations.

## NASA FIRMS

The [Go connector](../../services/api/internal/firms/) uses the [area API](https://firms.modaps.eosdis.nasa.gov/api/area/) for `VIIRS_NOAA20_NRT`, `VIIRS_NOAA21_NRT` and the combined `MODIS_NRT` product, which continues for Aqua and still accepts Terra rows. Suomi NPP and Terra are retained for historical ingest and linking. Set `FIMRA_FIRMS_POLL_PRODUCTS` to override the live products and `FIMRA_FIRMS_POLL_INTERVAL` to change the five-minute cadence (minimum one minute). Live polls share identical requests for the same key, product, bounds and day range.

Backfill defaults to `VIIRS_NOAA20_SP,VIIRS_NOAA21_SP,MODIS_SP`. Override the list with `FIMRA_FIRMS_BACKFILL_PRODUCTS` or select products on the command line, including `VIIRS_SNPP_SP` for historical Suomi NPP data. Backfill uses the date form of the area API and requests at most five days per call. Save a workspace key or set `FIMRA_FIRMS_MAP_KEY` as a fallback. Requests include the key in the URL and the connector redacts it in reported errors.

[The fixtures](firms/fixtures/) use the FIRMS column layout with made-up observations in Demo State. The VIIRS replay file spans two months. The MODIS file covers numeric confidence and a time without leading zeroes. Tests serve these files from memory.

`viirs_snpp_sp.csv` and `modis_sp.csv` stand in for standard-processing files. They add the `type` column, and their `version` values are made up. Their records fall between January 1 and February 15, 2000, across several 5-day chunks and a month boundary. Three of them are the same observations as records in `viirs.csv` and `modis.csv`, less than a metre apart, to test linking. `make backfill-demo` reads them in place of FIRMS.

The demo file stores offsets as acquisition times relative to midnight UTC on January 8, 2000. For instance, January 7 at 22:00 means two hours before the anchor. `make demo-detections` moves the anchor to the start of the current UTC hour. A rerun within that hour reuses the keys. Imports in later hours create fresh demo observations.

See the [core API guide](../../services/api/README.md) for ingest commands.

## NOAA VIIRS I-band Enterprise Fire

The [Go connector](../../services/api/internal/nodd/) uses anonymous S3 ListObjectsV2 and ranged GETs against NOAA's NODD buckets `noaa-nesdis-n20-pds` and `noaa-nesdis-n21-pds` in `us-east-1`. Both publish `VIIRS_EFIRE_VIIRSI_EDR/YYYY/MM/DD/EFIRE-VIIRSI_v<version>_<n20|n21>_s<start>_e<end>_c<created>.nc`. Times in the key are UTC with tenths of a second. See [NOAA JPSS data access](https://www.star.nesdis.noaa.gov/jpss/DataAccess.php), the [AWS JPSS registry](https://registry.opendata.aws/noaa-jpss/) and the [NOAA Enterprise Active Fire user manual](https://www-cdn.eumetsat.int/files/2023-11/EFIRE_EUM_v2.0.docx_.pdf).

The operational files are NetCDF4/HDF5. The connector uses the MIT-licensed [go-native-netcdf](https://github.com/batchatco/go-native-netcdf) reader, verified on NOAA-20 and NOAA-21 granules with and without fires. Range requests read the `geospatial_bounds` attribute and the sparse `FP_*` fire-pixel columns on the `nfire` dimension. The fire mask and confidence in the sparse records preserve NOAA's QA without downloading the full swath QA rasters. `FP_Latitude`, `FP_Longitude`, `FP_FireRadiativePower`, `FP_Confidence`, `FP_FireMask`, `FP_PixelArea`, `FP_Line` and `FP_Sample` are in the operational files. The product provides a nominal 375 m I-band resolution and per-pixel area in km². Observation time comes from the granule start. The [fixtures](noaa-nodd/fixtures/) contain only synthetic Demo State points or an empty fire-pixel dimension.

### India bucket latency sample

On 27 November 2026 UTC, listed both buckets for 25–27 November and screened 2,071 available granules with starts in 06:00–11:00 and 18:00–23:00 UTC. The latter window on the 27th had yet to occur. Intersected each granule's `geospatial_bounds` with the [Natural Earth 1:50m India polygon](https://www.naturalearthdata.com/downloads/50m-cultural-vectors/50m-admin-0-countries/), then downloaded the 132 matching NetCDF4 files to confirm their bounds and group them by `start_orbit_number`. Empty granules were included. The difference between S3 `LastModified` and the acquisition start in the object name, to whole seconds, gives the following minutes.

| Satellite | Granules | Passes | Minimum | Median | P95 | Max |
|---|---:|---:|---:|---:|---:|---:|
| NOAA-20 | 66 | 13 | 20.3 | 28.5 | 47.4 | 48.6 |
| NOAA-21 | 66 | 10 | 20.4 | 28.7 | 46.3 | 49.0 |

Taking the median within each pass first, the median and P95 across passes were 25.7 and 46.6 minutes for NOAA-20, and 27.1 and 45.4 for NOAA-21. This sample measures the current object's bucket timestamp and can include replacement uploads. It covers selected orbit windows and excludes Open FIMRA polling and processing time. Comparing the same pixels against FIRMS would require a separate paired first-seen sample.

Data source: NOAA/NESDIS JPSS VIIRS Enterprise Active Fire, distributed through NOAA Open Data Dissemination. NODD data are open for public use. Credit NOAA and identify any modified records as derived data. See the [NODD usage terms](https://registry.opendata.aws/noaa-jpss/#license).

## Copernicus Sentinel-3 SLSTR

The [Go connector](../../services/api/internal/cdse/) searches the public CDSE OData catalogue for `SL_2_FRP___` near-real-time (`_NR_`) granules from Sentinel-3A and Sentinel-3B. It searches the union of workspace bounding boxes, checks returned swath footprints against actual workspace boundaries in PostGIS, and downloads `FRP_MWIR1km_standard.csv` directly from the CDSE S3 bucket. [Catalogue documentation](https://documentation.dataspace.copernicus.eu/APIs/OData.html) · [S3 access](https://documentation.dataspace.copernicus.eu/APIs/S3.html).

Standard MWIR uses the operational thermal grid at a nominal 1 km resolution and covers day and night. Its F1 channel also covers hot pixels that saturate S7. The CSV supplies per-pixel UTC time, scan and track dimensions, FRP in megawatts, and summary confidence. The connector preserves the confidence class, used channel, view geometry and other CSV columns as source evidence. EUMETSAT recommends the alternative MWIR for expert analysis of small fires in relatively cloud-free, homogeneous scenes. The SWIR-only feed needs separate quality screening and reconciliation with MWIR, including the South Atlantic Anomaly flags. This connector uses the standard MWIR summary, which can include cloud-edge false alarms and does not carry the full NetCDF confidence flags. [EUMETSAT product notice, pp. 5–6, 12–13](https://user.eumetsat.int/s3/ope-eup-strapi-media/Sentinel_3_Product_Notice_SLSTR_Level_2_Near_Real_Time_Fire_Radiative_Power_PB_3_24_cfb5a27ab2.pdf).

The archived sample of 200 India-intersecting granules had a median publication delay of about 105 minutes and a p95 of about 119 minutes after sensing end, estimated as sensing start plus the nominal three-minute granule duration. Those figures measure CDSE publication, before local polling and matching. In the sample the passes over India fell around 03:00–06:00 and 14:00–17:00 UTC, adding morning and night coverage to the platform.

Contains modified Copernicus Sentinel data [year of observation]. Use the UTC year of the pixel for derived outputs. [Copernicus attribution guidance](https://documentation.dataspace.copernicus.eu/notebook-samples/sentinelhub/data_integration/ship_detection_and_ais_identification.html#attribution-licensing).

See the [API guide](../../services/api/README.md#copernicus-sentinel-3-ingestion) for deployment and polling settings. Run `make cdse-test-integration` on a migrated database to exercise the fake catalogue and object store, catch-up, replay, incident grouping and feed health.
