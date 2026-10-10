#!/usr/bin/env bash
set -euo pipefail

config="$(docker compose "$@" config --format json)"
jq -e '
  (.services | keys == ["frontend", "gateway", "serving", "warehouse"]) and
  all(.services | to_entries[];
    .key as $service | (.value.image // "") |
    test("^ghcr\\.io/justikra/umir-" + $service + ":sha-[0-9a-f]{40}$"))
' <<< "$config" > /dev/null

for service in frontend gateway serving warehouse; do
  image="$(jq -r --arg service "$service" '.services[$service].image' <<< "$config")"
  sha="${image##*:sha-}"
  source="https://github.com/justikra/umir-$service"
  if ! docker image inspect "$image" | jq -e --arg sha "$sha" --arg source "$source" '
    .[0].Config |
    .Labels["org.opencontainers.image.revision"] == $sha and
    ((.Labels["org.opencontainers.image.source"] // "" | ascii_downcase) == $source) and
    (.Healthcheck.Test | length > 1 and (.[0] == "CMD" or .[0] == "CMD-SHELL"))
  ' > /dev/null; then
    printf 'Invalid source, revision or HEALTHCHECK in %s\n' "$image" >&2
    exit 1
  fi
  printf 'OK: source, revision and HEALTHCHECK in %s\n' "$image"
done
