# Geo worker

The worker imports layers, writes file exports, converts forms to and from
XLSForm and draws situation reports as PDF files. It reads GeoPackage, zipped
Shapefile, GeoJSON, KML and KMZ, and saves draft layers and review items. It
writes exports in eight formats from rows the core API has already chosen. GDAL reads the files and transforms their
coordinates. Shapely repairs geometry and checks how the shapes fit together.
The container supplies GDAL and the Python lock file pins the other libraries.
The image also holds `formcheck`, the form validator built from
[packages/form-schema](../../packages/form-schema/README.md).

## Run and test

From the repository root:

```sh
make dev db-migrate geo-worker-up
make geo-test geo-test-integration
```

The first test suite runs in the GDAL container. The second signs in to the API,
uploads the synthetic demo files and compares the stored drafts with the seed.
It also publishes a changed beat layer and checks detection matches, grants
and alert history. The worker handles the upload and review. The API publishes
the version and re-matches detections through the Go job queue. See the
[API guide](../api/README.md#layer-imports) for the upload form.

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

## How exports work

- The API saves each request in `exports` and the permitted rows in
  `export_rows`, in one transaction. The worker claims a pending export with
  `FOR UPDATE SKIP LOCKED`, reads its rows by key, writes the file and marks it
  ready in the same transaction. It never decides which rows a user may see.
  The [API guide](../api/README.md#how-the-rows-are-chosen) explains how the
  database stops the row set from growing.
- Files go to `FIMRA_EXPORT_DIR`, mounted read-write at `/exports`. The worker
  writes into a `.part` directory and moves the finished file into place.
- GeoPackage and Excel keep the note in `export_note` and `export_fields`
  tables. Shapefile, GeoJSON, FlatGeobuf and both CSV formats are zipped with a
  `README.txt` note. KML is a KMZ file with `doc.kml` and `README.txt`.
- Date and time fields are native in GeoPackage, GeoJSON and FlatGeobuf. Other
  formats hold UTC text such as `2026-01-31T09:45:00Z`. CSV files come with a
  `.csvt` file of column types. The longitude and latitude CSV has no
  coordinate system in the file, and the note gives it.
- Shapefile names are limited to 10 characters. Fixed fields have set short
  names, such as `det_id` and `obs_time`. Hierarchy fields keep the first seven
  characters of the level and end in `_cd` or `_nm`, with a digit when two
  names would clash.
- Excel needs no extra library: GDAL's XLSX driver writes it. The GDAL build
  in the container has the plain KML driver, which reads back only each
  placemark's name and description. QGIS builds with LIBKML show every field.
  `readback.py` lists the KML schema fields for that reason.

Run `python readback.py FILE` in the container to describe any export with
OGR. `make export-test-integration` does this for every format it downloads.

## How situation reports are drawn

- The API works out each report with the requester's grants and saves it in
  `situation_reports`, in the request's transaction. The worker claims a
  pending report with `FOR UPDATE SKIP LOCKED`, draws the saved report with
  [situation_report.py](situation_report.py) and stores the PDF and its page
  count in the same row and transaction. It never reads alerts, detections or
  grants, so it cannot add anything the requester may not see. A report that
  cannot be drawn is marked failed with a plain-English reason.
- [ReportLab](https://www.reportlab.com/opensource/) writes the PDF. It is
  open source under the BSD licence, which can be combined with the AGPL, runs
  inside the worker with no outside service, and is pinned in the lock file
  with its dependencies, including HarfBuzz bindings for text shaping. It draws tables
  that continue across pages and vector shapes, so the map stays sharp when
  printed. A browser-based renderer would need a headless browser in the
  image, and an HTML renderer such as WeasyPrint needs system libraries for
  text layout; both are much larger for the same result.
- The map is drawn from the shapes saved in the report: the unit's outline,
  the units one level down, published forest and the alerts. It uses a simple
  projection with north up that keeps shapes true near the centre of the
  unit. The web map and report use the same marker shapes and age colours.
  A slash marks closed alerts and incidents in both browser print and PDF.
  Labels that would cover each other are left out.
- Pages are A4 with a footer naming the report and its page number. The image
  includes Noto Sans and Noto Sans fonts for Devanagari and Bengali under the
  SIL Open Font License. The worker embeds the fonts it uses in each PDF and
  shapes the script text with HarfBuzz. It uses the light colours from
  [tokens.json](../../docs/design-system/tokens.json).
- `make geo-test` draws synthetic reports, reads them back with pypdf and
  checks their text and page count. `make situation-report-test-integration`
  makes a report as each demo user through the API, waits for this worker and
  checks each PDF with [report_check.py](report_check.py). pypdf is in the
  lock file for these checks only.

## How XLSForm conversion works

XLSForm is the spreadsheet form format of ODK, which many departments already
know. The worker converts an XLSForm workbook into a draft form in the
[Open FIMRA form format](../../packages/form-schema/SPEC.md), and writes a form
back out as a workbook. [xlsform.py](xlsform.py) maps the sheets and
[xlsform_expr.py](xlsform_expr.py) translates the expressions.

### Commands

```sh
make xlsform-import FILE=path/to/form.xlsx          # draft form and review report in .xlsforms/
make xlsform-export FILE=packages/form-schema/examples/first-report.json
make xlsform-import FILE=form.xlsx OUT=some/folder  # write somewhere else
make xlsform-odk-check                              # export every example and convert it with pyxform
make db-up db-migrate xlsform-test-integration      # run conversion jobs on the database
```

Both conversions write a `.report.txt` file beside their output and print it.
`.xlsforms/` is ignored by Git, and so are all `.xlsx` files: spreadsheets are
written by commands and tests, never committed. `make geo-test` runs the unit
tests, including the round trip of every example form.

### Reading the workbook

- Only `.xlsx` workbooks are read. Save an `.xls` file or a Google Sheet as an
  Excel Workbook first.
- [openpyxl](https://openpyxl.readthedocs.io/) reads and writes the workbook,
  with [defusedxml](https://github.com/tiran/defusedxml) so that a crafted file
  cannot use XML entity tricks. Both are small, pure Python and under permissive
  licences (MIT and the Python licence), and openpyxl needs only `et-xmlfile`.
  GDAL's spreadsheet driver is not used here: it reads each sheet as a table
  of features with guessed column types, while the converter needs every cell
  as written and its sheet row for the review report.
- A workbook may be up to 10 MB, unpack to at most 64 MB in 512 parts, and have
  at most 10,000 rows on a sheet.

### One validator

Every draft is checked with the Go validator from `packages/form-schema`,
built into the image as the small `formcheck` command
([cmd/formcheck](../../packages/form-schema/cmd/formcheck/main.go)). Compose
passes that folder to the image build as a second build context. The worker
sends the draft as JSON on standard input and reads back the same problems
the core API and the web app would report. There is no Python copy of the
rules to drift from the others. The importer also asks the validator about
each pattern on its own, so a pattern that the form format does not allow is
left out with the validator's own message instead of failing the whole form.

A draft that the validator refuses is never saved: the job fails and the
problems go into its review report.

### The review report

Each entry names the sheet, the row, the column and the question's key where
they apply, and says in plain English what was changed or left out and why.
For example:

```
- survey sheet, row 5 (staff_count): “2” in the default column was not imported, because the form format has no default answers.
- survey sheet, row 13 (cause_other): The relevant expression “${cause} = 'other' and ${patrol_date} = today()” was not imported, because it compares “patrol_date” with today(), and the form format compares answers only with fixed values. It is always shown now.
```

Nothing is dropped without an entry. This includes question types the format
does not have, such as notes, barcodes, audio and the automatic `start` and
`end` values. It also covers columns such as `default`, `required_message`,
media and `bind::` columns, `or_other` lists, lists from files, unknown
appearances and sheets, settings, and every expression or part of a
constraint that cannot be translated exactly. Renamed keys and choices are
listed too. A
question shown only when a question that was left out is answered keeps the
question and loses the condition, with an entry saying so.

An export also gives notes, such as which Open FIMRA columns ODK ignores and
that ODK cannot fill in values from the alert.

### What maps to what

| Form format | XLSForm |
|---|---|
| `id`, `version`, English title | settings `form_id`, `version`, `form_title` |
| Section, group | top-level `begin_group`, nested `begin_group`. Questions outside any group go into a section called “Questions”. |
| Repeating group | `begin_repeat` |
| Description of a section or group | `hint` on the group row |
| `text`, `number`, `date`, `time`, `date-time` | `text`, `integer` (no decimal places) or `decimal`, `date`, `time`, `dateTime` |
| `single-choice`, `multiple-choice` | `select_one list`, `select_multiple list` and the choices sheet |
| `point`, `track`, `polygon` | `geopoint`, `geotrace`, `geoshape` |
| `photo`, `signature`, `file` | `image`, `image` with appearance `signature`, `file` |
| `calculated` | `calculate` with a `calculation` |
| `alert-value` | a read-only question of the matching type, with `fimra_alert_value` |
| `required`, `requireWhen` | `required` as `yes` or as an expression |
| `showWhen` | `relevant` |
| Limits, patterns, `notInFuture` | `constraint`, such as `. >= 0 and . <= 100`, `string-length(.) <= 80`, `regex(., '^[0-9]{10}$')`, `. <= today()`, `count-selected(.) >= 1` |
| `patternHint` | `constraint_message` on a text question |
| Cascading `parent` | `choice_filter` written as `column = ${parent}`, with the parent's choice in that column of the choices sheet |
| Translated texts | `label::Hindi (hi)` style columns, with `default_language` in settings |
| Widgets | appearances `multiline`, `counter`, `minimal` and `columns-pack` for paragraph, stepper, dropdown and buttons |

Conditions and calculations use the same parts of XPath both ways: `${name}`,
`=`, `<` and the other comparisons, `and`, `or`, `not()`, `selected()`,
`date()`, `date-time()`, `decimal-time()`, `+`, `-`, `*`, `div`, `sum()`,
`count()`, `round()` around a whole calculation for its decimal places,
`area(…) div 10000` for hectares, `distance(…) div 1000` for kilometres and
`(decimal-date-time(end) - decimal-date-time(start)) * 24` for hours. A plain
`area()` or `distance()` keeps its ODK meaning of square metres or metres.
Two details keep meanings exact:

- In XLSForm, `${f} != 'x'` is true when `f` is unanswered, and the form
  format's `not-equals` is not. So `!=` imports as `not` around `equals`, and
  `not-equals` exports as `(${f} != '' and ${f} != 'x')`.
- ODK finds a `regex()` pattern anywhere in the answer, while the form format
  matches the whole answer. Only patterns written between `^` and `$` import,
  and exports add them.

What XLSForm has no column for goes in extra survey columns, which ODK tools
ignore: `fimra_stage`, `fimra_unit`, `fimra_decimals`, `fimra_min_count`,
`fimra_max_count` (fewest and most entries, photos or files),
`fimra_max_size_mb`, `fimra_accept`, `fimra_max_accuracy`, `fimra_alert_value`,
`fimra_widget`, `fimra_width`, `fimra_collapsed` and `fimra_item_label`. When a
workbook has no `fimra_max_count` column, an `image` or `file` question takes
one photo or file, as in ODK. The `open_fimra` sheet holds the title's
translations, the description, the languages, the stages and the pages, one
row each.

### Round trips

Every example form in `packages/form-schema/examples`, including the five
starter-pack drafts, is exported, imported again and compared. The test passes
when the result is equivalent to the original, which means equal in every
property and order except:

- `$schema`, which only helps editors;
- the order of `layout.items`, which hold one display setting per item;
- a shared list used by only one question, which is the same as that question
  holding the choices itself. A list named after its only question imports as
  the question's own choices.

The round trip also gives an empty review report. Numbers are compared by
value, so `1` and `1.0` are equal.

### Conversion jobs

The API saves a request in `form_conversions` (migration 22). An import row
holds the uploaded workbook and its file name, and an export row holds a form
definition. The worker claims a pending row with `FOR UPDATE SKIP LOCKED`,
converts it and saves the result in the same transaction: the draft form, or
the workbook, and the review report. A failed job keeps its reason in
`error_message` and the details in `report`. Files are small, so both
workbooks live in the row rather than in the upload or export folders.

The forms tables and endpoints come with the forms slice, so no endpoint uses
these jobs yet. Wiring them in takes a few steps:

1. An upload endpoint saves the workbook in a new `form_conversions` row, with
   the workspace and the requester's subject, in the same transaction as its
   audit entry.
2. A status endpoint returns the row's status, report and error to the same
   requester. When it is ready, the forms service copies `draft_form` into a
   new draft form version and may then delete the row.
3. A download endpoint inserts an export row from a form version and, when it
   is ready, serves `result_file` as
   `application/vnd.openxmlformats-officedocument.spreadsheetml.sheet`.
