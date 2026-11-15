#!/usr/bin/env bash
# Build the native-window app (no terminal, no browser) for the OS this
# runs on, packaged for double-click use, into dist/:
#
#   Linux    ccsync-<v>-linux-<arch>-desktop.tar.gz   (binary + ./install.sh)
#   macOS    ccsync-<v>-macos-<arch>.zip               (ccsync.app)
#   Windows  ccsync-<v>-windows-<arch>-desktop.exe
#
# Native windows use the OS web view through CGO, so each OS builds its
# own: run this on that OS (the release workflow does all three). Runtime
# needs: webkit2gtk-4.1 (Linux), WebView2 (Windows 10/11 ship it), nothing
# extra on macOS.
set -euo pipefail
cd "$(dirname "$0")/.."

VERSION="${VERSION:-$(git describe --tags --exact-match 2>/dev/null | sed 's/^v//' || true)}"
VERSION="${VERSION:-dev}"
LDFLAGS="-s -w -X main.version=$VERSION"
ARCH=$(go env GOARCH)
OUT=dist
mkdir -p "$OUT"
STAGE=$(mktemp -d)
trap 'rm -rf "$STAGE"' EXIT

case "$(uname -s)" in
Linux)
  name="ccsync-$VERSION-linux-$ARCH-desktop"
  mkdir -p "$STAGE/$name"
  # build/pkgconfig maps webkit2gtk-4.0 (what webview_go asks for) to 4.1.
  PKG_CONFIG_PATH="$PWD/build/pkgconfig${PKG_CONFIG_PATH:+:$PKG_CONFIG_PATH}" \
    CGO_ENABLED=1 go build -tags desktop -trimpath -ldflags "$LDFLAGS" -o "$STAGE/$name/ccsync" .
  cp packaging/linux/install.sh "$STAGE/$name/install.sh"
  cp assets/icon-256.png "$STAGE/$name/icon.png"
  tar -C "$STAGE" -czf "$OUT/$name.tar.gz" "$name"
  echo "$OUT/$name.tar.gz"
  ;;
Darwin)
  app="$STAGE/ccsync.app"
  mkdir -p "$app/Contents/MacOS" "$app/Contents/Resources"
  CGO_ENABLED=1 go build -tags desktop -trimpath -ldflags "$LDFLAGS" -o "$app/Contents/MacOS/ccsync" .
  sed "s/@VERSION@/$VERSION/g" packaging/macos/Info.plist > "$app/Contents/Info.plist"
  iconset="$STAGE/ccsync.iconset"
  mkdir -p "$iconset"
  for size in 16 32 128 256; do
    sips -z $size $size assets/icon-256.png --out "$iconset/icon_${size}x${size}.png" >/dev/null
    sips -z $((size * 2)) $((size * 2)) assets/icon-256.png --out "$iconset/icon_${size}x${size}@2x.png" >/dev/null
  done
  iconutil -c icns "$iconset" -o "$app/Contents/Resources/ccsync.icns"
  name="ccsync-$VERSION-macos-$ARCH"
  ditto -c -k --keepParent "$app" "$OUT/$name.zip"
  echo "$OUT/$name.zip"
  ;;
MINGW* | MSYS* | CYGWIN*)
  name="ccsync-$VERSION-windows-$ARCH-desktop.exe"
  if command -v goversioninfo >/dev/null; then
    goversioninfo -64 -o resource.syso -product-version "$VERSION" build/versioninfo.json
    trap 'rm -rf "$STAGE" resource.syso' EXIT
  fi
  # windowsgui: no console window behind the app.
  CGO_ENABLED=1 go build -tags desktop -trimpath -ldflags "$LDFLAGS -H=windowsgui" -o "$OUT/$name" .
  echo "$OUT/$name"
  ;;
*)
  echo "unsupported OS: $(uname -s)" >&2
  exit 1
  ;;
esac
