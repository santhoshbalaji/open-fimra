#!/usr/bin/env bash
# Browser walkthrough of the first-run wizard on a fresh, empty database.
# Run it with `make wizard-walkthrough`, which sets the variables below.
#
# It creates the database WALK_DB beside the development database, starts a
# geo worker on it, serves the built web app on 127.0.0.1:$WEB_PORT (4200 by default) with a
# runtime config for this stack, runs the API test harness with a fake FIRMS
# server, and drives a headless Chromium in the Playwright container. The
# screenshots go to SHOTS, which should be outside the repository.

set -euo pipefail
: "${WALK_DB:?}" "${WALK_DATABASE_URL:?}" "${SHOTS:?}" "${ISSUER:?}" "${API_ADDR:?}"
: "${MAIL_SMTP:?}" "${MAIL_HTTP:?}" "${GOOSE_VERSION:?}" "${PLAYWRIGHT_IMAGE:?}"
read -ra compose <<< "${COMPOSE_CMD:?}"
web_port=${WEB_PORT:-4200}
cd "$(git rev-parse --show-toplevel)"

setup_code=WALK-THRU-CODE-2345
fake_key=0123456789abcdef0123456789abcdef
work=$(mktemp -d)
pids=()
worker=
cleanup() {
  for pid in "${pids[@]}"; do kill "$pid" 2> /dev/null || true; done
  [[ -n $worker ]] && docker stop "$worker" > /dev/null
  rm -rf "$work"
}
trap cleanup EXIT

if ss -ltn | grep -q "127.0.0.1:$web_port "; then
  echo "Port $web_port is in use. Stop the web dev server first." >&2
  exit 1
fi

"${compose[@]}" exec -T db sh -c "dropdb -U \"\$POSTGRES_USER\" --if-exists --force $WALK_DB && createdb -U \"\$POSTGRES_USER\" $WALK_DB"
go run "github.com/pressly/goose/v3/cmd/goose@$GOOSE_VERSION" -dir db/migrations postgres "$WALK_DATABASE_URL" up > /dev/null
worker=$("${compose[@]}" run -d --rm -e PGDATABASE="$WALK_DB" geo-worker)

# The built app with a runtime config for this stack. Nothing in the
# repository changes.
cp -r apps/web/dist/web/browser "$work/web"
printf '{"issuer": "%s", "clientId": "open-fimra-web", "apiBaseUrl": "http://%s"}\n' "$ISSUER" "$API_ADDR" > "$work/web/config.json"
cat > "$work/serve.mjs" << 'EOF'
import { createServer } from 'node:http';
import { createReadStream, existsSync, statSync } from 'node:fs';
import { extname, join, resolve } from 'node:path';
const root = resolve(process.argv[2]);
const types = { '.html': 'text/html', '.js': 'text/javascript', '.mjs': 'text/javascript', '.css': 'text/css',
  '.json': 'application/json', '.svg': 'image/svg+xml', '.png': 'image/png', '.woff2': 'font/woff2',
  '.geojson': 'application/geo+json' };
createServer((request, response) => {
  let file = join(root, decodeURIComponent(new URL(request.url, 'http://host').pathname));
  // Send the app for unknown paths, such as the sign-in callback.
  if (!file.startsWith(root) || !existsSync(file) || statSync(file).isDirectory()) file = join(root, 'index.html');
  response.writeHead(200, { 'Content-Type': types[extname(file)] ?? 'application/octet-stream' });
  createReadStream(file).pipe(response);
}).listen(Number(process.argv[3]), '127.0.0.1');
EOF
node "$work/serve.mjs" "$work/web" "$web_port" &
pids+=($!)

# Build the test binary first, so the process to stop is the server itself.
(cd services/api && go test -c -o "$work/api.test" .)
(
  cd services/api
  FIMRA_WALKTHROUGH=1 FIMRA_DATABASE_URL="$WALK_DATABASE_URL" FIMRA_SETUP_CODE="$setup_code" \
    FIMRA_SMTP_ADDR="$MAIL_SMTP" FIMRA_SMTP_FROM=open-fimra@example.invalid FIMRA_SMTP_SECURITY=local \
    FIMRA_WEB_URL="http://127.0.0.1:$web_port" FIMRA_CORS_ORIGINS="http://127.0.0.1:$web_port" FIMRA_HTTP_ADDR="$API_ADDR" \
    exec "$work/api.test" -test.v -test.run '^TestWizardWalkthroughServer$' -test.timeout 20m > "$work/api.log" 2>&1
) &
pids+=($!)
for _ in $(seq 120); do
  curl -sf "http://$API_ADDR/healthz" > /dev/null && break
  sleep 1
done
curl -sf "http://$API_ADDR/healthz" > /dev/null || { cat "$work/api.log"; exit 1; }

mkdir -p "$SHOTS" "$HOME/.cache/open-fimra-walkthrough"
cp scripts/wizard-walkthrough.mjs "$HOME/.cache/open-fimra-walkthrough/walkthrough.mjs"
if ! docker run --rm --network host --init \
  -v "$HOME/.cache/open-fimra-walkthrough:/work" -v "$PWD/seed/demo-state:/seed:ro" -v "$SHOTS:/shots" \
  -e WEB="http://127.0.0.1:$web_port" -e MAIL="http://$MAIL_HTTP" -e SEED=/seed -e SHOTS=/shots \
  -e SETUP_CODE="$setup_code" -e FIRMS_KEY="$fake_key" -e PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 \
  "$PLAYWRIGHT_IMAGE" bash -c 'cd /work && { [ -d node_modules/playwright ] || npm install --no-save --silent playwright@1.63.0; } && node walkthrough.mjs'
then
  tail -n 40 "$work/api.log"
  exit 1
fi
echo "Screenshots are in $SHOTS"
