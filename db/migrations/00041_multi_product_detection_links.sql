-- An observation can have both a FIRMS NRT and a FIRMS SP record as well as
-- a NOAA record. All later arrivals point directly to the first record.
-- +goose Up
CREATE INDEX detection_links_primary_idx ON detection_links (primary_id, primary_observed_at);
ALTER TABLE detection_links DROP CONSTRAINT detection_links_primary_id_primary_observed_at_key;

-- +goose Down
-- The earlier schema can retain only the first link per primary.
DELETE FROM detection_links l USING detection_links earlier
WHERE (l.primary_id, l.primary_observed_at) = (earlier.primary_id, earlier.primary_observed_at)
  AND (l.linked_at, l.detection_id, l.observed_at) >
      (earlier.linked_at, earlier.detection_id, earlier.observed_at);
ALTER TABLE detection_links ADD UNIQUE (primary_id, primary_observed_at);
DROP INDEX detection_links_primary_idx;
