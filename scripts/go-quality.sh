#!/usr/bin/env bash
set -euo pipefail
root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$root"
source .go-quality-versions
scratch="$(mktemp -d "${TMPDIR:-/tmp}/umir-factory-quality.XXXXXX")"
trap 'rm -rf "$scratch"' EXIT
action="${1:-check}"
shift "$(( $# > 0 ? 1 : 0 ))"
find . -type d \( -name .git -o -name .cache -o -name vendor -o -name node_modules \) -prune -o -type f -name go.mod -print | sed 's#/go.mod$##;s#^\./##' | LC_ALL=C sort > "$scratch/actual"
sed '/^[[:space:]]*#/d;/^[[:space:]]*$/d' .go-quality-modules | LC_ALL=C sort > "$scratch/expected"
if ! diff -u "$scratch/expected" "$scratch/actual"; then
  printf 'Missing or undeclared Go module; update the reviewed inventory\n' >&2
  exit 1
fi
tool() {
  local name="$1" version="$2" candidate output
  candidate="${GO_QUALITY_BIN:-$root/.cache/go-quality/bin}/$name"
  if [[ ! -x "$candidate" ]]; then candidate="$(command -v "$name" || true)"; fi
  [[ -n "$candidate" ]] || { printf 'Missing %s; run make tools\n' "$name" >&2; return 1; }
  if [[ "$name" == golangci-lint ]]; then
    output="$("$candidate" version)"
    [[ "$output" == *"version $version "* ]] || { printf 'Expected %s %s: %s\n' "$name" "$version" "$output" >&2; return 1; }
  else
    output="$("$candidate" --output-color=false version)"
    [[ "$output" == *"Linter version: $version"$'\n'* ]] || { printf 'Expected %s %s: %s\n' "$name" "$version" "$output" >&2; return 1; }
  fi
  printf '%s\n' "$candidate"
}
module_root() {
  case "$1" in
    tools) printf '%s\n' "$root/tools/assembly-checks" ;;
    business) printf '%s\n' "$root/tests/business" ;;
    http) printf '%s\n' "$root/tests/http" ;;
    durable) printf '%s\n' "$root/reference/durable" ;;
    *) printf 'Unknown factory module: %s\n' "$1" >&2; return 2 ;;
  esac
}
workspace() {
  local warehouse="${WAREHOUSE_DIR:-$root/../umir-warehouse}" serving="${SERVING_DIR:-$root/../umir-serving}"
  [[ -f "$warehouse/go.mod" ]] || { printf 'Set WAREHOUSE_DIR to a checkout with go.mod\n' >&2; return 1; }
  [[ -f "$serving/go.mod" ]] || { printf 'Set SERVING_DIR to a checkout with go.mod\n' >&2; return 1; }
  grep -q '^module github.com/JustIkra/umir-warehouse$' "$warehouse/go.mod" || { printf 'WAREHOUSE_DIR has the wrong module path\n' >&2; return 1; }
  grep -q '^module github.com/JustIkra/umir-serving$' "$serving/go.mod" || { printf 'SERVING_DIR has the wrong module path\n' >&2; return 1; }
  rm -f "$scratch/go.work"
  (cd "$scratch" && GOWORK=off go work init "$root/tests/business" "$warehouse" "$serving")
  printf '%s\n' "$scratch/go.work"
}
run_module() {
  local name="$1" action_name="$2" dir lint arch_file config
  local path
  local -a paths=()
  shift 2
  dir="$(module_root "$name")"
  lint="$(tool golangci-lint "$GOLANGCI_LINT_VERSION")"
  config="$root/.golangci.yml"
  arch_file="$root/.go-arch-lint.yml"
  if [[ "$name" == business ]]; then
    arch_file="$root/.go-arch-business.yml"
    local work
    work="$(workspace)"
    export GOWORK="$work"
  elif [[ "$name" == http ]]; then
    arch_file="$root/.go-arch-http.yml"
    export GOWORK=off
  elif [[ "$name" == durable ]]; then
    arch_file="$root/.go-arch-durable.yml"
    export GOWORK=off
  else
    export GOWORK=off
  fi
  case "$action_name" in
    fmt)
      if [[ $# -gt 0 ]]; then
        if [[ "$1" == "--" ]]; then shift; fi
        [[ $# -gt 0 ]] || { printf 'Provide one or more paths after --\n' >&2; return 1; }
        paths=("$@")
      else
        [[ -n "${FILES:-}" ]] || { printf 'Set FILES to newline-separated module-relative paths, or pass paths after --\n' >&2; return 1; }
        while IFS= read -r path || [[ -n "$path" ]]; do
          [[ -n "$path" ]] && paths+=("$path")
        done <<< "$FILES"
        [[ ${#paths[@]} -gt 0 ]] || { printf 'FILES must contain at least one non-empty path\n' >&2; return 1; }
      fi
      (cd "$dir" && "$lint" fmt --config "$config" "${paths[@]}")
      ;;
    fmt-all) (cd "$dir" && "$lint" fmt --config "$config") ;;
    fmt-check)
      (cd "$dir" && "$lint" fmt --config "$config" --diff) > "$scratch/$name-format.diff" 2> "$scratch/$name-format.err" || { cat "$scratch/$name-format.err" >&2; return 1; }
      if [[ -s "$scratch/$name-format.diff" ]]; then cat "$scratch/$name-format.diff"; return 1; fi
      ;;
    build)
      if [[ "$name" == business ]]; then
        mkdir -p "$scratch/$name-build"
        (cd "$dir" && go test -c -o "$scratch/$name-build/" ./...)
      elif [[ "$name" == durable ]]; then
        # A library module has no main packages; -o would fail with "no main packages to build".
        (cd "$dir" && go build ./...)
      else
        mkdir -p "$scratch/$name-build"
        (cd "$dir" && go build -o "$scratch/$name-build/" ./...)
      fi
      ;;
    lint)
      "$lint" config verify --config "$config"
      (cd "$dir" && "$lint" run --config "$config" ./...)
      ;;
    arch)
      local arch
      arch="$(tool go-arch-lint "$GO_ARCH_LINT_VERSION")"
      "$arch" --output-color=false check --project-path "$dir" --arch-file "$arch_file"
      ;;
    test) (cd "$dir" && go test -race -count=1 ./...) ;;
    *) printf 'Unknown factory module action: %s\n' "$action_name" >&2; return 2 ;;
  esac
}
run_tools() {
  local operation="$1"
  run_module tools "$operation" "${@:2}"
}
run_business() {
  local operation="$1"
  run_module business "$operation" "${@:2}"
}
run_http() {
  local operation="$1"
  run_module http "$operation" "${@:2}"
}
run_durable() {
  local operation="$1"
  run_module durable "$operation" "${@:2}"
}
run_named_module() {
  case "$1" in
    tools) run_tools "$2" "${@:3}" ;;
    business) run_business "$2" "${@:3}" ;;
    http) run_http "$2" "${@:3}" ;;
    durable) run_durable "$2" "${@:3}" ;;
  esac
}
case "$action" in
  fmt)
    case "${QUALITY_MODULE:-}" in
      tools) run_tools fmt "$@" ;;
      business) run_business fmt "$@" ;;
      http) run_http fmt "$@" ;;
      durable) run_durable fmt "$@" ;;
      *) printf 'Set QUALITY_MODULE=tools, business, http, or durable to select module-relative formatter paths\n' >&2; exit 2 ;;
    esac
    ;;
  fmt-all|fmt-check|build|lint|arch|test)
    case "${QUALITY_MODULE:-all}" in
      tools) run_tools "$action" "$@" ;;
      business) run_business "$action" "$@" ;;
      all)
        run_tools "$action" "$@"
        run_business "$action" "$@"
        run_http "$action" "$@"
        run_durable "$action" "$@"
        ;;
      http) run_http "$action" "$@" ;;
      durable) run_durable "$action" "$@" ;;
      *) printf 'QUALITY_MODULE must be tools, business, http, durable, or all\n' >&2; exit 2 ;;
    esac
    ;;
  check-tools|check-business|check-http|check-durable)
    case "$action" in
      check-tools) module=tools ;;
      check-business) module=business ;;
      check-http) module=http ;;
      check-durable) module=durable ;;
    esac
    for step in fmt-check build lint arch test; do run_named_module "$module" "$step"; done
    ;;
  check)
    for step in fmt-check build lint arch test; do
      run_tools "$step"
      run_business "$step"
      run_http "$step"
      run_durable "$step"
    done
    ;;
  *) printf 'Unknown quality command: %s\n' "$action" >&2; exit 1 ;;
esac
