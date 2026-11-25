-- PostGIS for geometry, and ltree for hierarchy paths such as state.circle.division.range.beat.

-- +goose Up
CREATE EXTENSION IF NOT EXISTS postgis;
CREATE EXTENSION IF NOT EXISTS ltree;

-- +goose Down
-- PostGIS stays: database images and administrators often install it, along with
-- extensions that depend on it, before any migration runs.
DROP EXTENSION IF EXISTS ltree;
