#!/usr/bin/env bash
set -euo pipefail

# Build the cmd/app/main.go into a binary named "prod" at the repo root

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="${SCRIPT_DIR%/scripts}"
cd "$REPO_ROOT"

if ! command -v go >/dev/null 2>&1; then
    echo "Error: Go toolchain not found in PATH" >&2
    exit 1
fi

# Allow overrides via environment variables
: "${GOOS:=}"
: "${GOARCH:=}"
: "${CGO_ENABLED:=0}"

OUT_BIN="$REPO_ROOT/prod"
PKG_PATH="./cmd/app"

echo "Building $PKG_PATH -> $OUT_BIN"
GO111MODULE=on CGO_ENABLED="$CGO_ENABLED" GOOS="$GOOS" GOARCH="$GOARCH" \
  go build -trimpath -ldflags "-s -w" -o "$OUT_BIN" "$PKG_PATH"

echo "Done: $OUT_BIN"


