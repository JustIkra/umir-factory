#!/bin/bash
set -euo pipefail

secret=/run/umir-secrets/init.password
generation=/run/umir-secrets/limiter-generation
[[ -r $secret && -r $generation ]] || { printf '%s\n' 'redis-init: required file missing' >&2; exit 1; }
export REDISCLI_AUTH="$(cat "$secret")"
redis-cli --tls --cacert /run/umir-secrets/ca.crt --user redis-init -h redis-limiter \
  SET umir:limiter:generation "$(cat "$generation")" >/dev/null
unset REDISCLI_AUTH
printf '%s\n' 'limiter continuity marker is ready'
