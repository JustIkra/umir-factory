#!/bin/sh
# Runs Docker Compose for the completion profile with the environment of a
# prepared private bundle:
#
#   scripts/completion/compose.sh --bundle /absolute/private/dir [compose arguments]
#
# `config` prints the resolved model, which can hold secret values (the service
# keys of Warehouse, Serving and Gateway are still environment variables), so it
# is accepted only with --quiet. Everything else is passed to Compose unchanged.
set -eu

usage() {
  echo "usage: compose.sh --bundle <absolute private directory> [docker compose arguments]" >&2
  exit 2
}

[ "${1:-}" = "--bundle" ] && [ -n "${2:-}" ] || usage
bundle=$2
shift 2
case "$bundle" in
  /*) ;;
  *) echo "compose.sh: the bundle path must be absolute" >&2; exit 2 ;;
esac
[ -f "$bundle/compose.env" ] || { echo "compose.sh: $bundle/compose.env not found; run prepare.mjs first" >&2; exit 2; }

# Any argument spelled `config` counts, so options with values cannot hide it.
command=
for argument in "$@"; do
  [ "$argument" = config ] && command=config
done
if [ "$command" = "config" ]; then
  quiet=no
  for argument in "$@"; do
    case "$argument" in
      --quiet|-q) quiet=yes ;;
    esac
  done
  [ "$quiet" = yes ] || { echo "compose.sh: config prints secret values; use config --quiet" >&2; exit 2; }
fi

root=$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)
exec docker compose --env-file "$bundle/compose.env" -f "$root/compose.completion.yaml" "$@"
