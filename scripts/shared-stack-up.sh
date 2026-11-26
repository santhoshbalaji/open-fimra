#!/usr/bin/env bash
set -euo pipefail

# Each caller may have a different checkout. Lock against simultaneous starts
# on this host before checking the container state.
exec 9>"${XDG_RUNTIME_DIR:-/tmp}/open-fimra-shared-$(id -u).lock"
flock 9
services=(db keycloak mailpit ntfy)
running=$("$@" ps --format '{{.Service}} {{.Health}}' --status running "${services[@]}")
for service in "${services[@]}"; do
  if ! grep -Fxq "$service healthy" <<< "$running"; then
    exec "$@" up -d --wait "${services[@]}"
  fi
done
