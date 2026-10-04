#!/bin/sh
# Idempotent Elasticsearch setup for the development stack: the index template for message search,
# the first index behind the `messages` write alias, and the kibana_system password.
set -eu

ES="${ES_URL}"
AUTH="elastic:${ELASTIC_PASSWORD}"

# -f turns HTTP errors into a non-zero exit; -S prints the error on failure only.
call() {
  method=$1
  path=$2
  shift 2
  curl -sS -f -u "$AUTH" -X "$method" -H 'Content-Type: application/json' "$ES$path" "$@" >/dev/null
}

echo "elasticsearch-init: index template messenger-messages"
call PUT /_index_template/messenger-messages --data-binary @/init/messages-template.json

if curl -s -o /dev/null -f -u "$AUTH" -I "$ES/_alias/messages"; then
  echo "elasticsearch-init: alias messages already exists"
else
  echo "elasticsearch-init: creating messages-000001 behind the alias messages"
  call PUT /messages-000001 --data '{"aliases":{"messages":{"is_write_index":true}}}'
fi

if [ -n "${KIBANA_PASSWORD:-}" ]; then
  echo "elasticsearch-init: kibana_system password"
  call POST /_security/user/kibana_system/_password --data "{\"password\":\"${KIBANA_PASSWORD}\"}"
fi

echo "elasticsearch-init: done"
