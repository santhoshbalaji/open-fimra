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

The integration tests check sign-in with all four demo users. They also exercise reference matching, replay a synthetic feed across two months and decode detection tiles to check access and filters. They group synthetic reference clusters into incidents and check what each demo user sees and may do. They run a backfill three times, make a feed go silent on a fake clock and check the feed health endpoint. Run them before starting the API, so the test worker can pick up its scheduled heartbeat. `make api-test` and `make api-lint` run without the development stack.

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
| `FIMRA_ATTACHMENT_DIR` | `.form-attachments` (at the repository root when using Make). Photos and files from field reports. See [Attachments](#attachments) for the other attachment settings. |
| `FIMRA_CORS_ORIGINS` | `http://127.0.0.1:4200,http://localhost:4200` |
| `FIMRA_SETUP_CODE` | Empty. On a fresh install the API then prints a one-time setup code in its log. See [First-run setup](#first-run-setup). |
| `FIMRA_SETTINGS_KEY` | Empty, which turns encrypted settings off. A base64-encoded 32-byte key, for example from `openssl rand -base64 32`. |
| `FIMRA_SETTINGS_OLD_KEYS` | Empty. Earlier settings keys, separated by commas, during a key rotation. |

When run through Make, the database URL defaults to `DATABASE_URL` and the issuer uses `FIMRA_KEYCLOAK_PORT`. Use these defaults with the local stack.

## Endpoints

- `GET /healthz` returns 200 while the service is running.
- `GET /readyz` returns 200 when PostgreSQL responds and 503 when it cannot be reached.
- `GET /api/v1/workspaces/{slug}/feeds` returns feed health for workspace administrators. See [Feed health](#feed-health).
- `/api/v1/workspaces/{slug}/forms` and the alert's `submissions` and `attachments` hold forms and field reports. See [Forms and field reports](#forms-and-field-reports).
- `/ogc/workspaces/{slug}` is the read-only GIS link, and `/api/v1/access-keys` manages the personal access keys it accepts. See [GIS link](#gis-link).
- `GET /api/v1/me` takes an access token in the `Authorization: Bearer` header. It returns the caller's `subject`, `username` and `roleAssignments`. Each assignment includes the workspace slug and name, hierarchy slug, role, unit code and name, level slug, path, the unit's `bounds` as `[west, south, east, north]` in degrees (null for a unit without a shape) and beat count. The field app fits its map to these bounds. A grant to one beat counts that beat. A signed-in user without grants gets an empty list. A missing or rejected token gets a JSON error and a bearer challenge with HTTP 401.

The service finds the signing keys through OpenID Connect discovery and caches them. It checks the issuer, audience and token times. It allows 30 seconds for differences between clocks.

## Control-room summary

`GET /api/v1/workspaces/{slug}/summary` accepts the alert queue's `hours` (1–720, default 168), `class` and `unitPath`. A workspace grant is required. Counts cover all matching records, including those on later queue pages.

| Field | Meaning |
|---|---|
| `detections`, `today` | Counts by forest class. Both use observation time and the same grants as detection tiles. The first uses the selected window. Today runs from midnight in India Standard Time (`Asia/Kolkata`) through the current time, independently of the window. Both apply the class and unit filters. |
| `queues`, `steps` | Routed alerts by queue and current workflow step. Grants and filters use the routing snapshot saved with each alert, including after a layer is published. Review and triage require a covering review grant. A missing count means zero. |
| `active`, `pendingReports` | Counts of alerts at a nonterminal step and alerts whose current step has the report-pending flag, respectively. |
| `overdue` | An alert counts once if the time since it entered its current step is strictly greater than the shortest non-null limit on an outgoing transition. At the deadline it is still on time. A step with no limit cannot be overdue. |
| `responseSeconds` | Median, 90th percentile and `count` of alerts for each milestone. `alerted` runs from the satellite observation to the alert being raised. `acknowledged`, `team_sent`, `reached_site`, `controlled` and `extinguished` run from the alert being raised to the first step-log entry for that step. Each milestone includes only alerts that reached it. An empty sample gives null for both values. |
| `feedHealth` | Current source, product and status from the same feed facts and thresholds as the feeds endpoint. Root grants get the workspace-wide list (empty before the first poll). Other grants get null. |

The counts come from [scope.sql](scope.sql), which chooses the detections and alerts a caller may see in a time window, followed by [summary.sql](summary.sql). Situation reports start with the same text, so the two always agree.

The demo budget is 500 ms at the 95th percentile. The integration suite times 20 signed summary requests per granted demo account against the local database. This includes token verification and response encoding. The handler runs one counts query and one additional feed query for a root grant. With 11 demo detections and seven routed alerts, a local run measured 5.73 ms at the 95th percentile for the state administrator, 2.35 ms for the division officer and 2.79 ms for the beat guard. CI reports fresh measurements for each run.

## Situation reports

A situation report is a printable summary of one unit for one day, a range of days or the past hours. Anyone with a role in a workspace can make one for their own units and every unit below them. A report shows only what the dashboard and the alert queue show that person.

| Request | What it does |
|---|---|
| `GET /api/v1/workspaces/{slug}/situation-report/units` | Lists the units the caller may report on, with each unit's hierarchy, path, name, depth and level label |
| `GET /api/v1/workspaces/{slug}/situation-report` | Returns a report as JSON, for the web page |
| `POST /api/v1/workspaces/{slug}/situation-reports` | Saves a report and queues its PDF. Returns 202 with the status and a `Location` header. |
| `GET /api/v1/workspaces/{slug}/situation-reports/{id}` | Returns the status: `pending`, `ready`, `failed` or `expired`, with the page count, file size and file name |
| `GET /api/v1/workspaces/{slug}/situation-reports/{id}/file` | Downloads a ready PDF |

The GET takes query parameters and the POST takes a JSON object with the same names:

| Field | Meaning |
|---|---|
| `from`, `to` | Days such as `2026-03-15`, in India Standard Time, both included. `to` defaults to `from`. A report covers at most 184 days and cannot end after today. A period that includes today ends when the report is made. |
| `hours` | Instead of days: the hours before now, from 1 to 720, as on the dashboard |
| `unitPath`, `hierarchy` | The unit, such as `ds.c1.d1` in `territorial`. The hierarchy defaults to the caller's own. Without a unit path the report is for the caller's highest granted unit, or for all their units when they have several at the same level. |

A unit outside the caller's units gets 403 and an unknown unit 404. A caller without a role in the workspace gets 403.

### What a report holds

- `counts`: the dashboard's numbers for the same window and unit, from the same SQL. Detections by forest class, alerts by queue and current step, active, overdue and waiting for a report, and response times for every milestone. A report on a whole hierarchy, such as the state, also counts alerts that matched no unit, like the dashboard without a unit filter.
- `alerts`: the total, active and closed alerts. Alerts are counted by observation time. Steps, active, closed, overdue and field reports are as of the moment the report is made, also for a period in the past.
- `incidents`: incidents with at least one alert in the period, with how many were opened in the period (their first alert the caller can see was observed in it), are active, closed and overdue, and a list of up to 200. Counts, times and places use only the member alerts the caller may see, as in the incident view.
- `fieldReports`: the forms submitted for the period's alerts, approved, returned and waiting for review, and the forms missing: an alert is at a step whose assigned form has no submission that counts. `forms` breaks these down by form.
- `overdueAlerts`: up to 200 alerts past their time limit, with their step, when they entered it and by how much they are late.
- `units`: alerts, active, overdue, waiting for a report and forms missing for each unit one level below the report's unit, labelled with the workspace's own level name in `unitLevel`.
- `map`: the unit's outline, the units below it, published forest, the period's alerts (the latest 5,000) and boxes around their incidents, as GeoJSON geometry and points in WGS 84.
- `labels` and `notes`: the words for each code, and how the numbers are worked out. The web page and the PDF print both as they are.

Places are named with the levels below the report's unit, such as `Range 2 › Beat 05`, using the names saved with each alert.

### PDF files

The POST works out the report in the request's transaction and saves it in `situation_reports` with the requester's subject. The [geo worker](../geo-worker/README.md#how-situation-reports-are-drawn) draws the saved report as a PDF and stores it in the same row, so the file holds exactly what the requester saw and nothing the worker chose. Only the requester can read the status or download the PDF, and they still need a role in the workspace. A report and its file are kept until `FIMRA_EXPORT_TTL` (24 hours by default) has passed. After that the download returns 410 and the API clears the content and the file every ten minutes. The database refuses any change to a saved report's request or content.

Making a report, preparing a PDF and downloading it are recorded in the audit log as `report.view`, `report.create` and `report.download`, with the period, unit and number of alerts.

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
| `place_hierarchy`, `places` | The slug of that same hierarchy, and JSON text with the code and name at every level, keyed by level slug, for example `{"beat":{"code":"B05","name":"Beat 05"}}`. Clients label each level with the workspace's own level names. Both are empty when the detection matched no unit. |
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

Use a bearer token with `GET /api/v1/workspaces/{slug}/alerts`. Results are newest first by observation time, then by alert ID. A page contains `items`, `canReview`, per-queue `counts` and, when another page exists, `nextCursor`. Counts include all visible rows matching the time, class, status and path filters. Queue and cursor apply only to the rows. Missing counts mean zero. Each row has the same place fields as a map tile, except that `places` is a JSON object rather than text.

| Parameter | Accepted values |
|---|---|
| `queue` | `unit`, `edge_review`, `triage` |
| `status` | A step slug from the default or workspace template, such as `acknowledged` |
| `hours` | Past 1–720 hours, default 168 |
| `class` | A detection forest class |
| `unitPath` | A whole path prefix, such as `ds.c1.d1` |
| `detection` | A detection ID, for selecting a point from the map |
| `incident` | An incident ID, for listing its members. The time window does not apply. |
| `limit` | 1–200 rows, default 100 |
| `cursor` | URL-encoded `nextCursor` from the previous page |

Use the row's `id` with `GET /api/v1/workspaces/{slug}/alerts/{id}`. Details are available for older alerts outside the list's time window. The `detection_id` identifies the corresponding map point. Each row also has `incident_id`, the incident the alert belongs to, or null for an alert created before incidents. Add `incident={id}` to list an incident's member alerts that you can see, whatever their age.

A grant covers the same matched paths as it does on the map. Sections 5.3 and 6 of the plan leave review with officers responsible for that area. For the review queues, the **covering grant itself** must be at division level or above in its own hierarchy. A root grant sees every queue in the workspace, including points without a path. In a hierarchy without a division level, only a root grant qualifies for review. A caller without a workspace grant gets 403. A hidden or missing alert detail returns 404.

## Alert workflow

An alert has one workflow instance. Its current step and entry time live there so the queue and the detail panel read the same state. The list and detail return the step slug in `status`, its label in `step_label`, its time in `step_time`, and an `overdue` flag. The alert insert trigger opens the instance and records New in the same transaction, including for backfills. Existing alerts start at their original creation time. A workspace template takes precedence over the shared default for new alerts.

| Covering grant | Default actions |
|---|---|
| Field staff | Acknowledge, send a team, record arrival, control and extinguish the fire, and record first and final report steps |
| Division officer at division level or above | All field actions, location exceptions, verification, closure and other outcomes |
| State admin | All moves in the template on visible alerts |

Each move uses the alert's matched hierarchy paths and queue access rules. An officer grant outside those paths gives no extra rights. Closed, false alarm, duplicate, controlled burn and not reachable end the workflow. First report and final report record that the form is pending. A step with an assigned form needs its submission before the alert moves on. See [Forms and field reports](#forms-and-field-reports).

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

The earliest time limit on a current step's outgoing transitions determines when an alert is overdue. The default allows 30 minutes to acknowledge, an hour to send a team, two hours to reach the site, 24 hours for a first report, 72 hours for a final report and 24 hours each for verification and closure. Control and extinguishing have no default time limit. The SQL view `alert_response_times` measures time from alert creation to the first acknowledgment, team sent, arrival, control and extinguishing recorded in the log, and from the satellite observation to alert creation. Missing milestones stay null.

## Incidents

Alerts that lie close together in space and time join one incident (PLAN.md section 5.3). An incident has its own workflow, and its member alerts keep theirs.

### Grouping rule

A new alert joins an open incident when it lies within the workspace's distance of **any** member and its observation time is within the time window of that same member, before or after. An incident is open until it reaches a final step, such as Closed or False alarm, or until it is merged. A fire next to a closed incident starts a new incident.

| Setting | Default | Accepted |
|---|---|---|
| `distanceMetres` | 1,500 metres: four VIIRS pixels, or one and a half MODIS pixels, so neighbouring pixels of one fire join while fires in separate valleys usually do not | 100 to 20,000 |
| `windowHours` | 24 hours: a fire seen on one day's passes joins the same fire seen on the next day's passes | Whole hours from 1 to 168 |

The rule lives in the workspace settings as `{"incidents": {"distanceMetres": 1500, "windowHours": 24}}`. A value outside the accepted range falls back to its default, so a typing mistake cannot switch grouping off. A state administrator at the workspace root can read it with `GET /api/v1/workspaces/{slug}/settings/incidents` and replace it with `PUT` and the same two fields. The answer also gives the `defaults`. Changes apply to alerts created afterwards and are audited. Existing incidents keep their members.

### Merging

An alert that is close to members of two or more open incidents merges them. The incident with the earliest member observation continues, with the lowest ID breaking a tie. The others get `merged_into` pointing to it and take no more steps or members. Their own timelines stay readable. Each member keeps the incident it first joined, and the merge is recorded in the audit log.

### Deterministic and safe to repeat

Grouping runs in the database transaction that creates the alerts, in a trigger after the alert insert, like the workflow and notifications. The alert and its incident are saved together or not at all, so no background job can miss an alert or group it twice. Within one statement, new alerts are grouped in order of observation time, then alert ID. Ingest, backfill and re-matching take a lock for each workspace first, so two transactions never group nearby alerts at the same moment.

Only a new alert changes incidents. A replayed feed creates no alert, and a re-match keeps every existing alert, so neither changes any incident. The distance and time tests are symmetric and an alert that bridges incidents merges them, so membership does not depend on the order in which alerts arrive, except where an incident was closed in between. Alerts created before this change have no incident.

### Who sees an incident

You see an incident when you may see at least one of its member alerts under the alert queue's rules: the alert's saved paths and, for review queues, a covering grant at division level or above. Every count, time, place, map bound and member list in the answer covers only the members you can see. A merged incident is visible to the same people as the incident it merged into. The timeline is shared by everyone who can see the incident. The incident's New entry has no location, because its first member may be in a unit that some viewers cannot see. Detection tiles carry no incident data. Alert exports add `incident_id`.

### Endpoints

All need a bearer token and a role in the workspace. Without a role you get 403. An incident you cannot see, or one that does not exist, gets 404.

- `GET /api/v1/workspaces/{slug}/incidents` lists incidents newest first by their latest visible member, then by ID. It accepts the alert queue's `hours`, `queue`, `class`, `unitPath`, `status`, `limit` and `cursor`. The time window keeps incidents with a visible member observed in it. Queue, class and unit path keep an incident when any visible member matches. Status is the incident's own step. The answer has `items`, `total` and, when there is another page, `nextCursor`.
- `GET /api/v1/workspaces/{slug}/incidents/{id}` reads one incident, including a merged one, whatever its age.
- `GET /api/v1/workspaces/{slug}/incidents/{id}/history` returns the incident's `entries`, your allowed `moves` and `mergedInto`. Each entry also has `alertsMoved`: how many of the alerts you can see moved with that step.
- `POST /api/v1/workspaces/{slug}/incidents/{id}/steps` takes the same body as an alert step and answers with the log ID, step, time and `alertsMoved`.

| Field | Meaning |
|---|---|
| `id`, `created_at` | Incident ID and when it opened |
| `status`, `step_label`, `step_time`, `overdue`, `terminal` | The incident's current workflow step, as for alerts |
| `merged_into`, `merged_at` | The incident this one merged into, and when. Null for an incident that continues. |
| `alert_count`, `queues`, `classes` | Visible members, and their counts by queue and forest class |
| `first_observed_at`, `last_observed_at` | Earliest and latest visible member observation, in Unix seconds. `last_observed_time` is the latest as text, for the cursor. |
| `beat_names`, `division_names` | Distinct beat and division names of the visible members |
| `bounds` | `[west, south, east, north]` of the visible members, in degrees |

### Incident workflow

An incident opens at New with the workspace's workflow template, the same one its alerts use. Moves on an incident use your roles on its visible members: a field grant that covers one member allows field steps, and an officer grant at division level or above on a member allows officer steps. A field step needs a location within `workflow_radius_metres` of one of the members you can see, or an officer's reason.

Acting on the incident moves its members. Each member alert that you could move the same way, from its own current step, gets its own log entry with your user, time, location, note and reason, and a link to the incident's entry. Other members stay where they are: alerts you cannot see, alerts already at a final step, and alerts that are behind or ahead of the incident. Those can be moved on their own, as before, until they catch up. Moving an alert on its own never moves its incident. A member's alert history shows `incidentId` on the entries its incident recorded. Response times, the dashboard and overdue checks keep using each alert's own log. A client ID identifies one request per incident, as for alerts. A merged incident refuses new steps with 409.

## Forms and field reports

Forms use the [form format](../../packages/form-schema/SPEC.md). The API checks every form file with the Go validator in `packages/form-schema`, so it accepts exactly what the web designer and the phone accept.

A form belongs to a workspace. Each version is a draft until it is published. A published version never changes: the database refuses any change to it or its deletion, and keeps the exact text that was published. To change a published form, add a version with a higher number.

A workspace administrator at the root of the hierarchy manages national and state forms. A division form can also be managed by a supervising officer whose unit holds the form's unit. Anyone with a role in the workspace can read published versions. Drafts are hidden from everyone else. Versions show `submissionCount`, the number of reports filled in on them, to the people who manage the form.

| Request | What it does |
|---|---|
| `GET /api/v1/workspaces/{slug}/forms` | Lists forms with their versions and assignments |
| `POST /api/v1/workspaces/{slug}/forms` | Adds a draft from a form file in the body, or from a form file with its template settings (see [Templates](#templates)). Its `id` names the form and its `version` must be higher than every existing version. Returns 201 and a `Location` header. |
| `GET /api/v1/workspaces/{slug}/forms/{form}/versions/{version}` | Returns a version with its `definition`, `columns`, `status` and flat `views` |
| `PUT /api/v1/workspaces/{slug}/forms/{form}/versions/{version}` | Replaces a draft. A published version gets 409. A plain form file keeps the draft's template settings; a file with its settings replaces them. |
| `POST /api/v1/workspaces/{slug}/forms/{form}/versions/{version}/publish` | Publishes a draft and creates its flat views. Publishing again returns the same version. |
| `PUT /api/v1/workspaces/{slug}/forms/{form}/assignments/{step}` | Assigns a published version to a workflow step, with `{"version": 1}`. Add `"hierarchy": "territorial"` and `"unitPaths": ["ds.c1.d1"]` to use the form only under those units. |
| `DELETE /api/v1/workspaces/{slug}/forms/{form}/assignments/{step}` | Takes the form off the step |

### Templates

Forms come in three levels ([PLAN.md section 5.4](../../PLAN.md#54-workflow-forms-and-field-reporting)):

| Level | Who manages it | What it extends |
|---|---|---|
| `national` | Arrives as a template file from a national body. A root administrator adds it, and it is published at once, so it is never a draft here and never changes. | Nothing |
| `state` | A root administrator | A published national template, or nothing |
| `division` | A root administrator, or a supervising officer for the division | A published state form, always |

To give a level, the unit of a division form, the version it extends or template rules, send the form file inside an envelope:

```json
{
  "level": "state",
  "extends": {"form": "first-report", "version": 1},
  "template": {"allowExtension": true, "locked": ["demo_state"]},
  "definition": {"formatVersion": 1, "id": "demo-first-report", "version": 1, "...": "..."}
}
```

A division form adds `"hierarchy": "territorial", "unitPath": "ds.c1.d1"`: a current unit below the top. A form keeps its level and unit for all its versions. Each version records the version it extends in `extends` (and `extendsVersionId`), so a new version can extend a newer template.

`template` says what the version allows the forms one level down: `allowExtension` lets them extend it, and `locked` lists section and item keys they must keep. A locked section, group or repeating group locks everything inside it. Division forms cannot be extended. A version keeps the locks of the template it extends as well as its own, so a division form keeps the national parts of its state form. The rules are frozen with the version.

When a draft is saved and again when it is published, the API compares it with the version it extends. Every locked part must be there, unchanged and inside the same section or group. The shared lists and stages that locked parts use must be unchanged too. Unlocked parts can change or go, and new sections and questions can be added outside locked parts. A form that breaks a lock gets 422 with one sentence per problem, such as `Field "crew_size" is locked by the template it extends and cannot be removed.` The web designer runs the same checks as the form changes, with the same messages. The database also refuses a version that extends a draft, a form in another workspace, a form that is not one level up, or a template that does not allow extension.

A division form can be assigned only under its own unit: the assignment needs its hierarchy and unit paths at or below the unit that owns it. Assignment stays with the root administrator.

Submissions record the exact version they were filled in on, so a report filled in on a national template keeps showing with that template's version after the state extends it.

A form that does not pass the validator gets 422 with the validator's messages in `problems`. Each form has at most one version at each step; assigning another version replaces it. The step belongs to the workflow template that new alerts in the workspace use. A form with stages can be assigned only at the workflow step of one of its stages, and is filled in there up to that stage.

### Reports on workflow steps

A step with an assigned form needs a submission of that form before the alert can leave the step. Moving on without one gets 422, for example "Submit the First report form before leaving this step." A submission counts until an officer returns it. A move to a final step, such as false alarm or duplicate, never needs a form. A step without an assigned form works as before, so the default workflow needs no forms. An incident's move takes along only the member alerts that have the forms their current step needs.

`GET /api/v1/workspaces/{slug}/alerts/{id}/forms` lists the forms assigned to the alert's workflow, with `submitted` and `requiredNow` for each, and the alert's current `step`. An assignment limited to unit paths applies when the alert's matched units lie under one of them.

### Submissions

Post to `/api/v1/workspaces/{slug}/alerts/{id}/submissions`:

```json
{
  "id": "5ca1ab1e-0000-4000-8000-0000000000a1",
  "form": "first-report",
  "version": 1,
  "step": "first_report",
  "values": {"reached_at": "2026-03-15T14:30:00+05:30", "fire_status": "controlled"},
  "deviceTime": "2026-03-15T14:45:00+05:30",
  "location": {"longitude": 86.2975, "latitude": 16.4187}
}
```

The phone makes the `id`, a UUID in small letters, so it can retry safely. The same request from the same user returns the first receipt with 200; another request with that ID gets 409. `step` can be left out when the form is assigned to one step only. The alert must have reached the step. Any published version of the assigned form is accepted, because a phone that was offline may still have an earlier one. The submission records the version, the alert, the incident the alert belongs to at the time, the user, the device time, the server time and the location.

The server checks the values against the form version (form format, sections 8 to 13): types, required questions, `requireWhen`, conditions, text lengths and patterns, number limits and decimal places, dates in the future, choices and cascading lists, the number of choices, files and entries in repeating groups, and GeoJSON shapes. Photos, signatures and files must be attachments uploaded for the same alert; photos and signatures must be images, and files must match `accept` and `maxSizeMegabytes`. Problems come back with 422 in `problems`, one plain-English sentence each. Answers to hidden questions and later stages are not kept. The server works out calculated fields and fills in values from the alert itself, ignoring what was sent for them.

`GET /api/v1/workspaces/{slug}/alerts/{id}/submissions` lists an alert's submissions and `GET /api/v1/workspaces/{slug}/submissions/{id}` returns one. Anyone who can see the alert can read and submit, using the same grants and queue rules as the alert queue. Others get 404.

### Approvals

A supervising officer on the alert (a division officer at division level or above, or a state administrator) posts `{"decision": "approved"}` or `{"decision": "returned", "note": "Recount the teams."}` to `/api/v1/workspaces/{slug}/submissions/{id}/approval`. A returned submission needs a note. Each submission has one decision, which never changes: repeating it returns the same answer, and a different decision gets 409. A field user gets 403. The decision records who, when and why.

### Attachments

Upload each photo, signature or file before the submission, with an ID the phone makes:

```sh
curl -X PUT -H "Authorization: Bearer $TOKEN" -H "Content-Type: image/jpeg" \
  --data-binary @site.jpg \
  "http://127.0.0.1:18090/api/v1/workspaces/demo/alerts/26/attachments/5ca1ab1e-0000-4000-8000-000000000001?name=site.jpg"
```

The API accepts JPEG, PNG and WebP images, PDF, ZIP, plain text and CSV, up to `FIMRA_ATTACHMENT_MAX_MB` (25 MB by default). Images, PDF and ZIP files must look like the type they claim. The first upload gets 201, a retry of the same file gets 200, and another file with the same ID gets 409. `GET /api/v1/workspaces/{slug}/attachments/{id}` downloads a file as an attachment, never shown inline.

Attachments are never overwritten. Each file is stored once under its SHA-256 hash, and its record cannot change. Uploads and downloads need the same access to the alert as its detail view, and both are recorded in the audit log. Photos are stored exactly as taken, including any location in their EXIF data. [PLAN.md section 6](../../PLAN.md#6-permissions-security-and-compliance) requires that location data is removed from photos before any public release, so a public view of attachments must strip it first.

| Variable | Default |
|---|---|
| `FIMRA_ATTACHMENT_STORE` | `local`: files on the API server's disk. `s3` uses an S3-compatible object store. |
| `FIMRA_ATTACHMENT_DIR` | `.form-attachments` (at the repository root when using Make). Local files, and uploads while they are checked. Use a separate directory for each stack. |
| `FIMRA_ATTACHMENT_MAX_MB` | `25` |
| `FIMRA_S3_ENDPOINT`, `FIMRA_S3_REGION`, `FIMRA_S3_BUCKET`, `FIMRA_S3_PREFIX` | For `s3`: the store's address, region (default `us-east-1`), bucket and an optional key prefix. Objects are addressed by path. |
| `FIMRA_S3_ACCESS_KEY`, `FIMRA_S3_SECRET_KEY` | For `s3`: the store's keys. Keep them in `.env`. |

The project does not yet bundle an S3-compatible store; which one is an open question in [PLAN.md section 14](../../PLAN.md#14-facts-and-access-to-confirm). `make attachments-s3-test` checks the S3 path against a small S3-compatible server that runs for the test only.

### Flat views

Publishing a version creates views in the `form_views` schema whose columns are the form's columns, so fields can be charted and exported like ordinary columns. `f{id}` has one row per submission, where `{id}` is the version's database ID. `f{id}_{group}` has one row per entry of each repeating group, with `_entry` (such as `2` or `2.1`) and `_parent_entry` for entries inside another group. The table `form_version_views` and the version's `views` field list the names.

Columns from the form use the field keys and suitable types: numbers, dates, times, times with zones, text arrays for multiple choice, UUIDs for attachments and geometries in WGS 84 for shapes, with `<key>_area_ha` and `<key>_length_km` worked out from the shape. Columns added by the server start with an underscore, which no field key can: `_submission_id`, `_alert_id`, `_incident_id`, `_workspace_id`, `_submitted_by`, `_step`, `_device_time`, `_received_at` and `_location`. The validator allows only small letters, digits and underscores in keys, and the API checks names again and quotes them before it writes the view, so a form cannot inject SQL. The views are not filtered by permissions: anything that shows their rows to a user must join `_alert_id` to the user's alert access, as the API does.

### Starter pack

The [starter pack](../../packages/starter-pack/README.md) is a ready set of forms for the default workflow. The forms are built into the API.

| Form | Workflow steps |
|---|---|
| Control-room dispatch | Team sent (the control room sends a team) and Reached site (the team arrives), as the form's two stages |
| First report | First report |
| Final report | Final report |
| Manual fire report | None: saving it makes an alert. See [manual fire reports](#manual-fire-reports). |
| Patrol log | None: a patrol is not tied to an alert |

A workspace administrator at the root of the hierarchy uses these requests:

- `GET /api/v1/workspaces/{slug}/starter-pack` lists the starter forms. Each has `form`, `title`, `version`, its `steps` in the pack, its `status` (`present` when the workspace has a form with that key, otherwise `missing`) and the steps it is `assigned` to now. `enabled` is true when every starter form is present.
- `POST /api/v1/workspaces/{slug}/starter-pack` turns the pack on, in one transaction. Each starter form that the workspace does not have yet is added, published and assigned to its steps, and gets the status `added`. A form the workspace already has with the same key is left exactly as it is, with its versions, drafts and assignments, whoever made it. A step that already has another form is left alone too, and listed in that form's `skipped` with the reason, so a department's choice for a step is never changed.

Turning the pack on again is safe: it adds only starter forms that are still missing. It never brings back a step assignment that a department removed. The wizard's starter forms step and the forms screens use the same requests. The request is audited as `starter_pack.enable`, and each publication and assignment has its own audit entry.

With the pack on, the default workflow asks for each report before an alert moves on: the dispatch stage before leaving Team sent, the arrival stage before leaving Reached site, and the first and final reports before leaving their steps. The arrival stage repeats the dispatch answers, which the phone fills in from the first stage. An officer can approve each submission before verifying the alert. Approval does not hold up the workflow.

### Manual fire reports

The manual fire report records a fire reported by another source: a phone call from the public, forest staff, another agency or another satellite service. Until the FSI and Bhuvan connectors exist (PLAN.md section 15), this is also how staff record a fire from those services.

Post the answers to `/api/v1/workspaces/{slug}/manual-fire-reports`, like a submission without an alert or step:

```json
{
  "id": "5ca1ab1e-0000-4000-8000-00000000e001",
  "version": 1,
  "values": {
    "report_source": "public-call",
    "reporter_phone": "9000000000",
    "reported_at": "2026-03-15T14:30:00+05:30",
    "fire_location": {"type": "Point", "coordinates": [86.2975, 16.4187]},
    "visible_signs": "smoke"
  },
  "deviceTime": "2026-03-15T14:32:00+05:30",
  "location": {"longitude": 86.2975, "latitude": 16.4187}
}
```

Anyone with a role in the workspace can send one. The server checks the answers against the published `manual-fire-report` version, then makes a detection with the source `manual` at `fire_location`, observed at `reported_at`. The detection is matched in this workspace only, with the same forest classes, units and routing as a satellite detection, and its alert gets a workflow at New, joins or starts an incident, and notifies the responsible officers in the same transaction. The report is kept as a submission at the alert's New step. The answer is that submission, with the new alert in `alertId` and its address in the `Location` header.

The `id` makes a retry safe, as for submissions: the same request returns the first answer with 200, and another request with that ID gets 409. Nothing is saved, and the answer is 422 with a plain reason, when:

- the place is outside the workspace, outside the forest and its edge, or on the list of known heat sources, so a satellite detection there would make no alert either;
- the new alert would be outside the units the caller can see, so the caller could not follow it;
- the answers do not fit the form, or that version is not published.

A department can change the form, but it must keep a date and time called `reported_at` and a point called `fire_location`, outside any repeating group, and have no stages. The form takes no photos or files, because attachments are uploaded for an alert and this report is what makes the alert. Photos from the site go in the dispatch and first reports.

Manual detections appear in the detection tiles, counts and exports with the source `manual`. They have no ingest run, so feed health ignores them. A re-match after publishing a layer keeps each one to the workspace it was recorded in.

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

Each file has point geometry in WGS 84 (EPSG:4326) and one row per detection or alert, newest first. The fields are the detection ID, observation time in UTC, source, product, satellite, forest class and triage reason. Then come the code and name of every level of the main territorial hierarchy (`state_code`, `division_name` and so on), the forest feature code, fire radiative power, confidence, longitude and latitude. Alert exports add the alert ID, queue, creation time, the current workflow step with its label and time, and the incident ID. Alerts keep the class, reason and units they were routed with.

Every export carries a note of its source, filters, time window, coordinate system, request and generation times, row count and the requesting role. It never names the people involved. GeoPackage and Excel files hold it in the `export_note` and `export_fields` tables. Every other format is a zip file with the note in `README.txt` beside the data. Shapefile field names are limited to 10 characters. The note lists each field's short name, such as `divisio_cd` for `division_code`. Each export, and each download, is recorded in the audit log with its filters and row count.

Run `make export-test-integration` with the stack, Keycloak and geo worker running. It exports as each demo user and checks that the rows match what the same user sees in the alert queue and in detection tiles. It checks that `no.role` is refused, that one user cannot read another's export, that expiry removes the file and that the audit rows exist. It then opens every downloaded format with OGR in the geo worker container and prints its layers, geometry, coordinate system, fields and feature count.

## GIS link

Desktop GIS such as QGIS and ArcGIS Pro can read a workspace live, without
downloading a file. The core API serves a read-only
[OGC API – Features](https://ogcapi.ogc.org/features/) service for each
workspace at `/ogc/workspaces/{slug}`. It implements Part 1, Core, with GeoJSON
responses and the OpenAPI 3.0 definition. Coordinates are WGS 84 longitude and
latitude (CRS84), the only coordinate system offered.

### What each person sees

Every answer contains only what the person sees in the web app. The rules are
the same SQL as the endpoints the web app uses:

| Collection | ID | Rule |
|---|---|---|
| Detections | `detections` | As [detection tiles](#detection-tiles): `match_under_grant()` on the match's paths. A root grant covers every match, including matches with no unit. All four forest classes are included. |
| Alerts | `alerts` | As the [alert queue](#alert-queues): `alert_path_visible()` on the alert's own routing snapshot, so the review and triage queues need a grant at division level or above. |
| Published forest and known heat sources | `layer-{layer slug}`, such as `layer-forest` | As the [reference map](#workspace-map): every member of the workspace sees the published version. |
| Current hierarchy units | `units-{hierarchy}-{level}`, such as `units-territorial-beat` | As the reference map. One collection for each level of each hierarchy. |

A person without a role in the workspace gets HTTP 403 for every page,
including the landing page. A hidden or missing feature returns 404.

Detections have the detection ID, observation time in UTC, source, product,
satellite, forest class, triage reason, forest feature code, fire radiative
power and confidence. Then come `place_hierarchy`, the code and name of every
level of the main territorial hierarchy (`state_code`, `beat_name` and so on,
as in file exports) and `places`, the JSON text that detection tiles carry.
Alerts add the alert ID, queue, current workflow step with its label and time,
`overdue` and creation time, and keep the class, reason and units they were
routed with. Layers have the feature code and name, and forest adds
`legal_status`. Units have the code, name, path, hierarchy and level.

### Requests

| Path | Returns |
|---|---|
| `/ogc/workspaces/{slug}` | The landing page with links to the rest |
| `/ogc/workspaces/{slug}/conformance` | The conformance classes: core, GeoJSON and OpenAPI 3.0 |
| `/ogc/workspaces/{slug}/api` | The OpenAPI 3.0 definition, which tells clients the page size limits |
| `/ogc/workspaces/{slug}/collections` and `/collections/{id}` | The collections the workspace offers, with the workspace boundary as their extent |
| `/ogc/workspaces/{slug}/collections/{id}/items` | One page of features as a GeoJSON feature collection |
| `/ogc/workspaces/{slug}/collections/{id}/items/{featureId}` | One feature |

The items request accepts:

| Parameter | Meaning |
|---|---|
| `limit` | Features on one page, 100 by default. Values above 1,000 are reduced to 1,000. |
| `bbox` | `west,south,east,north` in degrees. Six numbers are accepted and the heights are ignored. |
| `datetime` | Detections and alerts only, on observation time: one RFC 3339 time, or an interval such as `2026-03-01T00:00:00Z/..` or `../2026-03-31T23:59:59Z`. Layers and units ignore it. |
| `forest_class` | Detections and alerts only: classes separated by commas, such as `inside,near_edge` |
| `queue`, `step` | Alerts only: one queue (`unit`, `edge_review` or `triage`) and one current workflow step, such as `new` |
| `cursor` | Set by the `next` link. Follow the link rather than building it. |

Detections and alerts are newest first, and layers and units are in ID order.
When there are more features, the page has a `next` link. Each page continues
after the last row of the previous one, by observation time and ID, on the
existing indexes of matches and alerts by workspace and observation time. No
rows are skipped with an offset, so later pages cost no more than the first.
Pages do not report a total count. Unknown
parameters, a coordinate system other than CRS84 and a malformed `bbox` or
`datetime` return HTTP 400. Links use the request's host. Behind a proxy, keep
the `Host` header and set `X-Forwarded-Proto: https`.

Incidents will be one more collection when they are built.

### Personal access keys

Desktop GIS cannot easily use the browser sign-in, so each person can make
personal access keys in the web app (**GIS link keys** in the account menu),
or with these endpoints and a signed-in bearer token:

- `GET /api/v1/access-keys` lists the caller's unrevoked keys with `id`,
  `name`, `hint` (the first six characters after `fimra_`), `createdAt`,
  `expiresAt`, `lastUsedAt` and `expired`.
- `POST /api/v1/access-keys` takes `{"name": "Office laptop", "expiresInDays": 90}`
  and returns HTTP 201 with the key in `key`. This is the only time the key is
  shown. Leave out `expiresInDays` (1 to 730) for a key that does not expire.
  A person can hold 20 unexpired keys.
- `DELETE /api/v1/access-keys/{id}` revokes one of the caller's own keys at
  once. Another person's key returns 404.

Serve the API over HTTPS outside development, because HTTP Basic sends the key
with every request. The database stores only a SHA-256 hash of each key. A key acts with exactly
its owner's grants at the time of each request, so removing a role takes effect
at once. Revoke the keys of anyone who leaves.

A key is accepted **only on the `/ogc/` routes**, in either of these ways:

- **HTTP Basic**, with any user name and the key as the password. This is what
  QGIS and most GIS software offer.
- The **`X-API-Key` header**, for scripts and for QGIS's API header method.

Every other route refuses a key with HTTP 401, including the key endpoints
themselves. A missing, malformed, revoked or expired key gets HTTP 401 with a
Basic challenge. A signed-in bearer token also works on the `/ogc/` routes.

The audit log records `access_key.create` and `access_key.revoke` with the
person's subject, and `access_key.use` with the key as the actor (`api_key`),
the owner's subject, the workspace and the path. Use is recorded with the
key's last-used time at most once a minute for each key, so the log shows when
and from where a key was used without a row for every map pan.

### Connecting QGIS

1. In the web app, open the account menu and choose **GIS link keys**. Make a
   key and copy it. Copy the service address shown on the same page, for
   example `http://127.0.0.1:18090/ogc/workspaces/demo`.
2. In QGIS 3, choose **Layer › Add Layer › Add WFS / OGC API – Features Layer**,
   then **New**.
3. Give the connection a name and paste the address as the URL. Set
   **Version** to **OGC API – Features** if QGIS asks.
4. Under **Authentication**, add a configuration with the **Basic** method.
   Enter any user name and paste the key as the password. QGIS keeps it in its
   encrypted password store.
5. Choose **OK**, then **Connect**. Pick `detections`, `alerts` or a layer and
   choose **Add**.

To read the service from the command line with GDAL's OAPIF driver:

```sh
ogrinfo -ro -so -al --config GDAL_HTTP_AUTH BASIC \
  --config GDAL_HTTP_USERPWD "gis:$KEY" \
  OAPIF:http://127.0.0.1:18090/ogc/workspaces/demo
```

`make api-test-integration` makes a key for each demo user and compares every
collection with the detection tiles, the alert queue and the reference data
for that user. It checks single features, bounding boxes, paging, refused,
revoked and expired keys and the audit rows. It then runs `ogrinfo` in the
geo worker's GDAL base image on the host network. ogrinfo reads every
collection, the state administrator with HTTP Basic and the division officer
with the `X-API-Key` header, and the test compares each count with the API.

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
transaction. For a new alert this happens after it joins its incident. A
recipient gets one new-alert notification per incident and channel: the first
member alert they can see notifies them, and later members that they can also
see do not. A member they could not see before, such as one in another unit,
still notifies them. Merged incidents count as one. A background check records an overdue event once for each step
entry, including when an alert returns to a step it has visited before. Each
event, recipient and channel has one database row, even when multiple grants
or rules match. Replaying a feed or re-matching an existing alert leaves that
row alone. A re-match that opens a new alert can notify its officers.

### Endpoints

All endpoints require a bearer token.

- `GET /api/v1/notifications` returns `items`, `unreadCount` and `hasMore`.
  The result starts with unread items and includes up to 50 per page. Use
  `offset=50` for the next page. Alert items include `forestClass`, `beatName`
  and `divisionName` from their routing snapshot, and the alert's
  `incidentId`. Missing values are null.
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
- `POST .../setup/steps/{step}` marks `layers`, `firms`, `forms` or `invitations` as `done` or `skipped`. Layers cannot be skipped and need every level and the forest layer published. FIRMS can be done only with a key. Starter forms can be done only once every starter form is in the workspace: turn them on with the [starter pack](#starter-pack) first.
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


### Workspace map

`GET /api/v1/workspaces/{slug}` returns the workspace boundary as
`[west, south, east, north]` in degrees (null before a boundary is set) and
`levels` ordered by hierarchy and depth. Each level has a hierarchy slug,
depth, level slug and translated labels keyed by language.

`GET /api/v1/workspaces/{slug}/tiles/reference/{z}/{x}/{y}.mvt` serves
current units, published forest and published exclusions. Add `?outline=1`
to download only the top-level outlines. Empty tiles return 204. Both routes
require a bearer token and a workspace role. Members see the whole reference
map. Tiles are privately cached and revalidated against published version IDs.
The metadata includes a `version` for the published reference layers. The map
checks it once a minute and reloads visible reference tiles when it changes.
