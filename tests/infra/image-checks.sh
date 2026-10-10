#!/usr/bin/env bash
set -euo pipefail

factory_dir="$(cd "$(dirname "$0")/../.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT

cat > "$test_dir/docker" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
if [[ "$1" == compose ]]; then
  jq -n --arg mode "$CASE" '{services: (["frontend", "gateway", "serving", "warehouse"] | map({key: ., value: {image: ("ghcr.io/justikra/umir-" + . + ":sha-" + ("a" * 40))}}) | from_entries)} | if $mode == "wrong-image" then .services.gateway.image = .services.warehouse.image elif $mode == "extra-service" then .services.extra = {} else . end'
elif [[ "$1 $2" == 'image inspect' ]]; then
  jq -n --arg mode "$CASE" --arg image "$3" '[{Config: {Labels: {"org.opencontainers.image.source": ("https://github.com/JustIkra/" + ($image | split("/")[-1] | split(":")[0])), "org.opencontainers.image.revision": ("a" * 40)}, Healthcheck: {Test: ["CMD", "/server", "healthcheck"]}}}] | if $mode == "missing-healthcheck" then del(.[0].Config.Healthcheck) elif $mode == "disabled-healthcheck" then .[0].Config.Healthcheck.Test = ["NONE"] elif $mode == "wrong-revision" then .[0].Config.Labels["org.opencontainers.image.revision"] = ("b" * 40) elif $mode == "wrong-source" then .[0].Config.Labels["org.opencontainers.image.source"] = "https://github.com/Other/service" else . end'
else
  exit 1
fi
SH

cat > "$test_dir/gh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
endpoint=""
for argument in "$@"; do
  [[ "$argument" != repos/* ]] || endpoint="$argument"
done
case "$endpoint" in
  */commits/*)
    [[ "$CASE" != missing-commit ]] || exit 1
    jq -n --arg mode "$CASE" '{sha: (if $mode == "wrong-commit" then "b" else "a" end | . * 40)}'
    ;;
  */actions/workflows/ci.yml/runs\?*)
    jq -n --arg mode "$CASE" '{workflow_runs: [{id: 12, head_sha: ("a" * 40), head_branch: "main", path: ".github/workflows/ci.yml", event: "push", status: "completed", conclusion: "success"}]} | if $mode == "failed-ci" then .workflow_runs[0].conclusion = "failure" elif $mode == "running-ci" then .workflow_runs[0].status = "in_progress" elif $mode == "pr-only" then .workflow_runs[0].event = "pull_request" elif $mode == "wrong-workflow" then .workflow_runs[0].path = ".github/workflows/docs.yml" elif $mode == "wrong-head" then .workflow_runs[0].head_sha = ("b" * 40) elif $mode == "wrong-branch" then .workflow_runs[0].head_branch = "feature" elif $mode == "no-ci" then .workflow_runs = [] elif $mode == "manual" then .workflow_runs[0].event = "workflow_dispatch" else . end'
    ;;
  */actions/runs/12/jobs\?*)
    jq -n --arg mode "$CASE" '{jobs: [{name: "Check", conclusion: "success"}, {name: "Publish image", conclusion: "success"}]} | if $mode == "skipped-publish" then .jobs[1].conclusion = "skipped" elif $mode == "skipped-check" then .jobs[0].conclusion = "skipped" elif $mode == "paginated-jobs" then {jobs: [.jobs[0]]}, {jobs: [.jobs[1]]} else . end'
    ;;
  *) exit 1 ;;
esac
SH

chmod +x "$test_dir/docker" "$test_dir/gh"
export PATH="$test_dir:$PATH"
cd "$factory_dir"

assert_exit() {
  local script="$1" mode="$2" expected="$3" actual=0
  CASE="$mode" bash "scripts/$script" --env-file .env.example > "$test_dir/output" 2>&1 || actual=$?
  if [[ "$expected" == success && "$actual" != 0 ]] || [[ "$expected" == failure && "$actual" == 0 ]]; then
    cat "$test_dir/output" >&2
    printf 'FAIL: %s %s (exit %s)\n' "$script" "$mode" "$actual" >&2
    exit 1
  fi
  printf 'PASS: %s %s\n' "$script" "$mode"
}

for mode in valid manual paginated-jobs; do
  assert_exit check-image-sources.sh "$mode" success
done
for mode in wrong-image extra-service missing-commit wrong-commit failed-ci running-ci pr-only wrong-workflow wrong-head wrong-branch no-ci skipped-publish skipped-check; do
  assert_exit check-image-sources.sh "$mode" failure
done
assert_exit check-pulled-images.sh valid success
for mode in wrong-image extra-service missing-healthcheck disabled-healthcheck wrong-revision wrong-source; do
  assert_exit check-pulled-images.sh "$mode" failure
done
