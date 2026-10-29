#!/usr/bin/env bash
# Builds release binaries into dist/ with checksums.txt. Usage: scripts/release.sh [version]
set -euo pipefail
cd "$(dirname "$0")/.."
version="${1:-$(cat VERSION)}"
rm -rf dist && mkdir dist
for target in linux/amd64 linux/arm64 darwin/amd64 darwin/arm64 windows/amd64 windows/arm64; do
  os="${target%/*}"; arch="${target#*/}"
  ext=""; [ "$os" = windows ] && ext=".exe"
  CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" go build -trimpath \
    -ldflags "-s -w -X main.version=$version" \
    -o "dist/agent-ledger-$os-$arch$ext" ./cmd/agent-ledger
done
(cd dist && if command -v sha256sum >/dev/null; then sha256sum agent-ledger-*; else shasum -a 256 agent-ledger-*; fi) >dist/checksums.txt
echo "built v$version:"; ls dist
