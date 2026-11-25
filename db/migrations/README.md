# Database migrations

Plain SQL migrations for PostgreSQL with PostGIS, run with [goose](https://github.com/pressly/goose). Each file has an `Up` and a `Down` section, and every migration must roll back cleanly.

The first migrations arrive in slice 2 of the build.
