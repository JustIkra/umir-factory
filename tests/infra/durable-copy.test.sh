#!/usr/bin/env bash
# Tests reference/durable/copy.sh on a temporary service directory.
set -euo pipefail

factory_dir="$(cd "$(dirname "$0")/../.." && pwd)"
copy="$factory_dir/reference/durable/copy.sh"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
svc="$work/svc"
mkdir -p "$svc"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

"$copy" postgres rabbitmq redis relay "$svc" >/dev/null || fail "copy failed"
for p in postgres rabbitmq redis relay; do
  [[ -f "$svc/internal/adapters/$p/doc.go" ]] || fail "$p/doc.go not copied"
done
ls "$svc"/internal/adapters/*/*_test.go >/dev/null 2>&1 && fail "test files were copied"
[[ -f "$svc/internal/adapters/postgres/outbox.sql" ]] || fail "sql fragment not copied"
head -n 1 "$svc/internal/adapters/relay/relay.go" | grep -q '^// Source: umir-factory reference/durable@' || fail "no Source line"
[[ -z "$(sed -n 2p "$svc/internal/adapters/relay/relay.go")" ]] || fail "no blank line after Source"

"$copy" --check postgres rabbitmq redis relay "$svc" >/dev/null || fail "check of a fresh copy failed"

# A changed Source revision alone is not a difference.
perl -pi -e 's/^(\/\/ Source: .*@).*/${1}abcdef123456/ if $. == 1' "$svc/internal/adapters/redis/limiter.go"
"$copy" --check redis "$svc" >/dev/null || fail "a different Source revision was reported"

# A changed body, a missing file and an extra file are reported.
echo '// edited' >> "$svc/internal/adapters/redis/limiter.go"
out="$("$copy" --check redis "$svc" || true)"
grep -q 'differs: .*limiter.go' <<<"$out" || fail "edit not reported: $out"
"$copy" --check redis "$svc" >/dev/null 2>&1 && fail "edit gave exit 0"
rm "$svc/internal/adapters/redis/slots.go"
out="$("$copy" --check redis "$svc" || true)"
grep -q 'missing: .*slots.go' <<<"$out" || fail "missing file not reported: $out"
touch "$svc/internal/adapters/redis/own.go"
out="$("$copy" --check redis "$svc" || true)"
grep -q 'extra: .*own.go' <<<"$out" || fail "extra file not reported: $out"
touch "$svc/internal/adapters/redis/own_test.go"
out="$("$copy" --check redis "$svc" || true)"
grep -q 'own_test.go' <<<"$out" && fail "service test file was reported"

# Renaming rewrites the package clause.
"$copy" relay:outbox "$svc" >/dev/null
grep -q '^package outbox$' "$svc/internal/adapters/outbox/relay.go" || fail "package not renamed"
"$copy" --check relay:outbox "$svc" >/dev/null || fail "check of a renamed copy failed"

# Bad usage.
"$copy" nosuch "$svc" >/dev/null 2>&1 && fail "unknown package accepted"
"$copy" postgres "$work/none" >/dev/null 2>&1 && fail "missing directory accepted"
echo "ok"
