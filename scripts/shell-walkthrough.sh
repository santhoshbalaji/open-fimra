#!/usr/bin/env bash
# Check the built shell against a seeded development stack.
set -euo pipefail
: "${SHOTS:?}" "${ISSUER:?}" "${API_ADDR:?}" "${FIMRA_DATABASE_URL:?}"
web_port=${WEB_PORT:-4200}
cd "$(git rev-parse --show-toplevel)"
work=$(mktemp -d)
pids=()
cleanup() {
  for pid in "${pids[@]}"; do kill "$pid" 2>/dev/null || true; done
  rm -rf "$work"
}
trap cleanup EXIT
shots=$(realpath -m "$SHOTS")
case "$shots/" in "$PWD/"*) echo 'Choose a screenshot directory outside the checkout.' >&2; exit 1;; esac
mkdir -p "$shots"
if ss -ltn | grep -q "127.0.0.1:$web_port "; then
  echo "Port $web_port is in use." >&2
  exit 1
fi
cp -r apps/web/dist/web/browser "$work/web"
printf '{"issuer":"%s","clientId":"open-fimra-web","apiBaseUrl":"http://%s"}\n' "$ISSUER" "$API_ADDR" > "$work/web/config.json"
python3 -I scripts/serve-static.py "$work/web" "$web_port" --spa > "$work/web.log" 2>&1 &
pids+=($!)
(cd services/api && go build -o "$work/api" .)
(
  cd services/api
  FIMRA_OIDC_ISSUER="$ISSUER" FIMRA_OIDC_AUDIENCE=open-fimra-api FIMRA_HTTP_ADDR="$API_ADDR" \
    FIMRA_CORS_ORIGINS="http://127.0.0.1:$web_port" exec "$work/api" > "$work/api.log" 2>&1
) &
pids+=($!)
for _ in $(seq 60); do
  curl -sf "http://$API_ADDR/healthz" >/dev/null && break
  sleep 1
done
curl -sf "http://$API_ADDR/healthz" >/dev/null || { cat "$work/api.log"; exit 1; }
mkdir "$work/test"
cp scripts/shell-walkthrough.mjs "$work/test/"
axe_version=$(node -p 'require("./apps/web/package.json").devDependencies["@axe-core/playwright"]')
docker run --rm --network host --init \
  -v "$work/test:/work" -v "$shots:/shots" -w /work \
  -e WEB="http://127.0.0.1:$web_port" -e API="http://$API_ADDR" -e SHOTS=/shots \
  -e PLAYWRIGHT_SKIP_BROWSER_DOWNLOAD=1 -e AXE_VERSION="$axe_version" \
  mcr.microsoft.com/playwright:v1.63.0-noble bash -c \
    'npm install --no-save --silent playwright@1.63.0 "@axe-core/playwright@$AXE_VERSION" && node shell-walkthrough.mjs'
