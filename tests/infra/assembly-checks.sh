#!/usr/bin/env bash
set -Eeuo pipefail

factory_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
test_dir="$(mktemp -d "${TMPDIR:-/tmp}/umir-assembly-checks-test.XXXXXX")"
trap 'rm -rf -- "$test_dir"' EXIT

mkdir -p "$test_dir/bin"
env_file="$test_dir/candidate env.env"
touch "$env_file"
env_file="$(cd "$(dirname "$env_file")" && pwd)/$(basename "$env_file")"

cat > "$test_dir/bin/docker" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

{
  printf 'project=%s bind=%s web=%s api=%s args=' "$COMPOSE_PROJECT_NAME" "$UMIR_BIND" "$UMIR_WEB_PORT" "$UMIR_API_PORT"
  printf '<%s>' "$@"
  printf '\n'
} >> "$TRACE"

if [[ "$1" != compose ]]; then
  if [[ "$1" == image && "$2" == inspect ]]; then
    image="$3"
    revision="${image##*:sha-}"
    if [[ "$CASE" == metadata-failure ]]; then revision="$(printf '%040d' 0)"; fi
    repo="${image##*/}"
    repo="${repo%%:*}"
    source="https://github.com/justikra/$repo"
    jq -n --arg source "$source" --arg revision "$revision" \
      '[{Config: {Labels: {"org.opencontainers.image.source": $source, "org.opencontainers.image.revision": $revision}, Healthcheck: {Test: ["CMD", "/server", "healthcheck"]}}}]'
    exit 0
  fi
  exit 1
fi
shift

while [[ $# -gt 0 ]]; do
  case "$1" in
    -f|--env-file) shift 2 ;;
    *) action="$1"; shift; break ;;
  esac
done

case "$action" in
  config)
    if [[ " $* " == *" --format json "* ]]; then
      jq -n '{services: {
        frontend: {image: ("ghcr.io/justikra/umir-frontend:sha-" + ("a" * 40))},
        gateway: {image: ("ghcr.io/justikra/umir-gateway:sha-" + ("b" * 40))},
        serving: {image: ("ghcr.io/justikra/umir-serving:sha-" + ("c" * 40))},
        warehouse: {image: ("ghcr.io/justikra/umir-warehouse:sha-" + ("d" * 40))}
      }}'
    fi
    ;;
  pull) ;;
  up)
    if [[ "$CASE" == partial-up ]]; then exit 19; fi
    ;;
  port)
    case "$1" in
      frontend) printf '127.0.0.1:32768\n' ;;
      gateway) printf '127.0.0.1:32769\n' ;;
      *) exit 1 ;;
    esac
    ;;
  logs) printf 'candidate logs for %s\n' "$COMPOSE_PROJECT_NAME" ;;
  down)
    if [[ "$CASE" == cleanup-failure || "$CASE" == primary-and-cleanup-failure ]]; then exit 31; fi
    ;;
  *) exit 1 ;;
esac
SH

cat > "$test_dir/bin/gh" <<'SH'
#!/usr/bin/env bash
set -euo pipefail

endpoint=""
for argument in "$@"; do
  [[ "$argument" != repos/* ]] || endpoint="$argument"
done
case "$endpoint" in
  */commits/*)
    sha="${endpoint##*/}"
    jq -n --arg sha "$sha" '{sha: $sha}'
    if [[ "$CASE" == provenance-failure ]]; then exit 22; fi
    ;;
  */actions/workflows/ci.yml/runs\?*)
    sha="${endpoint#*head_sha=}"
    sha="${sha%%&*}"
    jq -n --arg sha "$sha" \
      '{workflow_runs: [{id: 17, head_sha: $sha, head_branch: "main", path: ".github/workflows/ci.yml", event: "push", status: "completed", conclusion: "success"}]}'
    ;;
  */actions/runs/17/jobs\?*)
    jq -n '{jobs: [{name: "Check", conclusion: "success"}, {name: "Publish image", conclusion: "success"}]}'
    ;;
  *) exit 1 ;;
esac
SH

cat > "$test_dir/bin/umir-checks" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
{
  printf 'project=%s bind=%s web=%s api=%s args=' "$COMPOSE_PROJECT_NAME" "$UMIR_BIND" "$UMIR_WEB_PORT" "$UMIR_API_PORT"
  printf '<%s>' "$@"
  printf '\n'
} >> "$TRACE"
if [[ "$1" == smoke && ( "$CASE" == smoke-failure || "$CASE" == primary-and-cleanup-failure ) ]]; then exit 28; fi
[[ "$1" == smoke ]]
SH

cat > "$test_dir/bin/go" <<'SH'
#!/usr/bin/env bash
set -euo pipefail
{
  printf 'GOWORK=%s args=' "${GOWORK:-}"
  printf '<%s>' "$@"
  printf '\n'
} >> "$TRACE"
[[ "$GOWORK" == off && "$1" == -C && "$3" == build ]]
output=""
while [[ $# -gt 0 ]]; do
  if [[ "$1" == -o ]]; then output="$2"; shift 2; else shift; fi
done
[[ -n "$output" ]]
cat > "$output" <<'BIN'
#!/usr/bin/env bash
set -euo pipefail
[[ "$1" == smoke ]]
if [[ "$CASE" == smoke-failure || "$CASE" == primary-and-cleanup-failure ]]; then exit 28; fi
BIN
chmod +x "$output"
SH

chmod +x "$test_dir/bin/docker" "$test_dir/bin/gh" "$test_dir/bin/umir-checks" "$test_dir/bin/go"

assert_contains() {
  local file="$1" expected="$2"
  if ! grep -F -- "$expected" "$file" >/dev/null; then
    cat "$file" >&2
    printf 'Expected output to contain: %s\n' "$expected" >&2
    exit 1
  fi
}

assert_not_contains() {
  local file="$1" unexpected="$2"
  if grep -F -- "$unexpected" "$file" >/dev/null; then
    cat "$file" >&2
    printf 'Expected output not to contain: %s\n' "$unexpected" >&2
    exit 1
  fi
}

run_case() {
  local name="$1" expected_status="$2" use_default_build="${3:-false}" status=0
  local case_dir="$test_dir/$name"
  mkdir -p "$case_dir"
  : > "$case_dir/trace"
  if [[ "$use_default_build" == true ]]; then
    PATH="$test_dir/bin:$PATH" \
      CASE="$name" TRACE="$case_dir/trace" TMPDIR="$case_dir" \
      UMIR_CHECKS_BIN= \
      bash "$factory_dir/scripts/verify-assembly.sh" --env-file "$env_file" > "$case_dir/output" 2>&1 || status=$?
  else
    PATH="$test_dir/bin:$PATH" \
      CASE="$name" TRACE="$case_dir/trace" TMPDIR="$case_dir" \
      UMIR_CHECKS_BIN="$test_dir/bin/umir-checks" \
      bash "$factory_dir/scripts/verify-assembly.sh" --env-file "$env_file" > "$case_dir/output" 2>&1 || status=$?
  fi
  if [[ "$status" != "$expected_status" ]]; then
    cat "$case_dir/output" >&2
    printf 'Expected %s exit %s, got %s\n' "$name" "$expected_status" "$status" >&2
    exit 1
  fi
  printf '%s\n' "$case_dir"
}

success_dir="$(run_case success 0)"
project="$(sed -n 's/^project=\([^ ]*\).*/\1/p' "$success_dir/trace" | head -n 1)"
[[ "$project" =~ ^umir-check-[0-9]+-[0-9]+$ ]]
while IFS= read -r line; do
  [[ "$line" == *"project=$project bind=127.0.0.1 web=0 api=0"* ]]
  if [[ "$line" == *"<image><inspect>"* ]]; then continue; fi
  [[ "$line" == *"<compose><-f><$factory_dir/compose.yaml><--env-file><$env_file>"* ]] || [[ "$line" == *"<smoke>"* ]]
done < "$success_dir/trace"
assert_contains "$success_dir/trace" '<up><-d><--wait><--wait-timeout><120>'
assert_contains "$success_dir/trace" '<down><--remove-orphans>'
assert_contains "$success_dir/trace" "<smoke><--factory-dir><$factory_dir><--env-file><$env_file><--web-url><http://127.0.0.1:32768><--api-url><http://127.0.0.1:32769>"
assert_not_contains "$success_dir/trace" '<-v>'
assert_not_contains "$success_dir/trace" '<logs>'
printf 'PASS: success isolates Compose project and ephemeral loopback ports\n'

default_build_dir="$(run_case default-build 0 true)"
assert_contains "$default_build_dir/trace" "GOWORK=off args=<-C><$factory_dir/tools/assembly-checks><build>"
assert_contains "$default_build_dir/trace" '<down><--remove-orphans>'
[[ -z "$(find "$default_build_dir" -maxdepth 1 -type d -name 'umir-checks.*' -print -quit)" ]]
printf 'PASS: local default builds umir-checks with GOWORK=off in its nested module\n'

provenance_dir="$(run_case provenance-failure 22)"
assert_not_contains "$provenance_dir/trace" '<pull>'
assert_not_contains "$provenance_dir/trace" '<up>'
printf 'PASS: provenance failure prevents pull and startup\n'

metadata_dir="$(run_case metadata-failure 1)"
assert_contains "$metadata_dir/trace" '<pull>'
assert_not_contains "$metadata_dir/trace" '<up>'
printf 'PASS: pulled-image metadata failure prevents startup\n'

partial_dir="$(run_case partial-up 19)"
assert_contains "$partial_dir/trace" '<logs><--no-color>'
assert_contains "$partial_dir/trace" '<down><--remove-orphans>'
assert_contains "$partial_dir/output" 'candidate logs'
printf 'PASS: partial startup failure logs and cleans the candidate project\n'

smoke_dir="$(run_case smoke-failure 28)"
assert_contains "$smoke_dir/trace" '<logs><--no-color>'
assert_contains "$smoke_dir/trace" '<down><--remove-orphans>'
assert_contains "$smoke_dir/output" 'candidate logs'
printf 'PASS: smoke failure logs and cleans the candidate project\n'

cleanup_dir="$(run_case cleanup-failure 31)"
assert_contains "$cleanup_dir/trace" '<down><--remove-orphans>'
assert_contains "$cleanup_dir/trace" '<logs><--no-color>'
assert_contains "$cleanup_dir/output" 'Failed to stop candidate Compose project'
printf 'PASS: cleanup failure fails an otherwise successful verification\n'

primary_cleanup_dir="$(run_case primary-and-cleanup-failure 28)"
assert_contains "$primary_cleanup_dir/trace" '<down><--remove-orphans>'
assert_contains "$primary_cleanup_dir/output" 'candidate logs'
printf 'PASS: cleanup failure preserves the original smoke failure status\n'

unknown_dir="$test_dir/unknown-arguments"
mkdir -p "$unknown_dir"
: > "$unknown_dir/trace"
status=0
PATH="$test_dir/bin:$PATH" TRACE="$unknown_dir/trace" \
  UMIR_CHECKS_BIN="$test_dir/bin/umir-checks" \
  bash "$factory_dir/scripts/verify-assembly.sh" --bogus > "$unknown_dir/output" 2>&1 || status=$?
[[ "$status" == 2 && ! -s "$unknown_dir/trace" ]]
assert_contains "$unknown_dir/output" 'Unknown argument: --bogus'
printf 'PASS: unknown arguments are rejected before runtime commands\n'

help_dir="$test_dir/help"
mkdir -p "$help_dir"
: > "$help_dir/trace"
PATH="$test_dir/bin:$PATH" TRACE="$help_dir/trace" \
  UMIR_CHECKS_BIN="$test_dir/bin/umir-checks" \
  bash "$factory_dir/scripts/verify-assembly.sh" --help > "$help_dir/output" 2>&1
[[ ! -s "$help_dir/trace" ]]
assert_contains "$help_dir/output" 'Usage:'
printf 'PASS: help exits before runtime commands\n'
