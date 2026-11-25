# Geo worker

The worker reads GeoPackage, zipped Shapefile, GeoJSON, KML and KMZ. It saves
draft layers and review items. GDAL reads the files and transforms their
coordinates. Shapely repairs geometry and checks how the shapes fit together.
The container supplies GDAL and the Python lock file pins the other libraries.

## Run and test

From the repository root:

```sh
make dev db-migrate geo-worker-up
make geo-test geo-test-integration
```

The first test suite runs in the GDAL container. The second signs in to the API,
uploads the synthetic demo files and compares the stored drafts with the seed
SQL. See the [API guide](../api/README.md#layer-imports) for the upload form.

## How imports work

- The API saves each file under a generated name in `FIMRA_UPLOAD_DIR`.
  Compose mounts the same directory read-only in the worker. Files remain there
  with their drafts so the original can be checked later.
- The API inserts a `geo_jobs` row in the same database transaction as the
  draft and audit entry. River still handles Go jobs. The Python worker polls
  its own table in the shared PostgreSQL database, using
  `FOR UPDATE SKIP LOCKED` so two workers cannot take the same job.
  Its lock lasts until draft data, review items and status commit together.
  If the process stops before commit, PostgreSQL rolls back and the job is
  available to another worker. A file that cannot be imported gets a failed
  status and a blocking review item.
- GeoJSON and KML use their file format's default coordinate system when
  implicit. A Shapefile needs its projection file. Geometry is stored in
  WGS 84 (EPSG:4326) and area in square metres using EPSG:7755. A source layer
  may contain up to 20,000 features and one million coordinates. ZIP and KMZ
  files may contain up to
  128 entries and 128 MiB after unpacking.
- Hierarchy drafts go into `draft_units`. They have their own paths, parent
  codes and parent paths. Each child layer can use a ready draft from the
  preceding level or the current published units there. A missing parent code
  gets a suggestion from the largest shared area. A missing unit code gets a
  suggestion based on its name, such as `B16` for `Beat 16`. Unresolved paths
  are saved empty for review. Matching and role grants read the live `units`
  table.
- Overlaps and gaps larger than one square metre go to review. The worker
  compares coverage against the parent footprints or the workspace boundary
  at the root level. When there is no reference footprint, it looks for
  enclosed gaps. A forest draft uses the selected coverage draft or the
  workspace's deepest current territorial level to check for forest outside
  the units. Interior holes and missing forest codes are recorded as review
  items while preserving the source features.
- Forest and hierarchy features are polygons. Exclusion and analysis layers
  can also hold points and lines. Non-hierarchy drafts store geometry and
  unmapped attributes in `layer_features`, linked to a draft version. Each
  review item goes into `layer_import_problems`. A large review list shows
  the first 2,000 items and a blocking item telling the user to fix and retry.
