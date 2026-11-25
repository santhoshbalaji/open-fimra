# Core API

The core service uses the development sign-in server to verify access tokens. It reads each signed-in user's role grants from PostgreSQL. A River worker runs a heartbeat on startup and every five minutes. The heartbeat leaves application data alone.

## Run locally

From the repository root, with Go 1.26 or later:

```sh
make dev
make db-migrate
make api-test-integration
make api-run
```

The integration test loads the fictional workspace if it is missing, adds any missing demo role grants, and checks all four demo users. Run it before starting the API, so the test worker can pick up its scheduled heartbeat. `make api-test` and `make api-lint` run without the development stack.

| Variable | Development default |
|---|---|
| `FIMRA_DATABASE_URL` | `postgres://fimra:fimra-dev@127.0.0.1:15432/fimra?sslmode=disable` |
| `FIMRA_OIDC_ISSUER` | `http://127.0.0.1:18180/realms/open-fimra` |
| `FIMRA_OIDC_AUDIENCE` | `open-fimra-api` |
| `FIMRA_HTTP_ADDR` | `127.0.0.1:18090` |

When run through Make, the database URL defaults to `DATABASE_URL` and the issuer uses `FIMRA_KEYCLOAK_PORT`. Use these defaults with the local stack.

## Endpoints

- `GET /healthz` returns 200 while the service is running.
- `GET /readyz` returns 200 when PostgreSQL responds and 503 when it cannot be reached.
- `GET /api/v1/me` takes an access token in the `Authorization: Bearer` header. It returns the caller's `subject`, `username` and `roleAssignments`. Each assignment includes the workspace slug and name, hierarchy slug, role, unit code and name, level slug, path and beat count. A grant to one beat counts that beat. A signed-in user without grants gets an empty list. A missing or rejected token gets a JSON error and a bearer challenge with HTTP 401.

The service finds the signing keys through OpenID Connect discovery and caches them. It checks the issuer, audience and token times. It allows 30 seconds for differences between clocks.

## Database queries

Regenerate the checked-in Go queries from this directory after editing `queries.sql`:

```sh
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
```

The role query starts from the signed-in user's ID. A later slice will run permission checks and data reads in a request transaction, set the caller's identity for that transaction and enforce workspace and path access with PostgreSQL row-level security. The generated queries can already use a transaction through `WithTx`.
