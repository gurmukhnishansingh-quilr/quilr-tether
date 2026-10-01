#!/usr/bin/env sh
# Cross-compile static tether binaries for every supported platform into dist/.
# Needs only a Go toolchain; the binaries need nothing on the target machine.
set -eu
VERSION="${VERSION:-$(git describe --tags --always 2>/dev/null || echo dev)}"
LDFLAGS="-s -w -X main.version=${VERSION}"
mkdir -p dist
for target in windows/amd64 windows/arm64 darwin/amd64 darwin/arm64 linux/amd64 linux/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  ext=""; [ "$os" = windows ] && ext=".exe"
  out="dist/tether-${os}-${arch}${ext}"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath -ldflags "$LDFLAGS" -o "$out" ./cmd/tether
  echo "built $out"
done
