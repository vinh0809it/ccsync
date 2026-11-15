#!/usr/bin/env bash
# Build distributable ccsync binaries.
#
#   ./build/dist.sh            build browser-mode binaries for all platforms
#   ./build/dist.sh desktop    also build the Linux native-window binary
#
# Browser-mode binaries are pure Go (CGO_ENABLED=0): they run on any machine
# with git, opening the UI in the user's default browser (`ccsync gui`).
# The Linux desktop binary is a native window but needs webkit2gtk at runtime
# and can only be built on Linux.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v go >/dev/null || { echo "go not found in PATH"; exit 1; }
OUT=dist
rm -rf "$OUT"; mkdir -p "$OUT"
# Version: $VERSION, else the git tag (v1.2.3 -> 1.2.3), else "dev".
VERSION="${VERSION:-$(git describe --tags --exact-match 2>/dev/null | sed 's/^v//' || true)}"
VERSION="${VERSION:-dev}"
LDFLAGS="-s -w -X main.version=$VERSION"

# Embed the Windows icon (produces resource_windows_amd64.syso in repo root).
if command -v goversioninfo >/dev/null; then
  IFS=. read -r MAJ MIN PAT <<<"${VERSION%%-*}"
  [[ "$MAJ$MIN$PAT" =~ ^[0-9]+$ ]] || { MAJ=0; MIN=0; PAT=0; }
  goversioninfo -platform-specific -o resource.syso \
    -ver-major "$MAJ" -ver-minor "$MIN" -ver-patch "$PAT" \
    -product-ver-major "$MAJ" -product-ver-minor "$MIN" -product-ver-patch "$PAT" \
    -product-version "$VERSION" versioninfo.json
fi

build() { # os arch ext
  local os=$1 arch=$2 ext=${3:-}
  local name="ccsync-${VERSION}-${os}-${arch}${ext}"
  echo "  $name"
  CGO_ENABLED=0 GOOS=$os GOARCH=$arch go build -trimpath \
    -ldflags "$LDFLAGS" -o "$OUT/$name" .
}

echo "browser-mode binaries (run anywhere, no dependencies):"
build linux   amd64
build linux   arm64
build windows amd64 .exe
build darwin  amd64          # Intel Mac
build darwin  arm64          # Apple Silicon

# Clean up the windows-only resources so normal `go build` isn't affected.
rm -f resource*.syso

if [ "${1:-}" = "desktop" ]; then
  echo "linux native-window binary (needs webkit2gtk at runtime):"
  PKG_CONFIG_PATH="$PWD/build/pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}" \
    go build -tags desktop -trimpath -ldflags "$LDFLAGS" \
    -o "$OUT/ccsync-${VERSION}-linux-amd64-desktop" .
  echo "  ccsync-${VERSION}-linux-amd64-desktop"
fi

(cd "$OUT" && sha256sum ccsync-* > SHA256SUMS)

echo
echo "done -> $OUT/"
ls -la "$OUT"
