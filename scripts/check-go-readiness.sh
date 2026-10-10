#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
scratch="$(mktemp -d "${TMPDIR:-/tmp}/factory-go-readiness.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT
find . -type d \( -name .git -o -name .cache -o -name vendor -o -name node_modules -o -name testdata \) -prune -o -type f -name go.mod -print | sed 's#/go.mod$##;s#^\./##' | LC_ALL=C sort > "$scratch/actual"
sed '/^[[:space:]]*#/d;/^[[:space:]]*$/d' .go-quality-modules | LC_ALL=C sort > "$scratch/expected"
if ! diff -u "$scratch/expected" "$scratch/actual"; then
  printf 'Factory Go adoption is not ready: expected modules are missing or undeclared modules exist.\n' >&2
  exit 1
fi
printf 'Factory modules are present. Use their effective quality configs and the controlled business runner; readiness does not prove tests passed.\n'
