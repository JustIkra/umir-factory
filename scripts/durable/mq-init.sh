#!/bin/sh
set -eu

node=rabbit@rabbitmq
definitions=${1:-/var/lib/rabbitmq/runtime/definitions.json}
if rabbitmqctl -q -n "$node" list_users | awk 'NR > 1 {print $1}' | grep -Fxq guest; then
  rabbitmqctl -q -n "$node" delete_user guest
fi
rabbitmqctl -q -n "$node" import_definitions "$definitions"
printf '%s\n' 'RabbitMQ topology and users are ready'
