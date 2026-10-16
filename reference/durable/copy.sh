#!/usr/bin/env bash
# Copy reference packages into a service, or check that a service copy still
# matches the reference.
#
#   reference/durable/copy.sh [--check] <package>[:<name>]... <service-dir>
#
# <package> is one of postgres, rabbitmq, redis, relay. The production files of
# the package (*.go without _test.go, and *.sql) go to
# <service-dir>/internal/adapters/<name>, where <name> defaults to the package
# name. When <name> differs, the package clause is rewritten to it. Every Go
# file gets the first line
#
#   // Source: umir-factory reference/durable@<sha>
#
# followed by an empty line, so a doc comment stays a doc comment and a
# //go:build line stays valid. Logic is changed only in the reference (through
# a change request); a copy changes nothing but import paths. Tests are not
# copied: a service writes its own and runs the reference suite from the
# factory repository.
#
# --check changes nothing. It reports files that are missing, differ from the
# reference (ignoring the Source line) or exist in the copy without a reference
# counterpart, prints the source revision found in the copy, and exits 1 on any
# difference. A copy made from an older reference revision shows differences
# until it is refreshed.
set -euo pipefail
set +x

here="$(cd "$(dirname "$0")" && pwd)"
check=0
if [[ "${1:-}" == "--check" ]]; then
  check=1
  shift
fi
if (($# < 2)); then
  echo "usage: copy.sh [--check] <package>[:<name>]... <service-dir>" >&2
  exit 2
fi
service_dir="${*: -1}"
packages=("${@:1:$#-1}")
[[ -d "$service_dir" ]] || { echo "copy.sh: no such directory: $service_dir" >&2; exit 2; }

revision="$(git -C "$here" rev-parse --short=12 HEAD 2>/dev/null || echo unknown)"
if [[ "$revision" != unknown && -n "$(git -C "$here" status --porcelain -- . 2>/dev/null)" ]]; then
  revision="${revision}-dirty"
fi

# render <source-file> <name>: the file as a service copy must contain it.
render() {
  local file="$1" name="$2" base
  base="$(basename "$file")"
  if [[ "$base" == *.go ]]; then
    printf '// Source: umir-factory reference/durable@%s\n\n' "$revision"
    if [[ "$name" == "$(basename "$(dirname "$file")")" ]]; then
      cat "$file"
    else
      sed -E "s/^package [A-Za-z0-9_]+/package $name/" "$file"
    fi
  else
    cat "$file"
  fi
}

# stripped <file>: content without the Source line and the blank line after it.
stripped() {
  if [[ "$(head -n 1 "$1")" == "// Source: "* ]]; then tail -n +3 "$1"; else cat "$1"; fi
}

production_files() {
  local dir="$1" f
  for f in "$dir"/*.go "$dir"/*.sql; do
    [[ -e "$f" ]] || continue
    [[ "$f" == *_test.go ]] && continue
    printf '%s\n' "$f"
  done
}

status=0
tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT

for spec in "${packages[@]}"; do
  pkg="${spec%%:*}"
  name="${spec#*:}"
  [[ "$spec" == *:* ]] || name="$pkg"
  case "$pkg" in postgres | rabbitmq | redis | relay) ;; *)
    echo "copy.sh: unknown package: $pkg" >&2
    exit 2
    ;;
  esac
  src="$here/$pkg"
  dst="$service_dir/internal/adapters/$name"

  if ((check == 0)); then
    mkdir -p "$dst"
    while IFS= read -r f; do
      render "$f" "$name" >"$dst/$(basename "$f")"
    done < <(production_files "$src")
    echo "copied $pkg -> $dst @ $revision"
    continue
  fi

  while IFS= read -r f; do
    base="$(basename "$f")"
    target="$dst/$base"
    if [[ ! -f "$target" ]]; then
      echo "missing: $target"
      status=1
      continue
    fi
    render "$f" "$name" | tail -n +3 >"$tmp/expected"
    if [[ "$base" != *.go ]]; then
      render "$f" "$name" >"$tmp/expected"
    fi
    stripped "$target" >"$tmp/actual"
    if ! diff -q "$tmp/expected" "$tmp/actual" >/dev/null; then
      echo "differs: $target"
      status=1
    fi
  done < <(production_files "$src")

  if [[ -d "$dst" ]]; then
    for f in "$dst"/*.go "$dst"/*.sql; do
      [[ -e "$f" ]] || continue
      [[ "$f" == *_test.go ]] && continue
      [[ -f "$src/$(basename "$f")" ]] || { echo "extra: $f"; status=1; }
    done
    first="$(find "$dst" -maxdepth 1 -name '*.go' ! -name '*_test.go' -print -quit)"
    if [[ -n "$first" && "$(head -n 1 "$first")" == "// Source: "* ]]; then
      header="$(head -n 1 "$first")"
      echo "$pkg: copy made from ${header##*@}"
    fi
  else
    echo "missing: $dst"
    status=1
  fi
done

if ((check == 1 && status == 0)); then echo "ok: copies match the reference"; fi
exit "$status"
