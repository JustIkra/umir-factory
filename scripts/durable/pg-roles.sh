#!/bin/bash
set -euo pipefail
umask 077

root=/run/umir-secrets
sql=/tmp/pg-roles.sql

python3 - "$root" "$sql" <<'PY'
import os
import sys

root, path = sys.argv[1:]

def read(name):
    with open(os.path.join(root, name), "r", encoding="utf-8") as stream:
        return stream.read().rstrip("\n")

def literal(value):
    return "'" + value.replace("'", "''") + "'"

lines = ["\\set ON_ERROR_STOP on", "REVOKE ALL ON DATABASE postgres FROM PUBLIC;"]
for service in ("auth", "warehouse", "serving", "feedback"):
    owner = f"umir_{service}_owner"
    runtime = f"umir_{service}"
    database = f"umir_{service}"
    for role, password in ((owner, read(f"db/{service}-owner.password")), (runtime, read(f"db/{service}.password"))):
        lines.extend((
            f"SELECT format('CREATE ROLE %I LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS PASSWORD %L', {literal(role)}, {literal(password)}) WHERE NOT EXISTS (SELECT FROM pg_roles WHERE rolname = {literal(role)}) \\gexec",
            f"ALTER ROLE {role} LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOINHERIT NOBYPASSRLS PASSWORD {literal(password)};",
        ))
    lines.extend((
        f"SELECT format('CREATE DATABASE %I OWNER %I', {literal(database)}, {literal(owner)}) WHERE NOT EXISTS (SELECT FROM pg_database WHERE datname = {literal(database)}) \\gexec",
        f"ALTER DATABASE {database} OWNER TO {owner};",
        f"REVOKE ALL ON DATABASE {database} FROM PUBLIC;",
        f"GRANT CONNECT ON DATABASE {database} TO {runtime};",
        f"GRANT CONNECT, TEMPORARY ON DATABASE {database} TO {owner};",
        f"\\connect {database}",
        "REVOKE ALL ON SCHEMA public FROM PUBLIC;",
        f"REVOKE ALL ON SCHEMA public FROM {runtime};",
        f"GRANT USAGE ON SCHEMA public TO {runtime};",
        f"GRANT USAGE, CREATE ON SCHEMA public TO {owner};",
        f"ALTER DEFAULT PRIVILEGES FOR ROLE {owner} IN SCHEMA public GRANT SELECT, INSERT, UPDATE, DELETE, REFERENCES, TRIGGER ON TABLES TO {runtime};",
        f"ALTER DEFAULT PRIVILEGES FOR ROLE {owner} IN SCHEMA public GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO {runtime};",
        f"ALTER ROLE {runtime} SET statement_timeout = '5s';",
        f"ALTER ROLE {runtime} SET lock_timeout = '2s';",
        f"ALTER ROLE {runtime} SET idle_in_transaction_session_timeout = '60s';",
    ))
with open(path, "w", encoding="utf-8") as stream:
    stream.write("\n".join(lines) + "\n")
os.chmod(path, 0o600)
PY

psql --no-psqlrc --quiet --set=ON_ERROR_STOP=1 --file="$sql"
rm -f "$sql"
printf '%s\n' 'durable database roles are ready'
