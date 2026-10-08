#!/usr/bin/env bash
# Records HARs with Playwright in Docker. Output: validator/testdata/har/*.har
set -euo pipefail
GO_IMAGE=golang:1.26.8
PW_VERSION=1.64.0
PW_IMAGE=mcr.microsoft.com/playwright:v${PW_VERSION}-noble

here=$(cd "$(dirname "$0")" && pwd)
har=$(dirname "$here")
responses=$(dirname "$har")/responses
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT
chmod 755 "$tmp"

docker run --rm --user "$(id -u):$(id -g)" -e HOME=/tmp -e GOCACHE=/tmp/gocache -e CGO_ENABLED=0 \
  -v "$here/server:/src:ro" -v "$tmp:/out" -w /tmp "$GO_IMAGE" \
  sh -c 'cp /src/main.go /tmp/ && go mod init recorder >/dev/null 2>&1 && go build -trimpath -o /out/server main.go'

cp "$here/record.mjs" "$tmp/"
docker run --rm --init --ipc=host --user "$(id -u):$(id -g)" -e HOME=/tmp \
  -v "$tmp:/work" -v "$responses:/responses:ro" -v "$har:/har" -w /work "$PW_IMAGE" \
  bash -c "npm init -y >/dev/null && npm i --no-audit --no-fund playwright@${PW_VERSION} >/dev/null \
    && (/work/server /responses & trap 'kill \$!' EXIT; sleep 1; node record.mjs /har)"
