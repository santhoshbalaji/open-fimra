# Synthetic demo data

Everything here is made up. **Demo State** is a fictional state drawn over open sea in the Bay of Bengal, so no shape can be mistaken for a real forest or administrative boundary.

Never add real forest boundaries, staff details or field reports to this folder or anywhere else in the repository. Real agency data stays on agency servers.

## What is in `demo-state/`

| File | Contents |
|---|---|
| `state.geojson`, `circles.geojson`, `divisions.geojson`, `ranges.geojson`, `beats.geojson` | A territorial hierarchy: 1 state, 2 circles, 4 divisions, 8 ranges and 24 beats, each with `code`, `name` and `parent_code`, as an agency would upload them |
| `sections.geojson`, `beats-with-sections.geojson` | Sixteen sections and a beat upload for the optional section preset. Eight beats report directly to a range. Range 1 skips sections, and Range 8 has both kinds of branch. |
| `protected-areas.geojson` | The fictional Demo Tiger Reserve, with core and buffer zones and a separate wildlife division in the loaded hierarchy |
| `civil-districts.geojson`, `committee-areas.geojson` | A civil district and a committee area that each cross the Hill and Valley division boundary |
| `forest.geojson` | Five forest features with a legal status |
| `exclusions.geojson` | One known heat source (a brick kiln) |
| `reference-detections.geojson` | Eleven test detections, each with its expected class, beats, triage reason and default queue |
| `load.sql` | Loads the demo workspace, hierarchy, layers and features into the database |
| `roles.sql` | Adds the demo role grants after `load.sql`. It also works with a previously loaded demo workspace. |
| `operations-roles.sql` | Adds the accounts and scoped grants for the operations replay |
| `reference.sql` | The reference detections as a temporary table |

All of it is written by [`generate.py`](generate.py), which uses only the Python standard library and always writes the same bytes. Do not edit these files by hand.

## Deliberate defects

Real layers are messy, so the demo data is too. These cases double as the spatial reference test set that a release gate in PLAN.md requires.

| Case | Where |
|---|---|
| Forest with a hole | Forest F02 has a clearing. A detection in it is more than 500 m from the forest, so it is *outside*. |
| Detection on a shared boundary | Exactly on the edge between beats B05 and B08, inside forest F01. It goes to triage as `shared_boundary`. |
| Overlapping beats | Beat B17 bulges about 430 m into B20. A detection in the overlap, inside forest F05, goes to triage as `overlapping_units`. |
| Forest outside every beat | Forest F03 crosses the eastern state edge. A detection in the part with no beat goes to triage as `no_unit`. |
| Missing codes | Beat B16 has no code and beat B22 has no parent code in `beats.geojson`. One forest has no code. An import sends these to review; `load.sql` uses the codes their positions imply, so the demo works. |
| Known heat source | A detection inside the brick kiln is a `fixed_heat_source`. |
| Near the edge | A detection about 300 m outside forest F02 is `near_edge` (the demo's near-edge distance is 500 m). |
| Far away | A detection 1.5° away is outside the workspace margin and is never matched. |

## Commands

```bash
make seed        # regenerate demo-state/
make seed-test   # check demo-state/ is up to date, load it and run the reference check
make demo-replay # load and verify the operations cases in a fresh development database
make demo-replay-test # test the same replay in a rolled-back copy of the state
```

The reference check in [`tests/reference-check.sql`](tests/reference-check.sql) states the meaning of each result in plain rules. The real matching code must give the same answers.

Run `make demo-replay` on a clean Demo State, before loading the individual
examples. It can also load Demo State into an empty migrated database. Running
it again leaves the cases as they are. It calls the API handlers in-process,
including the same attachment and submission paths as the field app. The
replay matches its observations only in Demo State. The
fictional photo is generated during the replay and goes into the local
attachment store. Use the same `FIMRA_ATTACHMENT_DIR` when running the API.
For this demo the command can sign with a temporary settings key. To keep
signing further extracts, configure `FIMRA_SETTINGS_KEY` before replaying.
Open the [operations tour](../docs/demo-scenarios.md) to explore the cases.

Run `make demo-dispatch` in a seeded development database to try the
fictional reserve dispatch rules and duty roster. The reserve core alert
goes to Demo Wildlife Division. The overlapping territorial alert still
needs a unit. Sign in as the demo state administrator to see the roster
under **Settings → Duty roster**.

## Demo accounts

The import file in `deploy/compose/keycloak` gives each local account the
password `demo`. The base seed grants access to the first three accounts.
The replay adds the operational grants. The shared worktree realm keeps
its original users, so the replay and its integration test use their own
local verifier without changing that realm. To sign in with the new
accounts, use a fresh development realm imported from this checkout.

| Account | Access |
|---|---|
| `state.admin` | State administrator who configures dispatch and duty and signs the aggregate export |
| `division.officer` | Hill Division officer, outgoing West control-room watch and holder of the ridge offence grant |
| `beat.guard` | Beat 01 guard who can see the inaccessible Brook Track point |
| `no.role` | Signed-in user with no workspace access |
| `duty.officer` | Incoming West control-room watch, with temporary cover across Hill and Valley divisions |
| `circle.reviewer` | West Circle reviewer who returns the estimate and approves the survey |
| `division.delegate` | Valley Division officer who can see the request for help and file the division return |
| `range.officer` | Range 2 field officer who records an update on the ridge case |
| `section.forester` | Reed Section forester who checks the report of smoke |
| `beat.alternate` | Beat 05 guard who syncs the GPS point and photo and files the assessments |
| `squad.leader` | Wildlife Division crew leader with temporary cover in Valley Division |
| `community.caller` | Committee caller who can submit public reports in Beat 05 |
| `district.liaison` | Read-only access to the two ranges in the fictional district |
| `river.officer` | River Division officer who files the nil return |
