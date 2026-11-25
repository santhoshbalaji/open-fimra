# Core API

The core service uses the development sign-in server to verify access tokens. It reads each signed-in user's role grants from PostgreSQL. River workers run a heartbeat and a feed health check on startup and every five minutes. The heartbeat leaves application data alone.

## Run locally

From the repository root, with Go 1.26 or later:

```sh
make dev
make db-migrate
make api-test-integration
make demo-detections
make api-run
```

The integration tests check sign-in with all four demo users. They also exercise reference matching, replay a synthetic feed across two months and decode detection tiles to check access and filters. They run a backfill three times, make a feed go silent on a fake clock and check the feed health endpoint. Run them before starting the API, so the test worker can pick up its scheduled heartbeat. `make api-test` and `make api-lint` run without the development stack.

| Variable | Development default |
|---|---|
| `FIMRA_DATABASE_URL` | `postgres://fimra:fimra-dev@127.0.0.1:15432/fimra?sslmode=disable` |
| `FIMRA_OIDC_ISSUER` | `http://127.0.0.1:18180/realms/open-fimra` |
| `FIMRA_OIDC_AUDIENCE` | `open-fimra-api` |
| `FIMRA_HTTP_ADDR` | `127.0.0.1:18090` |
| `FIMRA_FIRMS_MAP_KEY` | Empty, which disables live polling |
| `FIMRA_UPLOAD_DIR` | `.geo-uploads` (at the repository root when using Make) |
| `FIMRA_EXPORT_DIR` | `.geo-exports` (at the repository root when using Make). The geo worker writes export files here. |
| `FIMRA_EXPORT_TTL` | `24h`. How long a finished export can be downloaded, as a Go duration of at least one minute. |
| `FIMRA_CORS_ORIGINS` | `http://127.0.0.1:4200,http://localhost:4200` |
| `FIMRA_SETUP_CODE` | Empty. On a fresh install the API then prints a one-time setup code in its log. See [First-run setup](#first-run-setup). |
| `FIMRA_SETTINGS_KEY` | Empty, which turns encrypted settings off. A base64-encoded 32-byte key, for example from `openssl rand -base64 32`. |
| `FIMRA_SETTINGS_OLD_KEYS` | Empty. Earlier settings keys, separated by commas, during a key rotation. |

When run through Make, the database URL defaults to `DATABASE_URL` and the issuer uses `FIMRA_KEYCLOAK_PORT`. Use these defaults with the local stack.

## Endpoints

- `GET /healthz` returns 200 while the service is running.
- `GET /readyz` returns 200 when PostgreSQL responds and 503 when it cannot be reached.
- `GET /api/v1/workspaces/{slug}/feeds` returns feed health for workspace administrators. See [Feed health](#feed-health).
- `GET /api/v1/me` takes an access token in the `Authorization: Bearer` header. It returns the caller's `subject`, `username` and `roleAssignments`. Each assignment includes the workspace slug and name, hierarchy slug, role, unit code and name, level slug, path and beat count. A grant to one beat counts that beat. A signed-in user without grants gets an empty list. A missing or rejected token gets a JSON error and a bearer challenge with HTTP 401.

The service finds the signing keys through OpenID Connect discovery and caches them. It checks the issuer, audience and token times. It allows 30 seconds for differences between clocks.

## Control-room summary

`GET /api/v1/workspaces/{slug}/summary` accepts the alert queue's `hours` (1–720, default 168), `class` and `unitPath`. A workspace grant is required. Counts cover all matching records, including those on later queue pages.

| Field | Meaning |
|---|---|
| `detections`, `today` | Counts by forest class. Both use observation time and the same grants as detection tiles. The first uses the selected window. Today runs from midnight in India Standard Time (`Asia/Kolkata`) through the current time, independently of the window. Both apply the class and unit filters. |
| `queues`, `steps` | Routed alerts by queue and current workflow step. Grants and filters use the routing snapshot saved with each alert, including after a layer is published. Review and triage require a covering review grant. A missing count means zero. |
| `active`, `pendingReports` | Counts of alerts at a nonterminal step and alerts whose current step has the report-pending flag, respectively. |
| `overdue` | An alert counts once if the time since it entered its current step is strictly greater than the shortest non-null limit on an outgoing transition. At the deadline it is still on time. A step with no limit cannot be overdue. |
| `responseSeconds` | Median and 90th percentile of seconds from alert creation to first acknowledgment, arrival at site and control. Each milestone includes only alerts that reached it. An empty sample gives null for both values. |
| `feedHealth` | Current source, product and status from the same feed facts and thresholds as the feeds endpoint. Root grants get the workspace-wide list (empty before the first poll). Other grants get null. |

The demo budget is 500 ms at the 95th percentile. The integration suite times 20 signed summary requests per granted demo account against the local database. This includes token verification and response encoding. The handler runs one counts query and one additional feed query for a root grant. With 11 demo detections and seven routed alerts, a local run measured 5.73 ms at the 95th percentile for the state administrator, 2.35 ms for the division officer and 2.79 ms for the beat guard. CI reports fresh measurements for each run.

## Detection tiles

`GET /api/v1/workspaces/{slug}/tiles/detections/{z}/{x}/{y}.mvt` takes a bearer token. It returns a Mapbox vector tile with content type `application/vnd.mapbox-vector-tile` and one layer named `detections`. An allowed request with no features returns HTTP 204. A missing or invalid token gets 401. A signed-in caller without a role in the workspace gets 403.

A grant covers matched paths at or below the granted unit in that hierarchy. A grant at a hierarchy root covers every match in the workspace, including matches with no unit.

- `hours` is the number of hours to look back from the current time. It defaults to 168 and accepts integers from 1 through 720.
- `unitPath` limits matches to a whole unit path prefix, such as `ds.c1.d1`.
- `classes` is a comma-separated list. By default the tile includes `inside`, `near_edge` and `fixed_heat_source`. Add `outside` to the list to show those points too.
- The cross-origin allowlist accepts a comma-separated list of origins in `FIMRA_CORS_ORIGINS`. Allowed origins can send the `Authorization` header after a browser preflight.

| Properties | Meaning |
|---|---|
| `id`, `observed_at` | Detection ID and observation time in Unix seconds |
| `age` | 0 for under 6 hours, 1 for 6 to under 24 hours, 2 for 1 to under 3 days, and 3 for 3 days or more |
| `forest_class`, `triage` | Forest class and triage reason. The reason is empty when no triage is needed. |
| `beat_code`, `beat_name`, `range_name`, `division_name` | Names and beat code from the first hierarchy by slug at the time of matching. Missing values are empty. |
| `source`, `product`, `frp_mw`, `confidence` | Feed, product, fire radiative power in megawatts, and the feed's confidence text. The tile omits missing power or confidence values. |

After `make demo-detections`, a tile that covers the demo workspace can be requested with a demo user's signed-in token.

```sh
curl -H "Authorization: Bearer $TOKEN" \
  'http://127.0.0.1:18090/api/v1/workspaces/demo/tiles/detections/8/189/116.mvt?classes=inside,near_edge,outside,fixed_heat_source' \
  -o /tmp/detections.mvt
```

## FIRMS ingestion

A River job starts polling on service startup and runs every 15 minutes. It requests two UTC calendar days for each of the four products and each workspace with a boundary and a map key. Each poll reads the workspace's key from its encrypted settings, so a key saved in the wizard is used at the next poll without a restart. A workspace without a saved key uses `FIMRA_FIRMS_MAP_KEY`, so installations from before the wizard keep working. A workspace with neither is skipped. A saved key that cannot be decrypted fails each feed's run with the reason, which feed health then shows. The database keeps one detection per feed observation and matches each new detection to every workspace in range. Each poll, backfill chunk or import records its kind, time window, counts and outcome in `ingest_runs`.

The poller, backfill and connection test share one rate limiter for each key. NASA allows 5,000 transactions in each 10 minutes per key, and says a larger request may count as more than one. The limiter counts one transaction for each day a request asks for, which overestimates on purpose. It counts only the requests of its own process, so a backfill run from the command line has its own share.

Use `make ingest-fixture FILE=path/to/file.csv PRODUCT=MODIS_NRT` to import a file without calling FIRMS. The product defaults to `VIIRS_SNPP_NRT`. `make demo-detections` uses the synthetic demo file and shifts its observation times to the current hour.

## Layer imports

Use a bearer token for an account with the `state_admin` role at a workspace
root. Start the [geo worker](../geo-worker/README.md) to process uploads.

Send `POST /api/v1/workspaces/{slug}/layer-imports` as a multipart form.
The file may be up to 32 MiB.

| Part | What to send |
|---|---|
| `file` | One `.gpkg`, `.zip` containing a Shapefile, `.geojson`, `.kml` or `.kmz` file |
| `layerSlug` | Lowercase letters, digits and hyphens. Reuse a slug to make a new version of the same layer. |
| `layerName` | The display name for a new layer |
| `role` | `hierarchy`, `forest`, `exclusion` or `analysis` |
| `hierarchy` | The workspace hierarchy slug, required for the hierarchy role |
| `settings` | A JSON object with any of the settings below |

| Setting | What it does |
|---|---|
| `nameField`, `codeField` | Read a feature name or code from the chosen source field. A hierarchy requires `nameField`. |
| `parentCodeField` | Read a hierarchy unit's parent code from the chosen field |
| `sourceLayer` | Select a named source layer if the file contains more than one |
| `depth` | The existing level number, required for a hierarchy. The root level is 0. |
| `parentVersionId` | Optional ID of a ready hierarchy draft from the preceding level. The worker otherwise uses current published parent units. |
| `coverageVersionId` | Optional ID of a ready hierarchy draft in the same workspace, used to review forest coverage |

For example, this uses the synthetic forest file from the repository root:

```sh
curl -H "Authorization: Bearer $TOKEN" \
  -F 'file=@seed/demo-state/forest.geojson' \
  -F 'role=forest' -F 'layerSlug=forest' -F 'layerName=Forest' \
  -F 'settings={"nameField":"name","codeField":"code"}' \
  http://127.0.0.1:18090/api/v1/workspaces/demo/layer-imports
```

The API returns HTTP 202 with a `versionId` and a `Location` header. Use
`GET /api/v1/workspaces/{slug}/layer-imports/{versionId}` to read the draft.
Its import status is `pending`, `ready` or `failed`. The response includes
`sourceCrs` (the source coordinate system, if known), `featureCount`,
`unitCount` and `problems`. A ready draft can still have review items that
must be resolved before publishing. Each item has a `kind`, `message`,
`blocksPublish` flag and, where available, a `featureRef`. A reference like
`feature:3` means the third feature read from the selected source layer.

### Preview and publish

All layer routes require a state administrator grant at a workspace root.

- `GET /api/v1/workspaces/{slug}/layer-imports` lists existing layers,
  hierarchy levels and the 100 most recent uploads.
- `GET /api/v1/workspaces/{slug}/layer-imports/{versionId}/preview?days=30`
  checks detections against the published layers and against the same set with
  the ready draft swapped in. Included and excluded counts show changes in
  alert eligibility. The preview includes up to 20 examples per group. A detection can appear in more than one group. It also lists grants
  on disappearing paths and returns a `grantConfirmation` for that exact list.
- `POST /api/v1/workspaces/{slug}/layer-imports/{versionId}/publish` accepts a
  JSON body such as `{"days":30}`. To remove disappearing grants, include the
  `grantConfirmation` from the preview after the administrator approves the
  list. A blocked draft or stale confirmation returns HTTP 409. The window is
  1–90 days and defaults to 30.
- After HTTP 202, poll the same version's detail route. A published or retired
  upload includes `rematch` with a status and counts of detections checked,
  matches written and alerts created.

Publishing retires the previous layer version and records an audit entry in
the same transaction that saves the new version and queues the River replay.
For a hierarchy, the old rows at the selected level close when the new rows
open. Grants on surviving paths keep their existing records. Publish a
referenced parent draft before its children. A draft cannot leave current
child paths without parents. If a legacy layer supplied multiple levels,
the other levels keep their original source version.

Ingest, preview and replay call `classify_detection()` for area, forest and
hierarchy matching. Previews and replay skip linked copies of the same
observation, as ingest does. River checks the fixed window ending at
publication in batches and records progress with each commit. Each batch uses
the current published layers, so overlapping jobs never restore an older
match. A match stores the versions actually used and retains its first
processing time.

A newly eligible detection gets an alert and opens a workflow if it has no
alert yet. Existing alerts retain their original routing, forest class,
paths, layer versions and workflow history, even when a detection becomes
ineligible. The queue and workflow permissions use those saved alert paths.

## FIRMS backfill

Backfill fetches FIRMS standard-processing products, such as `VIIRS_SNPP_SP` and `MODIS_SP`, for one workspace and a range of UTC days. FIRMS publishes these weeks after the near-real-time products, with better calibration and geolocation, and keeps them for many years.

- It uses the area API's date form, `/api/area/csv/{key}/{product}/{box}/{days}/{date}`, which returns the days from `date` to `date + days - 1`. The [FIRMS area API page](https://firms.modaps.eosdis.nasa.gov/api/area/) allows 1 to 5 days, so the range is split into chunks of at most 5 days.
- It first asks the [data availability API](https://firms.modaps.eosdis.nasa.gov/api/data_availability/) which days the product has. FIRMS answers a day it has not processed yet with an empty file, so days after the last available day are left out and never recorded as done.
- Each chunk is one `ingest_runs` row of kind `backfill`. A failed chunk is recorded and the next chunk still runs.
- It is safe to repeat. A run skips every day that a successful backfill run of the same workspace, source and product already covers, so it continues where an earlier run stopped. Records that are fetched again are duplicates and change nothing.

```sh
make backfill-firms FROM=2024-01-01 TO=2024-06-30                 # run here, with the saved key or FIMRA_FIRMS_MAP_KEY
make backfill-queue FROM=2024-01-01 TO=2024-06-30 WORKSPACE=demo  # queue a job for the running API
make backfill-demo                                                 # synthetic fixtures, no network
```

`PRODUCTS` defaults to `VIIRS_SNPP_SP,MODIS_SP`. The queued job runs on its own queue, so a long backfill never delays a poll. A job that times out or fails is retried and starts from the first unfinished day. `make backfill-demo` loads the demo workspace and reads the synthetic files in [the fixtures folder](../../packages/connectors/firms/fixtures/). Run it twice: the second run fetches nothing. Records read from fixtures are stored under the source `demo`, like `make demo-detections`, so they never count as done for a real FIRMS backfill. A product with no fixture file fails instead of counting as an empty day.

### Same observation, two products

A satellite observation reaches FIRMS twice: in a near-real-time product, then in the standard-processing product. Both records are kept. When the second arrives, it is linked in `detection_links` to the first and gets no workspace match of its own, so it is not counted or alerted twice. This works in either order.

Two records are the same observation when they come from the same source, the paired product (for example `VIIRS_SNPP_NRT` and `VIIRS_SNPP_SP`), the same satellite and the same minute, and lie within half a pixel of each other: half the larger of the scan and track sizes, and at least 187.5 metres. Each record links to the nearest such record, and each earlier record takes at most one link.

## Feed health

`GET /api/v1/workspaces/{slug}/feeds` takes a bearer token. Only a role at the root of one of the workspace's hierarchies, such as the state administrator, may see it. Anyone else, and any unknown workspace, gets 403.

It lists each live feed: a source and product that the workspace has polled. Backfill and file imports are not live feeds.

| Field | Meaning |
|---|---|
| `status` | `healthy`, `delayed`, `quiet` or `failing`, from the thresholds below |
| `lastRunAt`, `lastRunStatus`, `lastError` | The latest poll and how it ended. The FIRMS key is never in the error. |
| `lastSuccessAt` | When the latest successful poll's response arrived |
| `latestObservationAt`, `delaySeconds` | The newest observation the feed has returned in the last 90 days, and its age |
| `failuresLast24h` | Failed polls in the last 24 hours |
| `processing` | Records new in a poll in the last 7 days, the 95th percentile of their processing time in seconds, and the share processed within 20 minutes |

The response also has `thresholds`, the `fireSeason` with `inSeason`, and `openAlerts`.

| Status | When |
|---|---|
| `failing` | No successful poll for 60 minutes, which is four missed polls |
| `quiet` | Polls work, but the newest observation is over 36 hours old: two missed daytime passes |
| `delayed` | The newest observation is over 18 hours old: one missed daytime pass, plus the few hours FIRMS takes to publish |
| `healthy` | Otherwise |

A new feed's age counts from its first poll. A workspace can change the thresholds and the fire season in its settings:

```json
{"feedHealth": {"failingAfterMinutes": 60, "delayedAfterHours": 18, "quietAfterHours": 36,
                "fireSeason": {"start": "11-01", "end": "06-30"}}}
```

The default fire season is 1 November to 30 June in India Standard Time. It is the national forest fire season. It covers the early fires of the dry deciduous forests of central and southern India and the late fires of the Himalayan pine forests in May and June. Forest fires are rare in the monsoon months, so a quiet feed is expected then. Invalid settings are logged and the defaults are used, so a typing mistake cannot switch alerts off.

### Feed-health alerts

A River job checks every workspace's feeds every five minutes. When a feed is `quiet` or `failing` in fire season, it records an alert in `feed_health_alerts`. There is at most one open alert per workspace feed: while the problem continues, the check updates that alert's status and last check time. When the feed is `healthy` or `delayed` again, the check resolves the alert. Outside fire season no new alert opens, and an open alert stays open until its feed recovers. Notifications are sent from these alerts in a later slice. `make feed-health` runs the check once and prints each feed.

### Processing time

The release gate asks that 95% of newly published FIRMS records are processed within 20 minutes. FIRMS does not say when it published a record. A record that is new in a poll was published after the feed's previous successful poll, so the time from that poll's fetch to the record's match is an upper estimate. Each run records `fetched_at` when its response arrived, each detection records the run that first stored it, and each match records `matched_at` when it was written.

## Database queries

Regenerate the checked-in Go queries from this directory after editing `queries.sql`:

```sh
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
```

The role query starts from the signed-in user's ID. The tile query checks grants and reads matches in one SQL statement. The ingest statement saves detections, links records of the same observation and builds their match snapshots in the same transaction. `feeds.sql` gathers the facts behind each feed's status.

## Alert queues

A workspace gets one new alert per matched detection inside forest or near its edge. The alert links to the match and keeps a copy of its hierarchy snapshot. Inside forest goes to the covering beat. Near-edge detections go to edge review by default. To route future near-edge detections straight to the beat, set the workspace column `review_near_edge` to false. A shared boundary, overlapping beats or a missing beat sends the alert to triage. Replaying a feed keeps the original assignment.

Use a bearer token with `GET /api/v1/workspaces/{slug}/alerts`. Results are newest first by observation time, then by alert ID. A page contains `items`, `canReview`, per-queue `counts` and, when another page exists, `nextCursor`. Counts include all visible rows matching the time, class, status and path filters. Queue and cursor apply only to the rows. Missing counts mean zero.

| Parameter | Accepted values |
|---|---|
| `queue` | `unit`, `edge_review`, `triage` |
| `status` | A step slug from the default or workspace template, such as `acknowledged` |
| `hours` | Past 1–720 hours, default 168 |
| `class` | A detection forest class |
| `unitPath` | A whole path prefix, such as `ds.c1.d1` |
| `detection` | A detection ID, for selecting a point from the map |
| `limit` | 1–200 rows, default 100 |
| `cursor` | URL-encoded `nextCursor` from the previous page |

Use the row's `id` with `GET /api/v1/workspaces/{slug}/alerts/{id}`. Details are available for older alerts outside the list's time window. The `detection_id` identifies the corresponding map point.

A grant covers the same matched paths as it does on the map. Sections 5.3 and 6 of the plan leave review with officers responsible for that area. For the review queues, the **covering grant itself** must be at division level or above in its own hierarchy. A root grant sees every queue in the workspace, including points without a path. In a hierarchy without a division level, only a root grant qualifies for review. A caller without a workspace grant gets 403. A hidden or missing alert detail returns 404.

## Alert workflow

An alert has one workflow instance. Its current step and entry time live there so the queue and the detail panel read the same state. The list and detail return the step slug in `status`, its label in `step_label`, its time in `step_time`, and an `overdue` flag. The alert insert trigger opens the instance and records New in the same transaction, including for backfills. Existing alerts start at their original creation time. A workspace template takes precedence over the shared default for new alerts.

| Covering grant | Default actions |
|---|---|
| Field staff | Acknowledge, send a team, record arrival, control and extinguish the fire, and record first and final report steps |
| Division officer at division level or above | All field actions, location exceptions, verification, closure and other outcomes |
| State admin | All moves in the template on visible alerts |

Each move uses the alert's matched hierarchy paths and queue access rules. An officer grant outside those paths gives no extra rights. Closed, false alarm, duplicate, controlled burn and not reachable end the workflow. First report and final report record that the form is pending.

Use `GET /api/v1/workspaces/{slug}/alerts/{id}/history` for ordered `entries` and the caller's allowed `moves`. Entries include the actor's display name, event time, location when available, note, any location exception and whether a report is pending. New is a system event at the detection point. Later entries identify the user by ID in storage. Staff names are only returned to a caller who can see the alert.

Post to `/api/v1/workspaces/{slug}/alerts/{id}/steps` with a bearer token and a JSON body, for example:

```json
{
  "step": "reached_site",
  "location": {"latitude": 16.4187, "longitude": 86.2975},
  "note": "Team is on site.",
  "clientId": "field-phone-42",
  "clientTime": "2026-11-25T10:15:00Z"
}
```

Reached site, controlled and extinguished need a point within the workspace's `workflow_radius_metres` (default 1,000 metres). An eligible officer can supply a nonblank `reason` for a missing or distant location. Other steps accept an optional location. A missing point stays empty. Notes are optional. The log and audit row are written together.

The response contains the log ID, step and event time. An optional client ID identifies one request per alert. Retrying the same request as the same user returns its original receipt. Reusing the ID for different details returns 409. An optional client time needs a client ID and must fall between the preceding step and server time. The server keeps its own recording time as well. An unavailable move returns 409, a disallowed role returns 403, and a missing or distant required point returns 422.

The earliest time limit on a current step's outgoing transitions determines when an alert is overdue. The default allows 30 minutes to acknowledge, an hour to send a team, two hours to reach the site, 24 hours for a first report, 72 hours for a final report and 24 hours each for verification and closure. Control and extinguishing have no default time limit. The SQL view `alert_response_times` measures time from alert creation to the first acknowledgment, arrival and control recorded in the log. Missing milestones stay null.

## File exports

Any user with a role in a workspace can export what they can see on the map or in the alert queue. The geo worker writes the file. Run it with `make geo-worker-up`.

- `POST /api/v1/workspaces/{slug}/exports` takes a JSON body and returns HTTP 202 with the export's status and a `Location` header.
- `GET /api/v1/workspaces/{slug}/exports/{id}` returns the status: `pending`, `ready`, `failed` or `expired`, with the row count, file size and file name.
- `GET /api/v1/workspaces/{slug}/exports/{id}/file` downloads a ready file.

| Field | Meaning |
|---|---|
| `kind` | `alerts` (the alert queue) or `detections` (the matched detections on the map). Required. |
| `format` | `gpkg` (GeoPackage, recommended), `shapefile` (zipped), `kml` (KMZ), `geojson`, `flatgeobuf`, `csv_wkt` (the point as well-known text), `csv_lonlat` (longitude and latitude columns) or `xlsx` (Excel). Required. |
| `hours` | The time window, from 1 to 720 hours. The default is 168. |
| `forestClasses` | A list of `inside`, `near_edge`, `outside` and `fixed_heat_source`. Detections default to the map's classes, without `outside`. Alerts default to all four. |
| `unitPath` | Only rows under this unit path, as on the alert queue. |
| `queue`, `step` | Alerts only: one queue (`unit`, `edge_review` or `triage`) and one workflow step, such as `new`. |

A user without a role in the workspace gets HTTP 403. An export of more than 100,000 rows gets HTTP 422 and should use narrower filters. Only the requester can read an export's status or download its file. Another user gets HTTP 404, and the requester also needs a role in the workspace at the time. A file is kept until its expiry time, which is set when the export is requested. After that the download returns HTTP 410. The API removes expired files and their saved rows every ten minutes.

### How the rows are chosen

The request saves the exact rows the user may see in `export_rows`, in one SQL statement inside the request's transaction ([exports.sql](exports.sql)). It uses the same rule as the endpoints the user already sees:

- **Detections** follow [tiles.sql](tiles.sql): `match_under_grant()` on the match's paths.
- **Alerts** follow [alerts.sql](alerts.sql): `match_under_grant()` on the alert's own routing snapshot, and review queues only for grants at division level or above.

The worker never sees the user, the grants or the filters. It is given only an export ID, and it reads rows by joining `export_rows` to detections, matches and alerts by their keys. It cannot widen the set:

- The database accepts new export rows only from the transaction that created the export, only while it is pending, only in its workspace and only up to its saved row count.
- Export rows cannot be changed. The request itself, with its filters, roles and row count, cannot be changed either.
- A finished file can never record more features than the saved row count. Rows can only drop out, when their detection or alert is deleted, and the file's note says so.

The values in a file, such as an alert's workflow step, are read when the file is written.

### Contents

Each file has point geometry in WGS 84 (EPSG:4326) and one row per detection or alert, newest first. The fields are the detection ID, observation time in UTC, source, product, satellite, forest class and triage reason. Then come the code and name of every level of the main territorial hierarchy (`state_code`, `division_name` and so on), the forest feature code, fire radiative power, confidence, longitude and latitude. Alert exports add the alert ID, queue, creation time and the current workflow step with its label and time. Alerts keep the class, reason and units they were routed with.

Every export carries a note of its source, filters, time window, coordinate system, request and generation times, row count and the requesting role. It never names the people involved. GeoPackage and Excel files hold it in the `export_note` and `export_fields` tables. Every other format is a zip file with the note in `README.txt` beside the data. Shapefile field names are limited to 10 characters. The note lists each field's short name, such as `divisio_cd` for `division_code`. Each export, and each download, is recorded in the audit log with its filters and row count.

Run `make export-test-integration` with the stack, Keycloak and geo worker running. It exports as each demo user and checks that the rows match what the same user sees in the alert queue and in detection tiles. It checks that `no.role` is refused, that one user cannot read another's export, that expiry removes the file and that the audit rows exist. It then opens every downloaded format with OGR in the geo worker container and prints its layers, geometry, coordinate system, fields and feature count.

## Notifications

The alert's saved paths decide who can receive a notification. Current grants
are checked again before email delivery and when someone opens their inbox.
Each workspace starts with these rules for both in-app notifications and email.

| Event | Recipients |
| --- | --- |
| New unit alert | Field staff and division officers whose grants cover the alert |
| New edge review or triage alert | People whose grants let them review that alert |
| Alert overdue at its current step | Division officers whose grants let them see the alert |
| Feed-health alert opens | People with a role at the root of a workspace hierarchy |

The alert or feed-health insert records its notifications in the same database
transaction. A background check records an overdue event once for each step
entry, including when an alert returns to a step it has visited before. Each
event, recipient and channel has one database row, even when multiple grants
or rules match. Replaying a feed or re-matching an existing alert leaves that
row alone. A re-match that opens a new alert can notify its officers.

### Endpoints

All endpoints require a bearer token.

- `GET /api/v1/notifications` returns `items`, `unreadCount` and `hasMore`.
  The result starts with unread items and includes up to 50 per page. Use
  `offset=50` for the next page.
- `POST /api/v1/notifications/{id}/read` marks one of the caller's
  notifications read and returns `204`. It is safe to repeat.
- Use `GET /api/v1/notifications/preferences` to read preferences. Use
  `POST` with `{"emailMuted":true}` or `{"emailMuted":false}` to set them.
  In-app notifications stay on when users mute email.
- `GET /api/v1/workspaces/{slug}/notification-rules` lists the rules. A state
  admin at the workspace root can replace them with a `POST` containing an
  array, for example
  `[{"event":"alert_new","recipients":"reviewers","channel":"in_app","queue":"triage"}]`.
  Events are `alert_new`, `alert_overdue` and `feed_open`. Select recipients
  with `field_staff`, `division_officer`, `reviewers` or `root`. Channels are
  `in_app` and `email`. An alert rule can filter by `queue` or `forestClass`.
  Forest classes supported here are `inside` and `near_edge`. Feed-health rules
  use `root` without a filter. Changes apply to new events.

### Email delivery

Email is disabled unless `FIMRA_SMTP_ADDR` is set. The web app requests the
OpenID Connect `email` scope. With email enabled, the API saves a verified
address from the token on the user row when `/me` loads. Provision this claim
and `email_verified` in your sign-in provider. A later `/me` request without a
verified address clears the stored address. The address is personal data.
Inbox and preferences responses omit it. The audit log records address
changes and access for delivery without recording the address.

| Setting | Value |
| --- | --- |
| `FIMRA_SMTP_ADDR` | Mail server host and port |
| `FIMRA_SMTP_FROM` | Sender email address |
| `FIMRA_SMTP_SECURITY` | `starttls` by default, `tls` for implicit TLS, or `local` for an unauthenticated loopback mail catcher |
| `FIMRA_SMTP_USERNAME`, `FIMRA_SMTP_PASSWORD` | Optional mail server credentials, supplied together |
| `FIMRA_WEB_URL` | Full URL of the web app for links in email |

A River job checks the database outbox. Confirmed temporary failures retry
with increasing delays and a maximum of five attempts. Permanent failures end
the delivery. SMTP may accept a message and lose the connection before
confirming it. The job records that outcome, or a worker crash during a send,
as `uncertain` and leaves it for operator review. Messages are plain text and
contain a link without detection coordinates. An email is queued only when
the recipient has a verified address on file and has left email on.

The delivery interface includes disabled stubs for text messages and mobile
push. A text message deployment needs the agency's gateway and templates
registered under India's DLT rules. Mobile push needs a configured provider
and device registration.

## First-run setup

A fresh install has no workspaces. The wizard in the web app takes the first administrator from there to a working alert map (PLAN.md section 7).

### Claiming the install

The first signed-in user who sends the one-time setup code becomes the state administrator at the root of the new workspace. The code guards the install, so a visitor who reaches the sign-in page first cannot claim it.

- Without `FIMRA_SETUP_CODE`, the API makes a random code at start-up, about 79 bits in four groups such as `ABCD-EFGH-JKMN-PQRS`, and prints it once in its log as `setupCode`. Only someone who can read the server's log can use it. A restart prints a new code.
- With `FIMRA_SETUP_CODE`, of at least 12 letters and digits, the API uses that code and does not print it. This suits automated installs.
- Codes compare in constant time and ignore case, spaces and dashes. After 10 wrong codes in 15 minutes, every attempt is refused until the window passes.
- The claim works exactly once. It runs in one transaction under an advisory lock and adds the only row `install_setup` can hold. Once any workspace exists, every claim gets HTTP 409, even with the right code.
- The claim is audited as `setup.bootstrap`, with the administrator's subject.

The claim creates the workspace, a `territorial` hierarchy with a `state` level, and a root unit with the chosen state code but no shape yet. The administrator's grant is on that root unit. Publishing the state boundary later replaces the unit at the same path, so the grant stays valid. A state file with a different code is blocked from publishing.

### Wizard endpoints

All except the first two need a state administrator grant at the workspace root.

- `GET /api/v1/setup` returns `bootstrapAvailable`.
- `POST /api/v1/setup/bootstrap` takes `setupCode`, `mode`, `workspaceName`, `workspaceSlug` and `stateCode`. Only `state` mode is available in phase 1; `national` gets HTTP 400 with that reason.
- `GET /api/v1/workspaces/{slug}/setup` returns the wizard state: mode, preset, levels with labels, the layer checklist, each step's status, the FIRMS key status, whether email is on, the finish time and the hierarchy presets. A workspace made before the wizard gets a setup record on first use.
- `POST .../setup/hierarchy` takes a `preset` and the full list of `levels`, each with a `slug` and `labels` such as `{"en": "Beat", "hi": "बीट"}`. Levels that already have units keep their identifier; only their labels can change.
- `POST .../setup/steps/{step}` marks `layers`, `firms`, `forms` or `invitations` as `done` or `skipped`. Layers cannot be skipped and need every level and the forest layer published. FIRMS can be done only with a key. Starter forms can only be skipped for now.
- `POST .../setup/finish` closes the wizard once every step is done or skipped. `POST .../setup/reopen` opens it again.
- `GET .../setup/units` lists current units for the invitation form.

`GET /api/v1/me` also returns `setup`: `bootstrapAvailable`, the `unfinished` workspaces whose wizard the caller should see, and `claimedInvitations`.

Layers use the [layer import endpoints](#layer-imports). The wizard names its layers after the levels, such as `territorial-beat`. Publishing the top level of the wizard's hierarchy sets the workspace boundary to the published state shape.

### Encrypted settings

Workspace secrets are stored in `workspace_settings_secrets`, sealed with AES-256-GCM. The key comes from `FIMRA_SETTINGS_KEY` and never reaches the database. Each value has its own random nonce. The workspace and setting name are bound to the ciphertext, so a value copied to another row does not open. Each row records the ID of the key that sealed it, a short hash that does not reveal the key.

- `GET`, `PUT` and `DELETE /api/v1/workspaces/{slug}/settings/firms` read, save and remove the FIRMS map key. `PUT` takes `{"mapKey": "..."}`. Every answer gives only `set`, `setAt`, the last four characters as `hint`, the `source` (`settings`, `environment` or `none`) and whether encryption is on. The key itself is never returned.
- `POST .../settings/firms/test` asks FIRMS for the data availability of `VIIRS_SNPP_NRT` with the key the poller would use. The answer says whether it worked and why not, with the key removed.
- Saving, removing and testing are audited without the value. The key is never logged: FIRMS errors pass through the client that removes it from URLs.

To rotate the settings key, set the new key in `FIMRA_SETTINGS_KEY` and list the old one in `FIMRA_SETTINGS_OLD_KEYS`. Restart the API, which can then open both. Run `make rotate-settings-key` to seal every saved secret with the new key, then remove the old key.

### Invitations

Officers' accounts live in the agency's identity provider, so the wizard does not create them. An invitation is a pending grant keyed by email address.

- `GET`, `POST` and `DELETE /api/v1/workspaces/{slug}/invitations[/{id}]` list, create and revoke invitations. `POST` takes `email`, `role` (`state_admin`, `division_officer` or `field_staff`) and `unitPath`. The list shows addresses masked, such as `d…@example.invalid`.
- Invitations expire after 14 days.
- On each `GET /api/v1/me`, a token with a **verified** email claim matching an invitation turns it into a role grant. Unverified addresses claim nothing.
- An invited address is personal data. The invitation is deleted when it is claimed or expires; an hourly job and each list request remove expired ones. Audit entries name the invitation, role and unit, never the address. The address is not logged and not stored in the job queue.
- With email on, a River job sends one invitation email. It reads the address from the invitation when it sends, so a revoked or claimed invitation sends nothing.

`make wizard-test-integration` checks all of this on a fresh database with only migrations applied. It claims the install, picks a preset, uploads the demo source files through the import and publish endpoints, saves a FIRMS key, invites the demo officers, polls a fake FIRMS server and checks what each officer's alert queue shows. It also checks that a second claim fails, that expired and unverified invitations grant nothing, and that no key or address reaches the logs, API answers, audit log or job queue.

