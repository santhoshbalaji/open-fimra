# Contributing to Open FIMRA

Thank you for helping. The project is at an early stage: the plan is in [PLAN.md](PLAN.md), and the build follows the slices in [section 15](PLAN.md#15-next-steps).

## Ground rules

- **No real agency data.** Do not commit real forest boundaries, staff details, field reports or anything taken from an existing government system. Demo and test data must be synthetic. `make check` blocks geospatial files outside `seed/` and test fixture folders.
- **No secrets.** Keep passwords and keys in a local `.env` file, which is never committed. `.env.example` lists the variables.
- **Independence.** Open FIMRA is not an official Government of India system and is not affiliated with NASA, the Forest Survey of India or the National Remote Sensing Centre. Do not use their logos or suggest an endorsement.
- **Licence.** Contributions are accepted under the [AGPL-3.0](LICENSE), with the attribution in [NOTICE](NOTICE).

## Setting up

You need Git, GNU Make and Docker with the Compose plugin. Rootless Docker works.

```bash
cp .env.example .env    # optional: change ports or passwords
make dev                # start PostGIS and Keycloak, and wait until both are healthy
make help               # list every command
```

The development stack binds to 127.0.0.1 only:

| Service | Address | Default sign-in |
|---|---|---|
| PostgreSQL with PostGIS | `127.0.0.1:15432`, database `fimra` | `fimra` / `fimra-dev` |
| Keycloak admin console | <http://127.0.0.1:18180> | `admin` / `admin-dev` |

On first start Keycloak imports the development realm `open-fimra` from [deploy/compose/keycloak](deploy/compose/keycloak/open-fimra-realm.json). It has three clients:

| Client | Use |
|---|---|
| `open-fimra-web` | The web app: public client, authorisation code with PKCE, redirects to `http://127.0.0.1:4200` and `http://localhost:4200` |
| `open-fimra-api` | The core API, as the token audience |
| `open-fimra-dev-cli` | Password sign-in for local tests only. A real installation never has this client. |

It also has demo users `state.admin`, `division.officer`, `beat.guard` and `no.role`, all with the password `demo` and fixed user IDs ending `…a1` to `…a4`. They are made up.

These defaults are for local development only. `make dev-down` stops the stack and keeps its data; `make dev-reset` deletes the data too.

Database migrations need Go (the version in CI is the current stable release). `make db-migrate` applies them, and `make db-test` checks that they roll back cleanly and runs the schema tests. See [db/migrations](db/migrations/README.md).

## Running the web shell

With the development stack running, use Node.js 24.15 or later in the 24.x series with Corepack. Run `make web-install`, then `make web-dev`. Open <http://127.0.0.1:4200> and sign in with a demo user. Before pushing web changes, run `make web-lint web-test web-build`. The [web app guide](apps/web/README.md) covers remote access and configuration.

## Running the core API

With Go 1.26 or later, run `make dev`, `make db-migrate` and `make api-test-integration`. The integration test loads the demo workspace and grants, signs in with the four demo accounts and checks the answers from the API. Run `make demo-detections` to put synthetic points on the map, then run `make api-run` to serve the API on <http://127.0.0.1:18090>.

Run `make api-lint api-test` for formatting, Go vet and unit tests. The integration tests check the reference matches, replay a synthetic feed and check which tile features each demo grant can see. To replay a file yourself, run `make ingest-fixture FILE=packages/connectors/firms/fixtures/viirs.csv`. See the [core API guide](services/api/README.md) for settings and endpoints.

## Running more than one stack

Two checkouts that use the same development stack share one database, and their migrations can clash. Give each checkout its own stack with a project name and free ports, for example in its `.env`:

```bash
COMPOSE_PROJECT_NAME=open-fimra-second
FIMRA_DB_PORT=15433
FIMRA_KEYCLOAK_PORT=18181
FIMRA_HTTP_ADDR=127.0.0.1:18091
```

Each stack uses about 2.5 GB of memory.

## Making a change

1. Branch from `main`.
2. Keep each pull request to one slice or one clear change.
3. Run `make check` before pushing, and `make db-test` if you changed the database. Use a disposable database for `make db-test` because it rolls back every migration. Continuous integration runs the same checks.
4. Describe what changed and how you tested it in the pull request.

Write documentation and interface text in plain English: short sentences, everyday words, and no unexplained abbreviations.
