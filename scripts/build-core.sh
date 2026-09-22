#!/bin/sh
set -eu
ROOT=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$ROOT"
export PATH="$ROOT/.tools/go/bin:$ROOT/.tools/bin:$PATH"
export GOPROXY="${GOPROXY:-https://goproxy.cn,direct}"
mkdir -p dist
go build -tags=cmfa,with_gvisor -trimpath -ldflags='-s -w' -o dist/jungo ./cmd/jungo
for ARCH in amd64 arm64; do
  CGO_ENABLED=0 GOOS=linux GOARCH="$ARCH" go build -tags=cmfa,with_gvisor -trimpath -ldflags='-s -w' -o "dist/jungo-linux-$ARCH" ./cmd/jungo
done
