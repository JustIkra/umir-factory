#!/usr/bin/env bash
set -Eeuo pipefail

factory_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$factory_dir"
compose_file="$factory_dir/compose.yaml"
env_file="$factory_dir/.env.example"
checks_bin=""
build_dir=""
cleanup_armed=0

usage() {
  cat <<'EOF'
Usage: bash scripts/verify-assembly.sh [--env-file PATH]
       bash scripts/verify-assembly.sh --help

Verify the pinned candidate assembly in an isolated Compose project.
EOF
}

if [[ $# -eq 1 && "$1" == --help ]]; then
  usage
  exit 0
fi

while [[ $# -gt 0 ]]; do
  case "$1" in
    --env-file)
      if [[ $# -lt 2 || -n "${env_file_seen:-}" ]]; then
        printf 'Expected at most one --env-file PATH\n' >&2
        exit 2
      fi
      env_file_seen=1
      env_file="$2"
      shift 2
      ;;
    *)
      printf 'Unknown argument: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ "$env_file" != /* ]]; then
  env_file="$factory_dir/$env_file"
fi
if [[ ! -f "$env_file" ]]; then
  printf 'Environment file does not exist: %s\n' "$env_file" >&2
  exit 2
fi
env_file="$(cd "$(dirname "$env_file")" && pwd)/$(basename "$env_file")"

for command_name in docker gh jq; do
  if ! command -v "$command_name" >/dev/null 2>&1; then
    printf 'Required command is missing: %s\n' "$command_name" >&2
    exit 127
  fi
done

export COMPOSE_FILE="$compose_file"
export COMPOSE_PROJECT_NAME="umir-check-$$-${RANDOM}${RANDOM}"
export UMIR_BIND=127.0.0.1
export UMIR_WEB_PORT=0
export UMIR_API_PORT=0

compose() {
  docker compose -f "$compose_file" --env-file "$env_file" "$@"
}

cleanup() {
  local status=$? cleanup_status=0 remove_status=0
  trap - EXIT

  if (( cleanup_armed )); then
    if (( status != 0 )); then
      compose logs --no-color || true
    fi
    if compose down --remove-orphans; then
      :
    else
      cleanup_status=$?
      printf 'Failed to stop candidate Compose project %s\n' "$COMPOSE_PROJECT_NAME" >&2
      if (( status == 0 )); then
        status=$cleanup_status
        compose logs --no-color || true
      fi
    fi
  fi

  if [[ -n "$build_dir" ]]; then
    rm -rf -- "$build_dir" || remove_status=$?
    if (( remove_status != 0 )); then
      printf 'Failed to remove owned temporary build directory\n' >&2
      if (( status == 0 )); then status=$remove_status; fi
    fi
  fi
  exit "$status"
}
trap cleanup EXIT

if [[ -n "${UMIR_CHECKS_BIN:-}" ]]; then
  if [[ "$UMIR_CHECKS_BIN" != /* || ! -f "$UMIR_CHECKS_BIN" || ! -x "$UMIR_CHECKS_BIN" ]]; then
    printf 'UMIR_CHECKS_BIN must be an absolute path to an executable file\n' >&2
    exit 2
  fi
  checks_bin="$UMIR_CHECKS_BIN"
else
  build_dir="$(mktemp -d "${TMPDIR:-/tmp}/umir-checks.XXXXXX")"
  checks_bin="$build_dir/umir-checks"
  if ! GOWORK=off go -C "$factory_dir/tools/assembly-checks" build -o "$checks_bin" .; then
    printf 'Failed to build tools/assembly-checks\n' >&2
    exit 1
  fi
fi

compose config --quiet
bash "$factory_dir/scripts/check-image-sources.sh" -f "$compose_file" --env-file "$env_file"
compose pull
bash "$factory_dir/scripts/check-pulled-images.sh" -f "$compose_file" --env-file "$env_file"

cleanup_armed=1
compose up -d --wait --wait-timeout 120
web_address="$(compose port frontend 8080)"
api_address="$(compose port gateway 8080)"
"$checks_bin" smoke \
  --factory-dir "$factory_dir" \
  --env-file "$env_file" \
  --web-url "http://$web_address" \
  --api-url "http://$api_address"
