#!/bin/bash
set -euo pipefail
umask 077

root=/run/umir-secrets
owner_uid=65532
owner_gid=65532
service_names=(auth warehouse serving feedback)
service_key_names=(gateway-auth serving-auth warehouse-auth feedback-auth auth-warehouse auth-serving auth-gateway gateway-warehouse gateway-serving gateway-feedback serving-warehouse)

fail() {
  printf '%s\n' "secrets: $1" >&2
  exit 1
}

secure_dir() {
  install -d -o "$owner_uid" -g "$owner_gid" -m 0700 "$1"
}

secret_file() {
  local path=$1
  if [[ ! -e $path ]]; then
    openssl rand -hex 32 >"$path.tmp"
    chown "$owner_uid:$owner_gid" "$path.tmp"
    chmod 0600 "$path.tmp"
    mv "$path.tmp" "$path"
  fi
  [[ -f $path && ! -L $path ]] || fail "expected a regular secret file: ${path#"$root"}"
  [[ $(stat -c '%u:%g:%a' "$path") == "$owner_uid:$owner_gid:600" ]] || fail "unsafe ownership or mode: ${path#"$root"}"
  [[ -s $path ]] || fail "empty secret file: ${path#"$root"}"
}

for dir in ca postgres rabbitmq redis-limiter redis-cache config db amqp redis keys auth; do
  secure_dir "$root/$dir"
done
install -o "$owner_uid" -g "$owner_gid" -m 0600 /usr/local/share/durable/rabbitmq.conf "$root/rabbitmq/rabbitmq.conf"
install -o "$owner_uid" -g "$owner_gid" -m 0600 /usr/local/share/durable/pg_hba.conf "$root/postgres/pg_hba.conf"

for file in "$root/postgres/bootstrap-password"; do secret_file "$file"; done
for service in "${service_names[@]}"; do
  secret_file "$root/db/$service.password"
  secret_file "$root/db/${service}-owner.password"
  secret_file "$root/amqp/$service.password"
done
secret_file "$root/amqp/gateway.password"
secret_file "$root/amqp/gateway.password"
for file in "$root/amqp/mq-init.password" "$root/amqp/ops.password" "$root/redis-limiter/gateway.password" "$root/redis-limiter/init.password" "$root/redis-limiter/health-password" "$root/redis-cache/warehouse.password" "$root/redis-cache/health-password"; do
  secret_file "$file"
done
for name in "${service_key_names[@]}"; do
  secret_file "$root/keys/$name"
done
for file in "$root/keys/FEEDBACK_DIGEST_KEY" "$root/keys/GATEWAY_ADMISSION_KEY" "$root/keys/GATEWAY_LIVE_CURSOR_KEY" "$root/auth/bootstrap-password"; do
  secret_file "$file"
done

python3 - /run/auth-owner-secrets/owner.json <<'PY'
import base64
import json
import os
import secrets
import sys
import uuid

path = sys.argv[1]
if not os.path.exists(path):
    results = {"kid": str(uuid.uuid4()), "key": base64.b64encode(secrets.token_bytes(32)).decode()}
    value = {
        "version": 2,
        "signing": {"kid": str(uuid.uuid4()), "seed": base64.b64encode(secrets.token_bytes(32)).decode()},
        "resultsKeys": [results],
        "challengeKey": base64.b64encode(secrets.token_bytes(32)).decode(),
        "challengePepper": base64.b64encode(secrets.token_bytes(32)).decode(),
    }
    descriptor = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as stream:
        json.dump(value, stream, separators=(",", ":"))
        stream.write("\n")
    os.chown(path, 65532, 65532)
    os.chmod(path, 0o600)
else:
    info = os.stat(path, follow_symlinks=False)
    if not os.path.isfile(path) or info.st_uid != 65532 or info.st_gid != 65532 or info.st_mode & 0o777 != 0o600 or info.st_size > 16384:
        raise SystemExit("unsafe Auth owner key file")
    value = json.load(open(path, encoding="utf-8"))
    if set(value) != {"version", "signing", "resultsKeys", "challengeKey", "challengePepper"} or value["version"] != 2 or not 1 <= len(value["resultsKeys"]) <= 8:
        raise SystemExit("invalid Auth owner key file")
PY

if [[ ! -e $root/config/limiter-generation ]]; then
  openssl rand -hex 16 >"$root/config/limiter-generation.tmp"
  chown "$owner_uid:$owner_gid" "$root/config/limiter-generation.tmp"
  chmod 0600 "$root/config/limiter-generation.tmp"
  mv "$root/config/limiter-generation.tmp" "$root/config/limiter-generation"
fi
[[ $(stat -c '%u:%g:%a' "$root/config/limiter-generation") == "$owner_uid:$owner_gid:600" ]] || fail 'unsafe limiter generation ownership or mode'

if [[ ! -s $root/ca/ca.crt || ! -s $root/ca/ca.key ]]; then
  openssl req -x509 -newkey rsa:3072 -sha256 -days 3650 -nodes \
    -subj '/CN=Umir Durable Private CA' -keyout "$root/ca/ca.key" -out "$root/ca/ca.crt" >/dev/null 2>&1
fi
chown "$owner_uid:$owner_gid" "$root/ca/ca.key" "$root/ca/ca.crt"
chmod 0600 "$root/ca/ca.key" "$root/ca/ca.crt"

issue_leaf() {
  local name=$1 sans=$2 dir="$root/$1"
  if [[ ! -s $dir/$name.crt || ! -s $dir/$name.key ]]; then
    openssl req -new -newkey rsa:3072 -sha256 -nodes -subj "/CN=$name" \
      -keyout "$dir/$name.key" -out "$dir/$name.csr" >/dev/null 2>&1
    printf 'subjectAltName=%s\nextendedKeyUsage=serverAuth\n' "$sans" >"$dir/$name.ext"
    openssl x509 -req -sha256 -days 365 -in "$dir/$name.csr" \
      -CA "$root/ca/ca.crt" -CAkey "$root/ca/ca.key" -CAcreateserial \
      -extfile "$dir/$name.ext" -out "$dir/$name.crt" >/dev/null 2>&1
    rm -f "$dir/$name.csr" "$dir/$name.ext"
  fi
  chown "$owner_uid:$owner_gid" "$dir/$name.crt" "$dir/$name.key"
  chmod 0600 "$dir/$name.crt" "$dir/$name.key"
}

issue_leaf postgres 'DNS:postgres'
issue_leaf rabbitmq 'DNS:rabbitmq'
issue_leaf redis-limiter 'DNS:redis-limiter'
issue_leaf redis-cache 'DNS:redis-cache'

for service in "${service_names[@]}"; do
  runtime_user="umir_$service"
  owner_user="umir_${service}_owner"
  runtime_password=$(cat "$root/db/$service.password")
  owner_password=$(cat "$root/db/${service}-owner.password")
  printf 'postgres:5432:umir_%s:%s:%s\n' "$service" "$runtime_user" "$runtime_password" >"$root/db/$service.pgpass"
  printf 'postgres:5432:umir_%s:%s:%s\n' "$service" "$owner_user" "$owner_password" >"$root/db/${service}-owner.pgpass"
  chmod 0600 "$root/db/$service.pgpass" "$root/db/${service}-owner.pgpass"
  chown "$owner_uid:$owner_gid" "$root/db/$service.pgpass" "$root/db/${service}-owner.pgpass"
  printf 'postgres://%s:%s@postgres:5432/umir_%s?sslmode=verify-full&sslrootcert=/run/umir-secrets/ca/ca.crt' \
    "$runtime_user" "$runtime_password" "$service" >"$root/db/$service.url"
  printf 'postgres://%s:%s@postgres:5432/umir_%s?sslmode=verify-full&sslrootcert=/run/umir-secrets/ca/ca.crt' \
    "$owner_user" "$owner_password" "$service" >"$root/db/${service}-owner.url"
  chmod 0600 "$root/db/$service.url" "$root/db/${service}-owner.url"
  chown "$owner_uid:$owner_gid" "$root/db/$service.url" "$root/db/${service}-owner.url"
done

for service in auth warehouse serving feedback gateway; do
  pass_file="$root/amqp/$service.password"
  pass=$(cat "$pass_file")
  user="umir_$service"
  printf 'amqps://%s:%s@rabbitmq:5671/umir' "$user" "$pass" >"$root/amqp/$service.url"
  chmod 0600 "$root/amqp/$service.url"
  chown "$owner_uid:$owner_gid" "$root/amqp/$service.url"
done
mq_init_pass=$(cat "$root/amqp/mq-init.password")
printf 'amqps://umir_mq_init:%s@rabbitmq:5671/umir' "$mq_init_pass" >"$root/amqp/mq-init.url"
chmod 0600 "$root/amqp/mq-init.url"
chown "$owner_uid:$owner_gid" "$root/amqp/mq-init.url"

redis_gateway_pass=$(cat "$root/redis-limiter/gateway.password")
redis_warehouse_pass=$(cat "$root/redis-cache/warehouse.password")
printf 'rediss://gateway:%s@redis-limiter:6379/0' "$redis_gateway_pass" >"$root/redis/gateway.url"
printf 'rediss://warehouse:%s@redis-cache:6379/0' "$redis_warehouse_pass" >"$root/redis/warehouse.url"
chmod 0600 "$root/redis/gateway.url" "$root/redis/warehouse.url"
chown "$owner_uid:$owner_gid" "$root/redis/gateway.url" "$root/redis/warehouse.url"

python3 - "$root" <<'PY'
import base64
import hashlib
import json
import os
import secrets
import sys

root = sys.argv[1]

def read(path):
    with open(os.path.join(root, path), "rb") as stream:
        return stream.read().rstrip(b"\n").decode()

def rabbit_hash(password, username):
    salt_path = os.path.join(root, "amqp", username + ".salt")
    if not os.path.exists(salt_path):
        with open(salt_path, "wb") as stream:
            stream.write(secrets.token_bytes(4))
        os.chmod(salt_path, 0o600)
        os.chown(salt_path, 65532, 65532)
    with open(salt_path, "rb") as stream:
        salt = stream.read()
    digest = hashlib.sha256(salt + password.encode()).digest()
    return base64.b64encode(salt + digest).decode()

users = [
    ("umir_auth", "amqp/auth.password"),
    ("umir_warehouse", "amqp/warehouse.password"),
    ("umir_serving", "amqp/serving.password"),
    ("umir_feedback", "amqp/feedback.password"),
    ("umir_gateway", "amqp/gateway.password"),
    ("umir_mq_init", "amqp/mq-init.password"),
    ("umir_mq_ops", "amqp/ops.password"),
]
permissions = {
    "umir_auth": (r"^$", r"^umir\.(auth|tasks|dlx)$", r"^auth\.mail$"),
    "umir_warehouse": (r"^$", r"^umir\.events$", r"^$"),
    "umir_serving": (r"^serving\.auth\.[A-Za-z0-9_-]+$", r"^serving\.auth\.[A-Za-z0-9_-]+$", r"^(umir\.auth|serving\.auth\.[A-Za-z0-9_-]+)$"),
    "umir_feedback": (r"^$", r"^umir\.(tasks|dlx)$", r"^feedback\.(smtp|telegram|auth)$"),
    "umir_gateway": (r"^gateway\.(live|auth)\.[A-Za-z0-9_-]+$", r"^gateway\.(live|auth)\.[A-Za-z0-9_-]+$", r"^(umir\.(events|auth)|gateway\.(live|auth)\.[A-Za-z0-9_-]+)$"),
    "umir_mq_init": (r".*", r".*", r".*"),
    "umir_mq_ops": (r".*", r".*", r".*"),
}
exchanges = [
    {"name": name, "vhost": "umir", "type": kind, "durable": True, "auto_delete": False, "internal": False, "arguments": {}}
    for name, kind in (("umir.events", "topic"), ("umir.auth", "topic"), ("umir.tasks", "direct"), ("umir.dlx", "direct"))
]
definitions = {
    "rabbit_version": "4.3",
    "users": [{"name": name, "password_hash": rabbit_hash(read(path), name), "hashing_algorithm": "rabbit_password_hashing_sha256", "tags": "management" if name == "umir_mq_ops" else ""} for name, path in users],
    "vhosts": [{"name": "umir", "description": "Umir durable services", "tags": ""}],
    "permissions": [{"user": user, "vhost": "umir", "configure": config, "write": write, "read": read} for user, (config, write, read) in permissions.items()],
    "topic_permissions": [],
    "exchanges": exchanges,
    "queues": [],
    "bindings": [],
}
for queue in ("auth.mail", "feedback.smtp", "feedback.telegram"):
    for suffix, args in (
        ("", {"x-queue-type": "quorum", "x-delivery-limit": 5, "x-overflow": "reject-publish", "x-dead-letter-exchange": "umir.dlx", "x-dead-letter-routing-key": queue + ".dlq", "x-dead-letter-strategy": "at-least-once"}),
        (".retry", {"x-queue-type": "quorum", "x-message-ttl": 30000, "x-overflow": "reject-publish", "x-dead-letter-exchange": "umir.dlx", "x-dead-letter-routing-key": queue, "x-dead-letter-strategy": "at-least-once"}),
        (".dlq", {"x-queue-type": "quorum"}),
    ):
        name = queue + suffix
        definitions["queues"].append({"name": name, "vhost": "umir", "durable": True, "auto_delete": False, "arguments": args})
        source = "umir.tasks" if suffix == "" else "umir.dlx"
        definitions["bindings"].append({"source": source, "vhost": "umir", "destination": name, "destination_type": "queue", "routing_key": name, "arguments": {}})

for name in ("auth.mail", "feedback.smtp", "feedback.telegram"):
    definitions["bindings"].append({"source": "umir.tasks", "vhost": "umir", "destination": name, "destination_type": "queue", "routing_key": name, "arguments": {}})

path = os.path.join(root, "rabbitmq", "definitions.json")
with open(path, "w", encoding="utf-8") as stream:
    json.dump(definitions, stream, separators=(",", ":"))
    stream.write("\n")

redis_users = {
    "health": ("redis-limiter/health-password", "~*", "+ping"),
    "redis-init": ("redis-limiter/init.password", "~umir:limiter:generation ~umir:limiter:strict_since", "+ping +get +set +del"),
    "gateway": ("redis-limiter/gateway.password", "~umir:gw:* ~umir:limiter:generation ~umir:limiter:strict_since", "+hello +client|setinfo +client|setname +ping +get +set +del +incr +exists +pttl +pexpire +time +zadd +zrem +zrangebyscore +zscore +zcard +hset +hget +hvals +hdel +evalsha +script|load"),
}
limiter = ["user default off"]
for user, (path, keys, commands) in redis_users.items():
    password_hash = hashlib.sha256(read(path).encode()).hexdigest()
    limiter.append(f"user {user} on #{password_hash} resetkeys nocommands {keys} +auth {commands}")
with open(os.path.join(root, "redis-limiter", "users.acl"), "w", encoding="utf-8") as stream:
    stream.write("\n".join(limiter) + "\n")

cache_hash = hashlib.sha256(read("redis-cache/health-password").encode()).hexdigest()
warehouse_hash = hashlib.sha256(read("redis-cache/warehouse.password").encode()).hexdigest()
with open(os.path.join(root, "redis-cache", "users.acl"), "w", encoding="utf-8") as stream:
    stream.write("\n".join(("user default off", f"user health on #{cache_hash} resetkeys nocommands ~* +auth +ping", f"user warehouse on #{warehouse_hash} resetkeys nocommands ~umir:wh:* +auth +hello +client|setinfo +client|setname +ping +get +set +del +exists +pttl +pexpire +time +zadd +zrem +zrangebyscore +zscore +zcard +hset +hget +hvals +hdel +evalsha +script|load")) + "\n")

feedback_queue = "feedback.auth"
for suffix, args in (
    ("", {"x-queue-type": "quorum", "x-delivery-limit": 5, "x-overflow": "reject-publish", "x-dead-letter-exchange": "umir.dlx", "x-dead-letter-routing-key": feedback_queue + ".dlq", "x-dead-letter-strategy": "at-least-once"}),
    (".retry", {"x-queue-type": "quorum", "x-message-ttl": 30000, "x-overflow": "reject-publish", "x-dead-letter-exchange": "umir.dlx", "x-dead-letter-routing-key": feedback_queue, "x-dead-letter-strategy": "at-least-once"}),
    (".dlq", {"x-queue-type": "quorum"}),
):
    name = feedback_queue + suffix
    definitions["queues"].append({"name": name, "vhost": "umir", "durable": True, "auto_delete": False, "arguments": args})
    definitions["bindings"].append({"source": "umir.dlx" if suffix else "umir.auth", "vhost": "umir", "destination": name, "destination_type": "queue", "routing_key": name, "arguments": {}})

for event in ("auth.restaurant.deleted", "auth.organization.deleted"):
    definitions["bindings"].append({"source": "umir.auth", "vhost": "umir", "destination": "feedback.auth", "destination_type": "queue", "routing_key": event, "arguments": {}})

feedback_definitions = os.path.join(root, "rabbitmq", "definitions-feedback.json")
with open(feedback_definitions, "w", encoding="utf-8") as stream:
    json.dump(definitions, stream, separators=(",", ":"))
    stream.write("\n")
PY

install -d -o 999 -g 999 -m 0700 /run/pg-data/runtime /run/mq-data/runtime
chown 999:999 /run/pg-data /run/mq-data
chmod 0711 /run/pg-data
chmod 0700 /run/mq-data
chmod 0755 /run/pg-data/runtime
install -o 999 -g 999 -m 0600 "$root/postgres/postgres.key" /run/pg-data/runtime/postgres.key
install -o 999 -g 999 -m 0644 "$root/postgres/postgres.crt" /run/pg-data/runtime/postgres.crt
install -o 999 -g 999 -m 0644 "$root/ca/ca.crt" /run/pg-data/runtime/ca.crt
install -o 999 -g 999 -m 0600 "$root/postgres/bootstrap-password" /run/pg-data/runtime/bootstrap-password
install -o 999 -g 999 -m 0600 "$root/postgres/pg_hba.conf" /run/pg-data/runtime/pg_hba.conf
bootstrap_pass=$(cat "$root/postgres/bootstrap-password")
printf 'postgres:5432:*:umir_bootstrap:%s\n' "$bootstrap_pass" >/run/pg-data/runtime/pgpass
chown "$owner_uid:$owner_gid" /run/pg-data/runtime/pgpass
chmod 0600 /run/pg-data/runtime/pgpass

install -o 999 -g 999 -m 0644 "$root/rabbitmq/rabbitmq.conf" /run/mq-data/runtime/rabbitmq.conf
install -o 999 -g 999 -m 0644 "$root/rabbitmq/rabbitmq.crt" /run/mq-data/runtime/rabbitmq.crt
install -o 999 -g 999 -m 0600 "$root/rabbitmq/rabbitmq.key" /run/mq-data/runtime/rabbitmq.key
install -o 999 -g 999 -m 0644 "$root/ca/ca.crt" /run/mq-data/runtime/ca.crt
install -o 999 -g 999 -m 0600 "$root/rabbitmq/definitions.json" /run/mq-data/runtime/definitions.json
install -o 999 -g 999 -m 0600 "$root/rabbitmq/definitions-feedback.json" /run/mq-data/runtime/definitions-feedback.json

install -d -o 999 -g 999 -m 0700 /run/redis-limiter-data
chown -R 999:999 /run/redis-limiter-data
install -d -o "$owner_uid" -g "$owner_gid" -m 0700 "$root/redis-limiter/runtime" "$root/redis-cache/runtime" "$root/redis-init/runtime"
for service in redis-limiter redis-cache; do
  install -o "$owner_uid" -g "$owner_gid" -m 0600 "$root/$service/$service.crt" "$root/$service/runtime/redis.crt"
  install -o "$owner_uid" -g "$owner_gid" -m 0600 "$root/$service/$service.key" "$root/$service/runtime/redis.key"
  install -o "$owner_uid" -g "$owner_gid" -m 0600 "$root/$service/users.acl" "$root/$service/runtime/users.acl"
  install -o "$owner_uid" -g "$owner_gid" -m 0600 "$root/$service/health-password" "$root/$service/runtime/health-password"
  install -o "$owner_uid" -g "$owner_gid" -m 0600 "$root/ca/ca.crt" "$root/$service/runtime/ca.crt"
done
install -o "$owner_uid" -g "$owner_gid" -m 0600 "$root/redis-limiter/init.password" "$root/redis-init/runtime/init.password"
install -o "$owner_uid" -g "$owner_gid" -m 0600 "$root/config/limiter-generation" "$root/redis-init/runtime/limiter-generation"
install -o "$owner_uid" -g "$owner_gid" -m 0600 "$root/ca/ca.crt" "$root/redis-init/runtime/ca.crt"

chown -R "$owner_uid:$owner_gid" "$root"
find "$root" -type d -exec chmod 0700 {} +
find "$root" -type f -exec chmod 0600 {} +
chown -R 999:999 "$root/redis-limiter/runtime" "$root/redis-cache/runtime"
chmod 0755 "$root/redis-limiter/runtime" "$root/redis-cache/runtime"
chmod 0600 "$root/redis-limiter/runtime/redis.key" "$root/redis-limiter/runtime/users.acl" "$root/redis-limiter/runtime/health-password"
chmod 0644 "$root/redis-limiter/runtime/redis.crt" "$root/redis-limiter/runtime/ca.crt"
chmod 0600 "$root/redis-cache/runtime/redis.key" "$root/redis-cache/runtime/users.acl" "$root/redis-cache/runtime/health-password"
chmod 0644 "$root/redis-cache/runtime/redis.crt" "$root/redis-cache/runtime/ca.crt"
chown -R "$owner_uid:$owner_gid" "$root/redis-init/runtime"
chmod 0700 "$root/redis-init/runtime"
chmod 0600 "$root/redis-init/runtime/init.password" "$root/redis-init/runtime/limiter-generation" "$root/redis-init/runtime/ca.crt"
printf '%s\n' 'durable infrastructure secrets are ready'
