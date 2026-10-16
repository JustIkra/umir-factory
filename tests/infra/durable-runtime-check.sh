#!/bin/bash
set -euo pipefail

project=${1:?pass a dedicated durable Compose project name}
[[ $project =~ ^umir-durable-[a-z0-9][a-z0-9_-]{0,48}$ ]] || { printf '%s\n' 'invalid durable project name' >&2; exit 2; }
tools_image=${DURABLE_TOOLS_IMAGE:-umir-durable-tools:local}
network="${project}_data"
secrets="${project}_umir-secrets"
pgdata="${project}_pg-data"
ownerkeys="${project}_auth-owner-secrets"

docker run --rm --network "$network" --user 65532:65532 --entrypoint /bin/bash \
  --mount "type=volume,source=$secrets,target=/run/umir-secrets,readonly" \
  --mount "type=volume,source=$pgdata,target=/run/pg-data,readonly" \
  --mount "type=volume,source=$ownerkeys,target=/run/auth-owner-secrets,readonly" \
  "$tools_image" -euc '
python3 - <<"PY"
import base64
import json
import os
import uuid

path = "/run/auth-owner-secrets/owner.json"
info = os.stat(path, follow_symlinks=False)
assert os.path.isfile(path) and info.st_uid == os.geteuid() == 65532 and info.st_gid == os.getegid() == 65532
assert info.st_mode & 0o777 == 0o600 and info.st_size <= 16384
with open(path, encoding="utf-8") as stream:
    value = json.load(stream)
assert set(value) == {"version", "signing", "resultsKeys", "challengeKey", "challengePepper"} and value["version"] == 2
assert set(value["signing"]) == {"kid", "seed"}
keys = [value["signing"]["seed"], value["challengeKey"], value["challengePepper"]]
ids = [value["signing"]["kid"]]
assert 1 <= len(value["resultsKeys"]) <= 8
for entry in value["resultsKeys"]:
    assert set(entry) == {"kid", "key"}
    keys.append(entry["key"])
    ids.append(entry["kid"])
for kid in ids:
    parsed = uuid.UUID(kid)
    assert str(parsed) == kid and parsed.version == 4
decoded = [base64.b64decode(key, validate=True) for key in keys]
assert all(len(key) == 32 for key in decoded) and len(set(decoded)) == len(decoded) and len(set(ids)) == len(ids)
PY
for service in auth warehouse serving feedback; do
  export PGHOST=postgres PGPORT=5432 PGDATABASE="umir_$service" PGUSER="umir_$service"
  export PGPASSFILE="/run/umir-secrets/db/$service.pgpass"
  export PGSSLMODE=verify-full PGSSLROOTCERT=/run/pg-data/runtime/ca.crt
  flags=$(psql --no-psqlrc --quiet --tuples-only --no-align -c "SELECT rolsuper::text || chr(58) || rolbypassrls::text FROM pg_roles WHERE rolname = current_user")
  [[ $flags == false:false ]] || { printf "runtime role flags invalid: %s\n" "$service" >&2; exit 1; }
  if psql --no-psqlrc --quiet -c "CREATE TABLE public.w2_runtime_probe (id integer)" >/dev/null 2>&1; then
    printf "runtime DDL unexpectedly allowed: %s\n" "$service" >&2
    exit 1
  fi
  export PGUSER="umir_${service}_owner" PGPASSFILE="/run/umir-secrets/db/${service}-owner.pgpass"
  psql --no-psqlrc --quiet -c "CREATE TABLE public.w2_owner_probe (id integer)" >/dev/null
  psql --no-psqlrc --quiet -c "DROP TABLE public.w2_owner_probe" >/dev/null
done
printf "%s\n" "PostgreSQL runtime and owner-role checks passed"
'

docker run --rm --network "$network" --user 65532:65532 --entrypoint /bin/bash \
  --mount "type=volume,source=$secrets,target=/run/umir-secrets,readonly" \
  "$tools_image" -euc '
export REDISCLI_AUTH="$(cat /run/umir-secrets/redis-limiter/gateway.password)"
redis-cli --no-auth-warning --tls --cacert /run/umir-secrets/ca/ca.crt --user gateway -h redis-limiter ping | grep -qx PONG
redis-cli --no-auth-warning --tls --cacert /run/umir-secrets/ca/ca.crt --user gateway -h redis-limiter SET umir:gw:infra-check 1 EX 5 | grep -qx OK
set -f
for command in "SET umir:wh:infra-check 1" "FLUSHDB" "FLUSHALL" "CONFIG GET dir" "KEYS *" "DEBUG OBJECT umir:gw:infra-check" "SHUTDOWN NOSAVE"; do
  response=$(redis-cli --no-auth-warning --tls --cacert /run/umir-secrets/ca/ca.crt --user gateway -h redis-limiter $command 2>&1 || true)
  if [[ $response != *NOPERM* && $response != *"ERR unknown command"* && $response != *"DEBUG command not allowed"* ]]; then
    printf "Redis limiter response did not deny %s: %s\n" "${command%% *}" "$response" >&2
    exit 1
  fi
done
export REDISCLI_AUTH="$(cat /run/umir-secrets/redis-limiter/init.password)"
redis-cli --no-auth-warning --tls --cacert /run/umir-secrets/ca/ca.crt --user redis-init -h redis-limiter GET umir:limiter:generation | grep -Eq "^[a-f0-9]{32}$"
response=$(redis-cli --no-auth-warning --tls --cacert /run/umir-secrets/ca/ca.crt --user redis-init -h redis-limiter GET umir:gw:infra-check 2>&1 || true)
if [[ $response != *NOPERM* ]]; then
  printf "%s\n" "redis-init unexpectedly read a Gateway key" >&2
  exit 1
fi
printf "%s\n" "Redis ACL and marker checks passed"
'

docker run --rm --network "$network" --user 65532:65532 --entrypoint /bin/sh \
  --mount "type=volume,source=$secrets,target=/run/umir-secrets,readonly" \
  "$tools_image" -ec 'openssl s_client -brief -connect rabbitmq:5671 -CAfile /run/umir-secrets/ca/ca.crt -verify_hostname rabbitmq -verify_return_error </dev/null >/dev/null 2>&1'
printf '%s\n' 'RabbitMQ TLS name verification passed'
