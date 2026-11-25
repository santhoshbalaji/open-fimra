#!/usr/bin/env bash
# Repository guards. These protect the rules in PLAN.md section 12:
# no real agency data, no secrets, and no large binaries in the repository.
# Run with `make check`. Continuous integration runs the same command.

set -euo pipefail
cd "$(git rev-parse --show-toplevel)"

max_bytes=$((2 * 1024 * 1024))
failed=0

fail() {
  echo "error: $1" >&2
  failed=1
}

# Geospatial data files may live only in synthetic data or test fixture folders.
geo_ext='\.(gpkg|shp|shx|dbf|prj|cpg|kml|kmz|geojson|fgb|gml|tif|tiff|pmtiles|mbtiles)$'
allowed_dirs='^(seed/|.*/(testdata|fixtures)/)'

while IFS= read -r -d '' file; do
  [[ -e "$file" ]] || continue
  lower=${file,,}

  if [[ $lower =~ $geo_ext && ! $file =~ $allowed_dirs ]]; then
    fail "$file: geospatial data belongs in seed/ or a testdata/ or fixtures/ folder, and must be synthetic"
  fi

  base=${file##*/}
  if [[ $base == .env || ( $base == .env.* && $base != .env.example ) ]]; then
    fail "$file: environment files hold secrets and must not be committed"
  fi

  size=$(wc -c < "$file")
  if (( size > max_bytes )); then
    fail "$file: $((size / 1024)) KiB is over the 2 MiB limit for committed files"
  fi

  if grep -Iq -e '-----BEGIN [A-Z ]*PRIVATE KEY-----' -- "$file"; then
    fail "$file: contains a private key"
  fi
done < <(git ls-files -z --cached --others --exclude-standard)

if (( failed )); then
  exit 1
fi
echo "Repository checks passed."
