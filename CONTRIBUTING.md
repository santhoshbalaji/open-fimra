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

On first start Keycloak imports the development realm `open-fimra` from [deploy/compose/keycloak](deploy/compose/keycloak/open-fimra-realm.json). It has four clients:

| Client | Use |
|---|---|
| `open-fimra-web` | The web app: public client, authorisation code with PKCE, accepts sign-in and sign-out on ports 4200 through 4209 on both loopback names |
| `open-fimra-mobile` | The field app: public client, authorisation code with a required S256 proof key and no password sign-in. It returns to `org.openfimra.field:/callback`, and to `/auth.html` on ports 4300 through 4309 on both loopback names for the browser test build only. |
| `open-fimra-api` | The core API, as the token audience |
| `open-fimra-dev-cli` | Password sign-in for local tests only. A real installation never has this client. |

It also has demo users `state.admin`, `division.officer`, `beat.guard` and `no.role`, all with the password `demo` and fixed user IDs ending `…a1` to `…a4`. They are made up.

These defaults are for local development only. `make dev-down` stops the stack and keeps its data; `make dev-reset` deletes the data too.

Database migrations need Go (the version in CI is the current stable release). `make db-migrate` applies them, and `make db-test` checks that they roll back cleanly and runs the schema tests. See [db/migrations](db/migrations/README.md).

## Running the web shell

With the development stack running, use Node.js 24.15 or later in the 24.x series with Corepack. Run `make web-install`, then `make web-dev`. Open <http://127.0.0.1:4200> and sign in with a demo user. Before pushing web changes, run `make web-lint web-test web-build`. The [web app guide](apps/web/README.md) covers remote access and configuration.

## Running the core API

With Go 1.26 or later, run `make dev`, `make db-migrate` and `make api-test-integration`. The integration test loads the demo workspace and grants, signs in with the four demo accounts and checks the answers from the API. Run `make demo-detections` to put synthetic points on the map, then run `make api-run` to serve the API on <http://127.0.0.1:18090>.

Run `make api-lint api-test` for formatting, Go vet and unit tests. The integration tests check the reference matches, replay a synthetic feed and check which tile features each demo grant can see. They also check that a backfill is safe to repeat and that a silent feed opens exactly one feed-health alert. They read the GIS link with each demo user's personal access key, compare it with the tiles and the alert queue, and read it again with `ogrinfo` in the geo worker's GDAL base image. To replay a file yourself, run `make ingest-fixture FILE=packages/connectors/firms/fixtures/viirs.csv`. Run `make backfill-demo` to backfill the demo workspace from synthetic standard-processing files, and `make feed-health` to check feed health once. See the [core API guide](services/api/README.md) for settings and endpoints.

## Changing the form format

The form format lives in [packages/form-schema](packages/form-schema/README.md):
a plain-English specification, the JSON Schema, a Go validator and a
TypeScript validator. Both validators must give the same messages. Run
`make web-install form-schema-test` before pushing changes to the format, its
validators or its examples. Example forms stay synthetic and use the fictional
Demo State.

## Running forms and field reports

Forms, submissions, attachments and approvals live in the core API. With the
development stack running and migrated, `make api-test-integration` publishes
the synthetic example forms, takes a synthetic alert through its first report
and checks every request as each demo user. `make attachments-s3-test` checks
S3-compatible attachment storage against a small server that runs for the test
only. With Keycloak running, `make submission-export-test-integration` makes its
own database and geo worker, has the division officer approve a first report and
checks that each demo user's export of field reports holds only what they may
see. Attachments are kept in the ignored `.form-attachments/` directory. If
you set `FIMRA_ATTACHMENT_DIR` in `.env`, use a separate directory for each
stack. See the [forms endpoints](services/api/README.md#forms-and-field-reports).
Run `make starter-pack-test` after changing the starter forms in
[packages/starter-pack](packages/starter-pack/README.md), and
`make wizard-test-integration` to take an alert through the default workflow
with them.

National, state and division templates are part of the same test. To try the
designer in a browser, run `make dev web-install form-designer-walkthrough`.
It loads the demo workspace into its own database (`fimra_forms_walkthrough`),
adds a synthetic national template with one report, and drives headless
Chromium in the Playwright container: the state extends the template by
keyboard and by drag and drop, publishes it, and checks that the earlier report
still shows with the version it used. Screenshots go to
`~/open-fimra-screenshots/form-designer`. Set `WEB_PORT` to serve the app on
another port from 4200 through 4209.

## Converting forms to and from XLSForm

The geo worker converts forms between the form format and XLSForm, the
spreadsheet format of ODK. Run `make geo-test` for the unit tests, including a
round trip of every example form, and `make xlsform-odk-check` to convert the
exported examples with pyxform. With `make db-up db-migrate`, run
`make xlsform-test-integration` for the conversion jobs. Try a file with
`make xlsform-import FILE=form.xlsx` or `make xlsform-export FILE=form.json`.
Test workbooks are written by the tests, so no spreadsheet is committed. See
the [worker guide](services/geo-worker/README.md#how-xlsform-conversion-works).

## Running the field app

The Flutter field app builds and tests in a container, so you need only Docker. Run `make mobile-lint mobile-test mobile-build` for the checks and an Android test build. With the development stack running, `make mobile-test-integration` signs the demo users in with PKCE and checks what each one sees, and `make mobile-walkthrough` drives the browser build in headless Chromium and saves screenshots outside the repository. Both use their own database beside the development one. See the [field app guide](apps/mobile/README.md), which also covers running the app on a phone.

If a disposable standalone stack predates the field app client, `make dev-reset dev` deletes its data and imports the current realm.

## Trying push notifications

The field app gets alert notifications through UnifiedPush and a push server
the agency runs. Run `make push-up` to start the local push server (ntfy) on
<http://127.0.0.1:18110>, and set `FIMRA_PUSH_SERVERS=http://127.0.0.1:18110`
in `.env` before `make api-run`. For parallel worktrees, use the shared push server on port 18108. `make api-test-integration` starts ntfy and checks
registration, delivery, a replayed feed and remote wipe with synthetic phones.
Google's Firebase Cloud Messaging is optional and never needed to build or
test. See [field phones and push](services/api/README.md#field-phones-and-push).

## Running layer imports and publishing

Run `make dev db-migrate geo-worker-up` to start the layer import worker with
the development stack. The worker has no published ports and all its geospatial
libraries are inside the container. Run `make geo-test geo-test-integration`
before pushing changes to imports or publishing. The integration tests check
synthetic drafts against the demo seed and publish a changed beat boundary.
The publishing test verifies updated detection matches, grant confirmation and
historical alerts in the running API.

Uploads are kept in the ignored `.geo-uploads/` directory in each checkout.
If you set `FIMRA_UPLOAD_DIR` in `.env`, use a separate directory for each stack.
See the [worker guide](services/geo-worker/README.md) and the
[upload endpoints](services/api/README.md#layer-imports).

## Running file exports

The same geo worker writes export files. With the stack and worker running, run
`make geo-test export-test-integration` before pushing changes to exports. The
first writes every format from synthetic rows and reads it back with OGR. The
second exports as each demo user, compares the rows with the alert queue and
detection tiles, and reads every downloaded format back with OGR.

Export files are kept in the ignored `.geo-exports/` directory until they
expire. If you set `FIMRA_EXPORT_DIR` in `.env`, use a separate directory for
each stack. See the [export endpoints](services/api/README.md#file-exports).

## Running situation reports

The core API works out situation reports and the geo worker draws their PDF
files. With the stack and worker running (`make dev db-migrate geo-worker-up`),
run `make api-test-integration` and `make situation-report-test-integration`
before pushing changes to reports or the dashboard. The first compares each
demo user's report with the dashboard and with direct database counts, and
checks who may report on which unit. The second makes a PDF as each demo user
and reads it back with pypdf. `make geo-test` draws synthetic reports. PDF
files are written by the worker and tests only, so `.gitignore` keeps them out
of the repository. See the [report endpoints](services/api/README.md#situation-reports).

## Parallel worktrees

Use one backend stack for all worktrees on the same Linux machine. In each worktree,
create a short `.env`. For example, use slot 1:

```bash
FIMRA_SHARED=1
FIMRA_HTTP_ADDR=127.0.0.1:18091
WEB_PORT=4201
MOBILE_WEB_PORT=4301
```

Give each worktree a free slot from 0 through 9. Use API port 18090 plus the
slot, web port 4200 plus the slot, and field app browser port 4300 plus the slot.
The imported development realm accepts the web and field app ports on
`localhost` and `127.0.0.1`. Keep the realm and its demo users as imported.

Run `make shared-up` to start the shared stack and `make shared-ps` to see it.
The project is `open-fimra-shared`. All shared ports bind to `127.0.0.1`:

| Service | Port |
|---|---:|
| PostgreSQL | 15431 |
| Keycloak | 18179 |
| Mailpit SMTP | 11008 |
| Mailpit web UI | 18008 |
| ntfy | 18108 |

Run `make worktree-db` to create, migrate and seed your database. The name comes
from the worktree directory. You can set a unique `FIMRA_DB_NAME` in `.env`
using up to 50 lowercase letters, digits and underscores. Start with a letter
and avoid double or trailing underscores. Tests that need a fresh database add a suffix to the worktree name. Mail and
push tests filter by recipient, sender or topic.

Run `make api-run` and `make web-dev` in the worktree. If you need a geo worker,
`make geo-worker-up` starts one for your database and the worktree's upload and
export directories. Use the default folders or give each worktree its own.
To try email or push from the API, point `FIMRA_SMTP_ADDR` or
`FIMRA_PUSH_SERVERS` at the shared ports above. For email links, set
`FIMRA_WEB_URL` to the worktree's web address.

Stop your API and tests and run `make geo-worker-down` before database cleanup.
`make worktree-db-reset` drops your databases and recreates the seeded one.
Run `make worktree-db-drop` before deleting or moving the worktree. Shared mode
refuses `make dev-down` and `make dev-reset`. Use `make shared-down` only when
everyone can stop, because it takes the backend offline for every worktree.
The standalone project `open-fimra-dev` remains separate on port 15432.

## Making a change

1. Branch from `main`.
2. Keep each pull request to one slice or one clear change.
3. Run `make check` before pushing, and `make db-test` if you changed the database. Use a disposable database for `make db-test` because it rolls back every migration. Continuous integration runs the same checks.
4. Describe what changed and how you tested it in the pull request.

Write documentation and interface text in plain English: short sentences, everyday words, and no unexplained abbreviations.

## Running the first-run wizard

A fresh install starts with the setup wizard instead of the demo data. To try it by hand, use an empty, migrated database without `make seed-test` or `make demo-detections`. Set `FIMRA_SETTINGS_KEY` in `.env` (make one with `openssl rand -base64 32`) so the FIRMS key can be saved. Start the API and copy the `setupCode` it prints, then sign in as `state.admin`.

Run these before pushing changes to setup:

```bash
make dev                      # the tests build the geo worker image and start their own copy
make wizard-test-integration  # an empty database to a working alert map and starter forms, through the wizard's endpoints
make wizard-walkthrough       # the same in headless Chromium, with screenshots outside the repository
```

Both create their own database beside the development one (`fimra_wizard_test` and `fimra_walkthrough`) and run a geo worker against it, so they leave the demo data alone. Both use a fake FIRMS server and the mail catcher, so nothing reaches NASA or a real mailbox.

## Trying email notifications

Run `make mail-up` to start the local mail catcher. Add these settings to your
local `.env` and restart the API. Sign in as a demo user before ingesting new
detections so the API has the verified address from that user's token.

```bash
FIMRA_SMTP_ADDR=127.0.0.1:11025
FIMRA_SMTP_FROM=open-fimra@example.invalid
FIMRA_SMTP_SECURITY=local
FIMRA_WEB_URL=http://127.0.0.1:4200
```

Open the mail catcher at <http://127.0.0.1:18025>. In shared mode, use SMTP port
11008 and open the mail catcher on port 18008. The test target
`make api-test-integration` starts the mail catcher and checks delivery,
retry and replay using synthetic addresses.
