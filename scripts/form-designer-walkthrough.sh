#!/usr/bin/env bash
# Browser walkthrough of the form designer. Run it with
# `make form-designer-walkthrough`, which sets the variables below.
#
# It creates the database WALK_DB beside the development database, serves the
# built web app on 127.0.0.1:$WEB_PORT (4200 by default) with a runtime config
# for this stack, runs the API test harness, which adds a synthetic national
# template and one report filled in on it, and drives a headless Chromium in
# the Playwright container. The screenshots go to SHOTS, which should be
# outside the repository.

set -euo pipefail
: "${WALK_DB:?}" "${WALK_DATABASE_URL:?}" "${SHOTS:?}" "${ISSUER:?}" "${API_ADDR:?}" "${GOOSE_VERSION:?}" "${PLAYWRIGHT_IMAGE:?}"
read -ra compose <<< "${COMPOSE_CMD:?}"
web_port=${WEB_PORT:-4200}
cd "$(git rev-parse --show-toplevel)"

work=$(mktemp -d)
pids=()
cleanup() {
  for pid in "${pids[@]}"; do kill "$pid" 2> /dev/null || true; done
  rm -rf "$work"
}
trap cleanup EXIT

if ss -ltn | grep -q "127.0.0.1:$web_port "; then
  echo "Port $web_port is in use. Stop the web dev server first, or set WEB_PORT." >&2
  exit 1
fi

"${compose[@]}" exec -T db sh -c "dropdb -U \"\$POSTGRES_USER\" --if-exists --force $WALK_DB && createdb -U \"\$POSTGRES_USER\" $WALK_DB"
go run "github.com/pressly/goose/v3/cmd/goose@$GOOSE_VERSION" -dir db/migrations postgres "$WALK_DATABASE_URL" up > /dev/null

# The built app with a runtime config for this stack. Nothing in the
# repository changes.
cp -r apps/web/dist/web/browser "$work/web"
printf '{"issuer": "%s", "clientId": "open-fimra-web", "apiBaseUrl": "http://%s"}\n' "$ISSUER" "$API_ADDR" > "$work/web/config.json"
cat > "$work/serve.mjs" << 'SERVE'
import { createServer } from 'node:http';
import { createReadStream, existsSync, statSync } from 'node:fs';
import { extname, join, resolve } from 'node:path';
const root = resolve(process.argv[2]);
const types = { '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css', '.json': 'application/json',
  '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2' };
createServer((request, response) => {
  let file = join(root, decodeURIComponent(new URL(request.url, 'http://host').pathname));
  if (!file.startsWith(root) || !existsSync(file) || statSync(file).isDirectory()) file = join(root, 'index.html');
  response.writeHead(200, { 'Content-Type': types[extname(file)] ?? 'application/octet-stream' });
  createReadStream(file).pipe(response);
}).listen(Number(process.argv[3]), '127.0.0.1');
SERVE
node "$work/serve.mjs" "$work/web" "$web_port" &
pids+=($!)

(cd services/api && go test -c -o "$work/api.test" .)
(
  cd services/api
  FIMRA_FORMS_WALKTHROUGH=1 FIMRA_DATABASE_URL="$WALK_DATABASE_URL" FIMRA_HTTP_ADDR="$API_ADDR" \
    FIMRA_CORS_ORIGINS="http://127.0.0.1:$web_port" \
    exec "$work/api.test" -test.v -test.run '^TestFormDesignerWalkthroughServer$' -test.timeout 15m > "$work/api.log" 2>&1
) &
pids+=($!)
for _ in $(seq 120); do
  curl -sf "http://$API_ADDR/healthz" > /dev/null && break
  sleep 1
done
curl -sf "http://$API_ADDR/healthz" > /dev/null || { cat "$work/api.log"; exit 1; }

mkdir -p "$SHOTS" "$work/browser"
cp scripts/form-designer-walkthrough.mjs "$work/browser/form-designer.mjs"
axe_version=$(node -p 'require("./apps/web/package.json").devDependencies["@axe-core/playwright"]')
if ! docker run --rm --network host --init \
  -v "$work/browser:/work" -v "$SHOTS:/shots" \
  -e WEB="http://127.0.0.1:$web_port" -e API="http://$API_ADDR" -e ISSUER="$ISSUER" -e SHOTS=/shots \
  -e PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 -e AXE_VERSION="$axe_version" \
  "$PLAYWRIGHT_IMAGE" bash -c 'cd /work && npm install --no-save --silent playwright@1.63.0 "@axe-core/playwright@$AXE_VERSION" && node form-designer.mjs'
then
  tail -n 40 "$work/api.log"
  exit 1
fi
echo "Screenshots are in $SHOTS"
