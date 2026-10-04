#!/bin/sh
# Creates every bucket listed in S3_BUCKETS (comma separated) unless it already exists.
set -eu

for bucket in $(echo "$S3_BUCKETS" | tr ',' ' '); do
  if aws --endpoint-url "$S3_ENDPOINT" s3api head-bucket --bucket "$bucket" >/dev/null 2>&1; then
    echo "object-storage-init: bucket $bucket already exists"
  else
    echo "object-storage-init: creating bucket $bucket"
    aws --endpoint-url "$S3_ENDPOINT" s3api create-bucket --bucket "$bucket" >/dev/null
  fi
done
