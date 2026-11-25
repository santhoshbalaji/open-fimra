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

These defaults are for local development only. `make dev-down` stops the stack and keeps its data; `make dev-reset` deletes the data too.

Database migrations need Go (the version in CI is the current stable release). `make db-migrate` applies them, and `make db-test` checks that they roll back cleanly and runs the schema tests. See [db/migrations](db/migrations/README.md).

## Making a change

1. Branch from `main`.
2. Keep each pull request to one slice or one clear change.
3. Run `make check` before pushing, and `make db-test` if you changed the database. Continuous integration runs the same checks.
4. Describe what changed and how you tested it in the pull request.

Write documentation and interface text in plain English: short sentences, everyday words, and no unexplained abbreviations.
