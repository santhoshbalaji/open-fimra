#!/usr/bin/env bash
# Field app tests against a fresh copy of the demo data. Run them with
# `make mobile-test-integration` (MODE=test) or `make mobile-walkthrough`
# (MODE=walkthrough), which set the variables below.
#
# Both create the database MOBILE_DB beside the development database, load
# the demo state and detections, add one synthetic forest patch and two
# detections in beat B01 so the demo beat guard has alerts, and start the
# core API on MOBILE_API_ADDR. The development database is left alone.
#
# MODE=test runs the Dart integration tests in the Flutter container. They
# sign in with PKCE through the real open-fimra-mobile client and check what
# each demo user sees and may do.
#
# MODE=walkthrough serves the browser build on 127.0.0.1:MOBILE_WEB_PORT and
# drives it in headless Chromium as the beat guard. Screenshots go to SHOTS,
# which should be outside the repository.

set -euo pipefail
: "${MODE:?}" "${MOBILE_DB:?}" "${MOBILE_DATABASE_URL:?}" "${MOBILE_API_ADDR:?}" "${MOBILE_WEB_PORT:?}"
: "${FIMRA_OIDC_ISSUER:?}" "${FIMRA_OIDC_AUDIENCE:?}" "${GOOSE_VERSION:?}" "${FLUTTER_IMAGE:?}"
read -ra compose <<< "${COMPOSE_CMD:?}"
read -ra docker_flags <<< "${MOBILE_DOCKER_FLAGS:-}"
cd "$(git rev-parse --show-toplevel)"

work=$(mktemp -d)
pids=()
cleanup() {
  for pid in "${pids[@]}"; do kill "$pid" 2> /dev/null || true; done
  rm -rf "$work"
}
trap cleanup EXIT

psql_test() {
  "${compose[@]}" exec -T db sh -c "psql -X -q -v ON_ERROR_STOP=1 -U \"\$POSTGRES_USER\" -d $MOBILE_DB"
}

echo "Migrating the test database $MOBILE_DB"
go run "github.com/pressly/goose/v3/cmd/goose@$GOOSE_VERSION" -dir db/migrations postgres "$MOBILE_DATABASE_URL" up > /dev/null

(cd services/api && go build -o "$work/api" .)
ingest() {
  (cd services/api && FIMRA_DATABASE_URL="$MOBILE_DATABASE_URL" "$work/api" ingest-firms -demo -file "../../$1")
}
ingest packages/connectors/firms/fixtures/demo.csv
psql_test < apps/mobile/test_integration/fixtures/beat-guard.sql
ingest apps/mobile/test_integration/fixtures/beat-guard.csv

web_origin="http://127.0.0.1:$MOBILE_WEB_PORT"
(
  cd services/api
  FIMRA_DATABASE_URL="$MOBILE_DATABASE_URL" FIMRA_HTTP_ADDR="$MOBILE_API_ADDR" \
    FIMRA_OIDC_ISSUER="$FIMRA_OIDC_ISSUER" FIMRA_OIDC_AUDIENCE="$FIMRA_OIDC_AUDIENCE" \
    FIMRA_CORS_ORIGINS="$web_origin,http://localhost:$MOBILE_WEB_PORT" FIMRA_FIRMS_MAP_KEY='' FIMRA_SMTP_ADDR='' \
    FIMRA_UPLOAD_DIR="$work/uploads" FIMRA_EXPORT_DIR="$work/exports" \
    exec "$work/api" > "$work/api.log" 2>&1
) &
pids+=($!)
for _ in $(seq 60); do
  curl -sf "http://$MOBILE_API_ADDR/healthz" > /dev/null && break
  sleep 1
done
curl -sf "http://$MOBILE_API_ADDR/healthz" > /dev/null || { cat "$work/api.log"; exit 1; }

flutter_run() {
  docker run --rm --init --network host --memory "${MOBILE_MEMORY:-4g}" --cpus "${MOBILE_CPUS:-2}" \
    -v "$PWD:/repo" -w /repo/apps/mobile -e PUB_CACHE=/repo/.mobile-cache/pub \
    -e FIMRA_TEST_API="http://$MOBILE_API_ADDR" -e FIMRA_TEST_ISSUER="$FIMRA_OIDC_ISSUER" \
    "${docker_flags[@]}" "$FLUTTER_IMAGE" "$@"
}

case $MODE in
  test)
    if ! flutter_run flutter --suppress-analytics test --reporter expanded test_integration; then
      tail -n 40 "$work/api.log"
      exit 1
    fi
    ;;
  walkthrough)
    : "${SHOTS:?}" "${PLAYWRIGHT_IMAGE:?}"
    if ss -ltn | grep -q "127.0.0.1:$MOBILE_WEB_PORT "; then
      echo "Port $MOBILE_WEB_PORT is in use. Choose another with MOBILE_WEB_PORT (4300 to 4309)." >&2
      exit 1
    fi
    python3 -I scripts/serve-static.py apps/mobile/build/web "$MOBILE_WEB_PORT" > "$work/web.log" 2>&1 &
    pids+=($!)
    mkdir -p "$SHOTS" "$work/browser"
    cp scripts/mobile-walkthrough.mjs "$work/browser/mobile-walkthrough.mjs"
    if ! docker run --rm --network host --init --memory "${MOBILE_MEMORY:-4g}" --cpus "${MOBILE_CPUS:-2}" \
      -v "$work/browser:/work" -v "$SHOTS:/shots" \
      -e WEB="$web_origin" -e API="http://$MOBILE_API_ADDR" -e ISSUER="$FIMRA_OIDC_ISSUER" -e SHOTS=/shots \
      -e PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 "$PLAYWRIGHT_IMAGE" \
      bash -c 'cd /work && { [ -d node_modules/playwright ] || npm install --no-save --silent playwright@1.63.0; } && node mobile-walkthrough.mjs'
    then
      tail -n 40 "$work/api.log"
      exit 1
    fi
    echo "Screenshots are in $SHOTS"
    ;;
  *)
    echo "MODE must be test or walkthrough." >&2
    exit 2
    ;;
esac
