# Synthetic demo data

Everything here is made up. **Demo State** is a fictional state drawn over open sea in the Bay of Bengal, so no shape can be mistaken for a real forest or administrative boundary.

Never add real forest boundaries, staff details or field reports to this folder or anywhere else in the repository. Real agency data stays on agency servers.

## What is in `demo-state/`

| File | Contents |
|---|---|
| `state.geojson`, `circles.geojson`, `divisions.geojson`, `ranges.geojson`, `beats.geojson` | A territorial hierarchy: 1 state, 2 circles, 4 divisions, 8 ranges and 24 beats, each with `code`, `name` and `parent_code`, as an agency would upload them |
| `forest.geojson` | Five forest features with a legal status |
| `exclusions.geojson` | One known heat source (a brick kiln) |
| `reference-detections.geojson` | Eleven test detections, each with its expected class, beats, triage reason and default queue |
| `load.sql` | Loads the demo workspace, hierarchy, layers and features into the database |
| `roles.sql` | Adds the demo role grants after `load.sql`. It also works with a previously loaded demo workspace. |
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
```

The reference check in [`tests/reference-check.sql`](tests/reference-check.sql) states the meaning of each result in plain rules. The real matching code must give the same answers.
