#!/usr/bin/env bash
# Downloads one release of MapLibre GL JS from the npm registry, checks it
# against its published SHA-512 and copies the browser files into a folder.
# The field app's browser test build serves this copy, so it loads no script
# from an outside service. Usage: fetch-maplibre-js.sh VERSION SHA512 DIR
set -euo pipefail

version=$1
integrity=$2
dir=$3

if [[ -f $dir/VERSION && $(cat "$dir/VERSION") == "$version" ]]; then
  exit 0
fi

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
curl -fsSL -o "$tmp/maplibre.tgz" "https://registry.npmjs.org/maplibre-gl/-/maplibre-gl-$version.tgz"
want=$(base64 -d <<< "$integrity" | od -An -tx1 | tr -d ' \n')
got=$(sha512sum "$tmp/maplibre.tgz" | cut -d' ' -f1)
if [[ $want != "$got" ]]; then
  echo "The MapLibre GL JS $version download does not match its checksum." >&2
  exit 1
fi
tar -xzf "$tmp/maplibre.tgz" -C "$tmp"
install -d "$dir"
for file in maplibre-gl.mjs maplibre-gl-worker.mjs maplibre-gl-shared.mjs maplibre-gl.css; do
  cp "$tmp/package/dist/$file" "$dir/"
done
cp "$tmp/package/LICENSE.txt" "$dir/"
echo "$version" > "$dir/VERSION"
echo "MapLibre GL JS $version is in $dir"
