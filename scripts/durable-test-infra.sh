#!/usr/bin/env bash
# Test infrastructure for the durable-stack integration suites (W0.6).
#
#   check  verify that umir-prod-pg and umir-prod-mq answer; changes nothing
#   up     create the missing umir-durable-redis-* containers (never recreates)
#   env    print an `export UMIR_TEST_*=...` line for `eval "$(... env)"`
#   down   remove only containers named umir-durable-redis-*
#
# Ports 5432 and 6379 belong to other projects and are never used.
#
# Redis password handling: one synthetic password is generated on the first
# `up` and kept in <repo>/.cache/durable-test-infra/env (dir 0700, file 0600).
# It is passed to `docker run` by name only (`-e VAR`, value inherited from this
# process), so it is not visible in the host argv or in `docker ps`. Inside the
# container a tiny `sh -c` wrapper writes `requirepass` to a 0600 config file
# and execs `redis-server <file> ...`, so the password is not in the redis-server
# argv either. `--requirepass` on the command line was deliberately avoided.
# The container environment still holds the password (visible to `docker
# inspect` on this machine), the same as the prod containers' credentials.
#
# Secrets are never printed; xtrace is disabled so an inherited `set -x` cannot
# leak them. Test hooks (used by tests/infra/durable-test-infra.test.sh):
# UMIR_DURABLE_STATE_DIR, UMIR_DURABLE_SKIP_HOST_PROBE, UMIR_DURABLE_WAIT_SECONDS.
set -euo pipefail
set +x

repo_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
state_dir="${UMIR_DURABLE_STATE_DIR:-$repo_dir/.cache/durable-test-infra}"
state_file="$state_dir/env"

host=127.0.0.1
pg_container=umir-prod-pg
pg_port=55432
mq_container=umir-prod-mq
mq_port=55672
mq_mgmt_port=55673
redis_prefix=umir-durable-redis-
limiter_container="${redis_prefix}limiter"
limiter_port=56379
cache_container="${redis_prefix}cache"
cache_port=56380
redis_image="${UMIR_DURABLE_REDIS_IMAGE:-redis:7.4-alpine}"
wait_seconds="${UMIR_DURABLE_WAIT_SECONDS:-30}"

die() { printf 'durable-test-infra: %s\n' "$*" >&2; exit 1; }

container_exists() { docker container inspect --format '{{.State.Running}}' "$1" > /dev/null 2>&1; }
container_running() { [[ "$(docker container inspect --format '{{.State.Running}}' "$1" 2> /dev/null)" == true ]]; }

host_port_open() {
  [[ -n "${UMIR_DURABLE_SKIP_HOST_PROBE:-}" ]] && return 0
  (exec 3<> "/dev/tcp/$host/$1") 2> /dev/null
}

# container_env <container> <VAR>: value from the container configuration.
container_env() {
  local envs
  envs="$(docker container inspect --format '{{range .Config.Env}}{{println .}}{{end}}' "$1" 2> /dev/null)" || return 1
  printf '%s\n' "$envs" | sed -n "s/^$2=//p" | head -n 1
}

# uri_encode: percent-encode stdin.
uri_encode() { jq -Rrs '@uri'; }

redis_password() {
  [[ -f "$state_file" ]] || return 1
  sed -n 's/^UMIR_DURABLE_REDIS_PASSWORD=//p' "$state_file" | head -n 1
}

ensure_password() {
  if [[ -n "$(redis_password 2> /dev/null || true)" ]]; then return 0; fi
  local old_umask password
  mkdir -p "$state_dir"
  chmod 700 "$state_dir"
  password="$(LC_ALL=C tr -dc 'A-Za-z0-9' < /dev/urandom | head -c 40)" || true
  [[ ${#password} -ge 24 ]] || die 'cannot generate a password'
  old_umask="$(umask)"
  umask 077
  printf 'UMIR_DURABLE_REDIS_PASSWORD=%s\n' "$password" > "$state_file"
  umask "$old_umask"
  chmod 600 "$state_file"
}

cmd_check() {
  local failed=0
  container_running "$pg_container" || { printf 'not running: %s\n' "$pg_container" >&2; failed=1; }
  container_running "$mq_container" || { printf 'not running: %s\n' "$mq_container" >&2; failed=1; }
  [[ $failed == 0 ]] || return 1
  docker exec "$pg_container" pg_isready -q > /dev/null 2>&1 || { printf 'postgres does not accept connections in %s\n' "$pg_container" >&2; failed=1; }
  docker exec "$mq_container" rabbitmq-diagnostics -q ping > /dev/null 2>&1 || { printf 'rabbitmq does not answer in %s\n' "$mq_container" >&2; failed=1; }
  local p
  for p in "$pg_port" "$mq_port" "$mq_mgmt_port"; do
    host_port_open "$p" || { printf 'nothing listens on %s:%s\n' "$host" "$p" >&2; failed=1; }
  done
  [[ $failed == 0 ]] || return 1
  printf 'ok: %s (%s:%s), %s (%s:%s, management %s)\n' "$pg_container" "$host" "$pg_port" "$mq_container" "$host" "$mq_port" "$mq_mgmt_port"
}

# start_redis <name> <host port> <redis flags...>
start_redis() {
  local name="$1" port="$2"
  shift 2
  [[ "$name" == "$redis_prefix"* ]] || die "refusing to manage $name"
  if container_exists "$name"; then
    if container_running "$name"; then
      printf 'exists: %s\n' "$name"
    else
      docker start "$name" > /dev/null
      printf 'started: %s\n' "$name"
    fi
    return 0
  fi
  UMIR_DURABLE_REDIS_PASSWORD="$(redis_password)" docker run -d \
    --name "$name" \
    --label umir.durable-test-infra=1 \
    -p "$host:$port:6379" \
    -e UMIR_DURABLE_REDIS_PASSWORD \
    "$redis_image" \
    sh -c 'umask 077; printf "requirepass %s\n" "$UMIR_DURABLE_REDIS_PASSWORD" > /tmp/auth.conf; exec redis-server /tmp/auth.conf "$@"' sh \
    "$@" > /dev/null
  printf 'created: %s (%s:%s)\n' "$name" "$host" "$port"
}

wait_redis() {
  local name="$1" deadline=$((SECONDS + wait_seconds)) reply
  while :; do
    reply="$(REDISCLI_AUTH="$(redis_password)" docker exec -e REDISCLI_AUTH "$name" redis-cli ping 2> /dev/null || true)"
    [[ "$reply" == PONG ]] && return 0
    (( SECONDS < deadline )) || die "$name did not answer PING within ${wait_seconds}s (if the container predates the password file, run: down, up)"
    sleep 0.5
  done
}

cmd_up() {
  ensure_password
  start_redis "$limiter_container" "$limiter_port" --appendonly yes --appendfsync everysec --maxmemory-policy noeviction
  start_redis "$cache_container" "$cache_port" --appendonly no --save '' --maxmemory-policy allkeys-lru
  wait_redis "$limiter_container"
  wait_redis "$cache_container"
  printf 'ready: %s, %s\n' "$limiter_container" "$cache_container"
}

cmd_env() {
  local pg_user pg_pass mq_user mq_pass rp
  container_running "$pg_container" || die "$pg_container is not running"
  container_running "$mq_container" || die "$mq_container is not running"
  rp="$(redis_password 2> /dev/null || true)"
  [[ -n "$rp" ]] || die 'no Redis password yet: run `durable-test-infra.sh up` first'

  # Credentials come from the container configuration (docker run -e ...).
  pg_user="$(container_env "$pg_container" POSTGRES_USER)"
  pg_pass="$(container_env "$pg_container" POSTGRES_PASSWORD)"
  mq_user="$(container_env "$mq_container" RABBITMQ_DEFAULT_USER)"
  mq_pass="$(container_env "$mq_container" RABBITMQ_DEFAULT_PASS)"
  [[ -n "$pg_user" ]] || pg_user=postgres
  [[ -n "$mq_user" ]] || mq_user=guest
  [[ -n "$mq_pass" ]] || mq_pass=guest

  local pg_auth mq_auth
  pg_auth="$(printf '%s' "$pg_user" | uri_encode)"
  [[ -z "$pg_pass" ]] || pg_auth="$pg_auth:$(printf '%s' "$pg_pass" | uri_encode)"
  mq_auth="$(printf '%s' "$mq_user" | uri_encode):$(printf '%s' "$mq_pass" | uri_encode)"
  rp="$(printf '%s' "$rp" | uri_encode)"

  # Tests create their own t_<repo>_<rand> database and vhost; the administrative
  # database `postgres` and the default vhost `/` are only the entry points.
  printf 'export UMIR_TEST_DATABASE_URL=%q UMIR_TEST_AMQP_URL=%q UMIR_TEST_AMQP_MANAGEMENT_URL=%q UMIR_TEST_REDIS_LIMITER_URL=%q UMIR_TEST_REDIS_CACHE_URL=%q\n' \
    "postgres://$pg_auth@$host:$pg_port/postgres?sslmode=disable" \
    "amqp://$mq_auth@$host:$mq_port/%2F" \
    "http://$mq_auth@$host:$mq_mgmt_port" \
    "redis://:$rp@$host:$limiter_port/0" \
    "redis://:$rp@$host:$cache_port/0"
}

cmd_down() {
  local name
  while IFS= read -r name; do
    [[ "$name" == "$redis_prefix"* ]] || continue
    docker rm -f -v "$name" > /dev/null
    printf 'removed: %s\n' "$name"
  done < <(docker ps -a --filter "name=^$redis_prefix" --format '{{.Names}}')
}

case "${1:-}" in
  check) cmd_check ;;
  up) cmd_up ;;
  env) cmd_env ;;
  down) cmd_down ;;
  *) printf 'usage: %s check|up|env|down\n' "$0" >&2; exit 2 ;;
esac
