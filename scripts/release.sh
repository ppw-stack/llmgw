#!/usr/bin/env bash
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

VERSION="${VERSION:-$(git describe --tags --always --dirty 2>/dev/null || echo dev)}"
COMMIT="${COMMIT:-$(git rev-parse --short HEAD 2>/dev/null || echo none)}"
DATE="${DATE:-$(date -u +%Y-%m-%dT%H:%M:%SZ)}"
LDFLAGS="-s -w -X main.version=${VERSION} -X main.commit=${COMMIT} -X main.date=${DATE}"

echo "==> building web assets"
make web

rm -rf dist build
mkdir -p dist

targets="darwin/arm64 darwin/amd64 linux/arm64 linux/amd64"
for target in $targets; do
  os="${target%/*}"
  arch="${target#*/}"
  name="llmgw_${VERSION}_${os}_${arch}"
  out="build/${name}"
  echo "==> ${os}/${arch}"
  mkdir -p "$out"
  GOOS="$os" GOARCH="$arch" CGO_ENABLED=0 \
    go build -trimpath -ldflags "$LDFLAGS" -o "$out/llmgw" ./cmd/llmgw
  cp config.example.yaml "$out/"
  cp README.md "$out/"
  tar -czf "dist/${name}.tar.gz" -C "$out" .
done

if command -v shasum >/dev/null 2>&1; then
  (cd dist && shasum -a 256 ./*.tar.gz > checksums.txt)
else
  (cd dist && sha256sum ./*.tar.gz > checksums.txt)
fi

rm -rf build
echo "==> artifacts"
ls -lh dist
