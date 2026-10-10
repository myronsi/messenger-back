#!/bin/sh
# Idempotent Elasticsearch setup for the development stack: the kibana_system password. The message index
# (template, versioned index, alias) belongs to the worker, which sets it up on start (internal/search).
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

# Stacks created before the worker owned the index have this template and index; they go.
curl -sS -o /dev/null -u "$AUTH" -X DELETE "$ES/_index_template/messenger-messages" || true
curl -sS -o /dev/null -u "$AUTH" -X DELETE "$ES/messages-000001" || true

if [ -n "${KIBANA_PASSWORD:-}" ]; then
  echo "elasticsearch-init: kibana_system password"
  call POST /_security/user/kibana_system/_password --data "{\"password\":\"${KIBANA_PASSWORD}\"}"
fi

echo "elasticsearch-init: done"
