#!/usr/bin/env bash
set -euo pipefail
[[ $# -le 1 ]] || { printf 'Expected at most one Compose file path\n' >&2; exit 2; }
config="${1:-${BASH_SOURCE[0]%/*}/../compose.yaml}"
stage=''
while IFS= read -r line || [[ -n "$line" ]]; do
  case "$line" in
    'x-umir-http-acceptance: selected') value=selected ;;
    'x-umir-http-acceptance: health-only') value=health-only ;;
    x-umir-http-acceptance:*) printf 'Unsupported HTTP acceptance marker\n' >&2; exit 1 ;;
    *) continue ;;
  esac
  [[ -z "$stage" ]] || { printf 'Duplicate HTTP acceptance marker\n' >&2; exit 1; }
  stage="$value"
done < "$config"
case "$stage" in
  selected) printf 'enabled=true\n' ;;
  health-only) printf 'enabled=false\n' ;;
  *) printf 'Missing HTTP acceptance marker\n' >&2; exit 1 ;;
esac
