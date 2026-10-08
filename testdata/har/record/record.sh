#!/usr/bin/env bash
# Records HARs with Playwright in Docker. Output: validator/testdata/har/*.har
set -euo pipefail
# Tag for humans, digest (multi-arch index) for reproducibility.
GO_IMAGE=golang:1.26.8@sha256:eb36c1664dd974cde625f736e02c204383deebe03977365caaec5bf49f794348
PW_VERSION=1.64.0
PW_IMAGE=mcr.microsoft.com/playwright:v${PW_VERSION}-noble@sha256:06a9939e57531807f8d5fd76ce44b53165ffb7d7501d87ab10e285c20b1e971f

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
  bash -c "wait_for_port() {
      for _ in \$(seq 50); do (exec 3<>/dev/tcp/127.0.0.1/8080) 2>/dev/null && return; sleep 0.2; done
      echo 'recorder server did not listen on 127.0.0.1:8080 within 10s' >&2; exit 1
    }
    npm init -y >/dev/null && npm i --no-audit --no-fund playwright@${PW_VERSION} >/dev/null \
    && (/work/server /responses & trap 'kill \$!' EXIT; wait_for_port; node record.mjs /har)"
