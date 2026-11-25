# Core API

The core service uses the development sign-in server to verify access tokens. It reads each signed-in user's role grants from PostgreSQL. A River worker runs a heartbeat on startup and every five minutes. The heartbeat leaves application data alone.

## Run locally

From the repository root, with Go 1.26 or later:

```sh
make dev
make db-migrate
make api-test-integration
make demo-detections
make api-run
```

The integration tests check sign-in with all four demo users. They also exercise reference matching, replay a synthetic feed across two months and decode detection tiles to check access and filters. Run it before starting the API, so the test worker can pick up its scheduled heartbeat. `make api-test` and `make api-lint` run without the development stack.

| Variable | Development default |
|---|---|
| `FIMRA_DATABASE_URL` | `postgres://fimra:fimra-dev@127.0.0.1:15432/fimra?sslmode=disable` |
| `FIMRA_OIDC_ISSUER` | `http://127.0.0.1:18180/realms/open-fimra` |
| `FIMRA_OIDC_AUDIENCE` | `open-fimra-api` |
| `FIMRA_HTTP_ADDR` | `127.0.0.1:18090` |
| `FIMRA_FIRMS_MAP_KEY` | Empty, which disables live polling |
| `FIMRA_CORS_ORIGINS` | `http://127.0.0.1:4200,http://localhost:4200` |

When run through Make, the database URL defaults to `DATABASE_URL` and the issuer uses `FIMRA_KEYCLOAK_PORT`. Use these defaults with the local stack.

## Endpoints

- `GET /healthz` returns 200 while the service is running.
- `GET /readyz` returns 200 when PostgreSQL responds and 503 when it cannot be reached.
- `GET /api/v1/me` takes an access token in the `Authorization: Bearer` header. It returns the caller's `subject`, `username` and `roleAssignments`. Each assignment includes the workspace slug and name, hierarchy slug, role, unit code and name, level slug, path and beat count. A grant to one beat counts that beat. A signed-in user without grants gets an empty list. A missing or rejected token gets a JSON error and a bearer challenge with HTTP 401.

The service finds the signing keys through OpenID Connect discovery and caches them. It checks the issuer, audience and token times. It allows 30 seconds for differences between clocks.

## Detection tiles

`GET /api/v1/workspaces/{slug}/tiles/detections/{z}/{x}/{y}.mvt` takes a bearer token. It returns a Mapbox vector tile with content type `application/vnd.mapbox-vector-tile` and one layer named `detections`. An allowed request with no features returns HTTP 204. A missing or invalid token gets 401. A signed-in caller without a role in the workspace gets 403.

A grant covers matched paths at or below the granted unit in that hierarchy. A grant at a hierarchy root covers every match in the workspace, including matches with no unit.

- `hours` is the number of hours to look back from the current time. It defaults to 168 and accepts integers from 1 through 720.
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

With a map key, a River job starts polling on service startup and runs every 15 minutes. It requests two UTC calendar days for each of the four products and each workspace with a boundary. The database keeps one detection per feed observation and matches each new detection to every workspace in range. Each poll or import records its time window, counts and outcome in `ingest_runs`.

Use `make ingest-fixture FILE=path/to/file.csv PRODUCT=MODIS_NRT` to import a file without calling FIRMS. The product defaults to `VIIRS_SNPP_NRT`. `make demo-detections` uses the synthetic demo file and shifts its observation times to the current hour.

## Database queries

Regenerate the checked-in Go queries from this directory after editing `queries.sql`:

```sh
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
```

The role query starts from the signed-in user's ID. The tile query checks grants and reads matches in one SQL statement. The ingest statement saves detections and builds their match snapshots in the same transaction.
