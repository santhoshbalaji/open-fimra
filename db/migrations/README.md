# Database migrations

Plain SQL migrations for PostgreSQL with PostGIS, run with [goose](https://github.com/pressly/goose). Each file has an `Up` and a `Down` section, and every migration must roll back cleanly.

| Migration | Creates |
|---|---|
| `00001_extensions` | PostGIS and `ltree` |
| `00002_workspaces_and_hierarchy` | Workspaces, hierarchies, hierarchy levels and units with `ltree` paths and validity dates |
| `00003_layers` | Layers, layer versions (one published at a time) and layer features |
| `00004_detections` | Detections partitioned by UTC month, `ensure_detection_partition()`, and per-workspace detection matches |
| `00005_job_queue` | River's job queue tables, exported from River's own migrations |
| `00006_audit_log` | The append-only audit log |
| `00007_users_and_roles` | User records, roles and grants at current unit paths |
| `00008_ingest_runs` | Feed run history and matched paths grouped by hierarchy |
| `00009_layer_import` | Import jobs, draft units and review problems |
| `00010_alerts` | Alerts, the workspace setting for near-edge review, and routing of existing matches |
| `00011_backfill_runs_and_links` | Run kinds (poll, backfill, import), fetch and observation times on runs, the run that stored each detection, and links between records of the same observation |
| `00012_feed_health_alerts` | Feed-health alerts, at most one open per workspace feed |
| `00014_workflow` | Workflow templates, alert instances, append-only step history and response times |
| `00015_layer_publishing` | Shared detection matching, saved alert routing and layer versions, and replay progress |
| `00016_exports` | File exports and their frozen rows, which only the request transaction can add |
| `00017_notifications` | Notification rules, a replay-safe inbox and the email delivery outbox |

## Commands

```bash
make db-up        # start only the development database
make db-migrate   # apply all migrations
make db-status    # list applied migrations
make db-rollback  # roll back the most recent migration
make db-test      # apply, roll back, reapply, then run db/tests/*.sql
```

Schema tests live in [`db/tests`](../tests). Each file runs in one transaction that is rolled back, and uses only made-up names and shapes.

## Rules

- Never edit a migration that has been merged. Add a new one.
- Detections need a partition for their month before they are inserted. Ingestion calls `ensure_detection_partition()` for each month in a batch.
- When River is upgraded, add a migration with the SQL for the new River versions only, exported with `river migrate-get`.
