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
  repo="JustIkra/umir-$service"
  gh api "repos/$repo/commits/$sha" |
    jq -e --arg sha "$sha" '.sha == $sha' > /dev/null
  run_ids="$(gh api --paginate "repos/$repo/actions/workflows/ci.yml/runs?head_sha=$sha&branch=main&status=success&per_page=100" |
    jq -r --arg sha "$sha" '.workflow_runs[] | select(
      .head_sha == $sha and .head_branch == "main" and
      .path == ".github/workflows/ci.yml" and
      (.event == "push" or .event == "workflow_dispatch") and
      .status == "completed" and .conclusion == "success"
    ) | .id')"
  verified=false
  while IFS= read -r run_id; do
    [[ -n "$run_id" ]] || continue
    jobs="$(gh api --paginate "repos/$repo/actions/runs/$run_id/jobs?per_page=100")"
    if jq -se '[.[].jobs[] | select(.conclusion == "success") | .name] |
      index("Check") != null and index("Publish image") != null' <<< "$jobs" > /dev/null; then
      printf 'OK: %s@%s https://github.com/%s/actions/runs/%s\n' "$repo" "$sha" "$repo" "$run_id"
      verified=true
      break
    fi
  done <<< "$run_ids"
  if [[ "$verified" != true ]]; then
    printf 'No successful main CI/CD check and image publication for %s@%s\n' "$repo" "$sha" >&2
    exit 1
  fi
done
