#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/umir-http-stage-checks.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT
script="$root/scripts/http-acceptance-stage.sh"
check() {
  local label="$1" input="$2" expected="$3" status="$4" actual code=0
  printf '%s' "$input" > "$scratch/compose.yaml"
  actual="$(PATH=/nonexistent /bin/bash "$script" "$scratch/compose.yaml" 2> "$scratch/stderr")" || code=$?
  [[ "$code" == "$status" && "$actual" == "$expected" ]] || {
    printf 'FAIL %s: code=%s output=%q\n' "$label" "$code" "$actual" >&2
    exit 1
  }
  printf 'PASS: %s\n' "$label"
}
check 'selected runs without rg or grep' $'x-umir-http-acceptance: selected\n' 'enabled=true' 0
check 'selected without final newline' 'x-umir-http-acceptance: selected' 'enabled=true' 0
check 'health-only explicitly disables HTTP' $'x-umir-http-acceptance: health-only\n' 'enabled=false' 0
check 'missing marker fails' $'services: {}\n' '' 1
check 'unknown marker fails' $'x-umir-http-acceptance: typo\n' '' 1
check 'duplicate markers fail' $'x-umir-http-acceptance: selected\nx-umir-http-acceptance: health-only\n' '' 1
code=0
PATH=/nonexistent /bin/bash "$script" "$scratch/missing.yaml" > "$scratch/output" 2> "$scratch/stderr" || code=$?
[[ "$code" != 0 && ! -s "$scratch/output" ]] || { printf 'FAIL: unreadable file\n' >&2; exit 1; }
printf 'PASS: unreadable Compose file fails\n'
