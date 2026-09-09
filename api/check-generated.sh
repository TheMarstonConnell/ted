#!/usr/bin/env bash
set -euo pipefail
cd "$(dirname "$0")/.."
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
cp api/openapi.yaml api/oapi-codegen.yaml go.mod "$tmp/"
(cd "$tmp" && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen@v2.5.1 --config oapi-codegen.yaml openapi.yaml)
if ! cmp -s api/generated.go "$tmp/generated.go"; then
  diff -u api/generated.go "$tmp/generated.go" || true
  echo 'Generated API is stale: run go generate ./api' >&2
  exit 1
fi
