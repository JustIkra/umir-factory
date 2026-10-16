#!/usr/bin/env bash
# Tests scripts/durable-test-infra.sh against a shim `docker` that logs every call.
set -euo pipefail

factory_dir="$(cd "$(dirname "$0")/../.." && pwd)"
test_dir="$(mktemp -d)"
trap 'rm -rf "$test_dir"' EXIT

pg_password='p@ss/w:rd-PGSECRET'
mq_password='mq#secret?MQSECRET'

cat > "$test_dir/docker" <<'SH'
#!/usr/bin/env bash
# Shim: records the call, answers from SHIM_EXISTING (space separated names).
set -euo pipefail
printf '%q ' "$@" | sed 's/ $//' >> "$SHIM_LOG"
echo >> "$SHIM_LOG"
args=("$@")
verb="${args[0]}"
[[ "$verb" != container ]] || { args=("${args[@]:1}"); verb="${args[0]}"; }
exists() { [[ " $SHIM_EXISTING " == *" $1 "* ]]; }
case "$verb" in
  inspect)
    name="${args[$((${#args[@]} - 1))]}"
    exists "$name" || exit 1
    case "$*" in
      *State.Running*) if [[ " ${SHIM_STOPPED:-} " == *" $name "* ]]; then echo false; else echo true; fi ;;
      *Config.Env*)
        case "$name" in
          umir-prod-pg) printf 'POSTGRES_PASSWORD=%s\nPOSTGRES_DB=umir\nPOSTGRES_USER=umir\nPG_MAJOR=17\n' "$SHIM_PG_PASSWORD" ;;
          umir-prod-mq) printf 'RABBITMQ_DEFAULT_PASS=%s\nRABBITMQ_DEFAULT_USER=umir\nHOME=/x\n' "$SHIM_MQ_PASSWORD" ;;
        esac ;;
    esac ;;
  ps)
    for n in $SHIM_EXISTING; do echo "$n"; done ;;
  run)
    [[ -n "${UMIR_DURABLE_REDIS_PASSWORD:-}" ]] && echo "RUN_ENV_PASSWORD_PRESENT" >> "$SHIM_LOG"
    echo "container-id" ;;
  rm|stop|start|kill|restart) ;;
  exec)
    case "$*" in
      *redis-cli*) echo PONG ;;
      *pg_isready*|*rabbitmq-diagnostics*) ;;
      *) echo "UNEXPECTED exec" >> "$SHIM_LOG"; exit 1 ;;
    esac ;;
  port) ;;
  *) echo "UNEXPECTED $*" >> "$SHIM_LOG"; exit 1 ;;
esac
SH
chmod +x "$test_dir/docker"
export PATH="$test_dir:$PATH"
export SHIM_LOG="$test_dir/docker.log"
export SHIM_PG_PASSWORD="$pg_password" SHIM_MQ_PASSWORD="$mq_password"
export UMIR_DURABLE_STATE_DIR="$test_dir/state"
export UMIR_DURABLE_SKIP_HOST_PROBE=1
export UMIR_DURABLE_WAIT_SECONDS=2
script="$factory_dir/scripts/durable-test-infra.sh"

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }

export SHIM_STOPPED=""

# run_case <existing containers> <command...>: sets $out/$err files and clears the log.
run_case() {
  SHIM_EXISTING="$1"; export SHIM_EXISTING; shift
  : > "$SHIM_LOG"
  local rc=0
  bash "$script" "$@" > "$test_dir/out" 2> "$test_dir/err" || rc=$?
  return "$rc"
}

mutating() { grep -E '^(container )?(run|rm|stop|kill|restart|start|create|pause|unpause|update|rename) ' "$SHIM_LOG" || true; }

assert_no_secret_in() {
  local label="$1"; shift
  local f secret
  for f in "$@"; do
    for secret in PGSECRET MQSECRET "$(sed -n 's/^UMIR_DURABLE_REDIS_PASSWORD=//p' "$UMIR_DURABLE_STATE_DIR/env" 2>/dev/null || true)"; do
      [[ -n "$secret" ]] || continue
      ! grep -qF -- "$secret" "$f" || fail "$label leaks a secret into $(basename "$f")"
    done
  done
}

prod="umir-prod-pg umir-prod-mq"
both="$prod umir-durable-redis-limiter umir-durable-redis-cache"

# (0) unknown command fails.
run_case "$prod" bogus && fail "bogus command must fail"
printf 'PASS: unknown command rejected\n'

# (1) check never mutates anything, and fails when a prod container is missing.
run_case "$prod" check || { cat "$test_dir/err" >&2; fail "check with prod up"; }
[[ -z "$(mutating)" ]] || fail "check called a mutating command: $(mutating)"
[[ ! -e "$UMIR_DURABLE_STATE_DIR" ]] || fail "check created state"
assert_no_secret_in check "$test_dir/out" "$test_dir/err" "$SHIM_LOG"
run_case "umir-prod-pg" check && fail "check must fail without umir-prod-mq"
[[ -z "$(mutating)" ]] || fail "failing check called a mutating command"
printf 'PASS: check is read-only\n'

# (2) up creates only the missing durable Redis containers; never recreates.
run_case "$prod" up || { cat "$test_dir/err" >&2; fail "up from scratch"; }
runs="$(grep -E '^run ' "$SHIM_LOG" || true)"
[[ "$(printf '%s\n' "$runs" | grep -c .)" == 2 ]] || fail "up must create exactly two containers: $runs"
printf '%s\n' "$runs" | grep -q -- '--name umir-durable-redis-limiter' || fail "limiter not created"
printf '%s\n' "$runs" | grep -q -- '--name umir-durable-redis-cache' || fail "cache not created"
printf '%s\n' "$runs" | grep -q -- '127.0.0.1:56379:6379' || fail "limiter port"
printf '%s\n' "$runs" | grep -q -- '127.0.0.1:56380:6379' || fail "cache port"
limiter_run="$(printf '%s\n' "$runs" | grep -- '--name umir-durable-redis-limiter')"
cache_run="$(printf '%s\n' "$runs" | grep -- '--name umir-durable-redis-cache')"
case "$limiter_run" in *' --maxmemory-policy noeviction'*) ;; *) fail "limiter policy" ;; esac
case "$limiter_run" in *' --appendonly yes'*) ;; *) fail "limiter AOF" ;; esac
case "$limiter_run" in *' --appendfsync everysec'*) ;; *) fail "limiter appendfsync" ;; esac
case "$cache_run" in *' --maxmemory-policy allkeys-lru'*) ;; *) fail "cache policy" ;; esac
case "$cache_run" in *' --appendonly no'*) ;; *) fail "cache persistence" ;; esac
case "$cache_run" in *" --save '' "*) ;; *) fail "cache must disable RDB snapshots (--save '')" ;; esac
case "$cache_run" in *' --maxmemory 512mb'*) ;; *) fail "cache must set --maxmemory 512mb" ;; esac
case "$limiter_run" in *' --maxmemory '*) fail "limiter must not set maxmemory" ;; esac
for r in "$limiter_run" "$cache_run"; do
  case "$r" in *' -e UMIR_DURABLE_REDIS_PASSWORD '*) ;; *) fail "password must be passed by name: -e UMIR_DURABLE_REDIS_PASSWORD" ;; esac
  case "$r" in *UMIR_DURABLE_REDIS_PASSWORD=*) fail "password value in argv" ;; esac
  case "$r" in *' --label umir.durable-test-infra=1 '*) ;; *) fail "container label missing" ;; esac
done
printf '%s\n' "$runs" | grep -qE -- '(^| )-p (127.0.0.1:)?(5432|6379):' && fail "forbidden host port"
if mutating | grep -vE 'umir-durable-redis-' | grep -q .; then fail "up mutated a foreign container"; fi
grep -q RUN_ENV_PASSWORD_PRESENT "$SHIM_LOG" || fail "password not passed through the environment"
env_file="$UMIR_DURABLE_STATE_DIR/env"
[[ -f "$env_file" ]] || fail "state file missing"
# GNU stat first: on GNU `stat -f` succeeds with filesystem data, on BSD `stat -c` fails.
[[ "$(stat -c '%a' "$env_file" 2>/dev/null || stat -f '%Lp' "$env_file")" == 600 ]] || fail "state file mode"
[[ "$(stat -c '%a' "$UMIR_DURABLE_STATE_DIR" 2>/dev/null || stat -f '%Lp' "$UMIR_DURABLE_STATE_DIR")" == 700 ]] || fail "state dir mode"
redis_password="$(sed -n 's/^UMIR_DURABLE_REDIS_PASSWORD=//p' "$env_file")"
[[ ${#redis_password} -ge 24 ]] || fail "redis password too short"
! grep -qF -- "$redis_password" "$SHIM_LOG" || fail "redis password appears in docker argv"
assert_no_secret_in up "$test_dir/out" "$test_dir/err"

run_case "$both" up || { cat "$test_dir/err" >&2; fail "idempotent up"; }
[[ -z "$(mutating)" ]] || fail "up recreated existing containers: $(mutating)"
[[ "$(sed -n 's/^UMIR_DURABLE_REDIS_PASSWORD=//p' "$env_file")" == "$redis_password" ]] || fail "password regenerated"

run_case "$prod umir-durable-redis-limiter" up || fail "partial up"
[[ "$(grep -cE '^run ' "$SHIM_LOG")" == 1 ]] || fail "partial up must create only the cache"
grep -E '^run ' "$SHIM_LOG" | grep -q -- '--name umir-durable-redis-cache' || fail "partial up created wrong container"
printf 'PASS: up creates only missing durable containers\n'

# (2b) a stopped durable container is started, never recreated.
SHIM_STOPPED="umir-durable-redis-cache" run_case "$both" up || { cat "$test_dir/err" >&2; fail "up with a stopped container"; }
grep -qx 'start umir-durable-redis-cache' "$SHIM_LOG" || fail "stopped container was not started: $(cat "$SHIM_LOG")"
[[ -z "$(grep -E '^(run|rm) ' "$SHIM_LOG" || true)" ]] || fail "stopped container was recreated"
[[ "$(grep -cE '^start ' "$SHIM_LOG")" == 1 ]] || fail "only the stopped container may be started"
printf 'PASS: up starts a stopped container\n'

# (3) env prints exports on stdout, nothing secret on stderr, no mutations.
run_case "$both" env || { cat "$test_dir/err" >&2; fail "env"; }
[[ -z "$(mutating)" ]] || fail "env called a mutating command: $(mutating)"
[[ ! -s "$test_dir/err" ]] || fail "env wrote to stderr"
line="$(cat "$test_dir/out")"
[[ "$(printf '%s\n' "$line" | wc -l | tr -d ' ')" == 1 ]] || fail "env must print one line"
for var in UMIR_TEST_DATABASE_URL UMIR_TEST_AMQP_URL UMIR_TEST_AMQP_MANAGEMENT_URL UMIR_TEST_REDIS_LIMITER_URL UMIR_TEST_REDIS_CACHE_URL; do
  [[ "$line" == export\ *" $var="* || "$line" == "export $var="* ]] || fail "env lacks $var"
done
eval "$line"
[[ "$UMIR_TEST_DATABASE_URL" == 'postgres://umir:p%40ss%2Fw%3Ard-PGSECRET@127.0.0.1:55432/postgres?sslmode=disable' ]] || fail "database url"
[[ "$UMIR_TEST_AMQP_URL" == 'amqp://umir:mq%23secret%3FMQSECRET@127.0.0.1:55672/%2F' ]] || fail "amqp url"
[[ "$UMIR_TEST_AMQP_MANAGEMENT_URL" == 'http://umir:mq%23secret%3FMQSECRET@127.0.0.1:55673' ]] || fail "amqp management url"
[[ "$UMIR_TEST_REDIS_LIMITER_URL" == "redis://:${redis_password}@127.0.0.1:56379/0" ]] || fail "limiter url"
[[ "$UMIR_TEST_REDIS_CACHE_URL" == "redis://:${redis_password}@127.0.0.1:56380/0" ]] || fail "cache url"
! grep -qE 'PGSECRET|MQSECRET' "$test_dir/err" || fail "env leaks to stderr"
# env fails clearly (and quietly) when prod is missing or up was never run.
run_case "umir-prod-pg" env && fail "env must fail without umir-prod-mq"
assert_no_secret_in env-fail "$test_dir/out" "$test_dir/err"
printf 'PASS: env prints exports only on stdout\n'

# env refuses to guess the RabbitMQ password when the container has none.
SHIM_MQ_PASSWORD="" run_case "$both" env && fail "env must fail without RABBITMQ_DEFAULT_PASS"
[[ ! -s "$test_dir/out" ]] || fail "env printed exports without a RabbitMQ password"
grep -q 'RABBITMQ_DEFAULT_PASS' "$test_dir/err" || fail "missing-password error must name the variable"
printf 'PASS: env fails clearly without a RabbitMQ password\n'

# env needs jq and says so up front.
nojq="$test_dir/nojq"
mkdir "$nojq"
for tool in dirname sed head tr mkdir chmod cat grep; do ln -s "$(command -v "$tool")" "$nojq/$tool"; done
cp "$test_dir/docker" "$nojq/docker"
SHIM_EXISTING="$both"; export SHIM_EXISTING
rc=0
PATH="$nojq" "$(command -v bash)" "$script" env > "$test_dir/out" 2> "$test_dir/err" || rc=$?
[[ "$rc" != 0 ]] || fail "env must fail without jq"
grep -q 'jq' "$test_dir/err" || fail "missing jq must be reported"
printf 'PASS: env requires jq\n'

# (4) down removes only umir-durable-redis-* containers.
run_case "$both umir-durable-redis-foo" down || { cat "$test_dir/err" >&2; fail "down"; }
touched="$(mutating)"
[[ -n "$touched" ]] || fail "down removed nothing"
printf '%s\n' "$touched" | grep -qE 'umir-prod' && fail "down touched a prod container"
printf '%s\n' "$touched" | grep -qE '^rm ' || fail "down did not rm"
grep -E '^ps ' "$SHIM_LOG" | grep -q -- 'label=umir.durable-test-infra=1' || fail "down must filter by the umir.durable-test-infra label"
while read -r l; do
  [[ -z "$l" ]] && continue
  for w in $l; do
    case "$w" in umir-*) [[ "$w" == umir-durable-redis-* ]] || fail "down touched $w" ;; esac
  done
done <<< "$touched"
printf 'PASS: down only removes durable redis containers\n'

# state file survives down (password reused by next up).
[[ -f "$env_file" ]] || fail "down removed state file"
printf 'ALL PASS\n'
