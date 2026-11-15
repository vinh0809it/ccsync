#!/usr/bin/env bash
# Install the ccsync Linux desktop app for the current user: copies the
# native-window binary, the icon, and a .desktop launcher into ~/.local so
# ccsync shows up in the application menu with its icon. No root needed.
#
# Usage: ./install.sh                    (from an unpacked release archive)
#        ./packaging/linux/install.sh [path-to-desktop-binary]   (from the repo)
set -euo pipefail
HERE=$(cd "$(dirname "$0")" && pwd)

if [ -f "$HERE/ccsync" ]; then
  # Unpacked release archive: binary and icon sit next to this script.
  BIN="$HERE/ccsync"
  ICON="$HERE/icon.png"
else
  # Repository checkout.
  cd "$HERE/../.."
  ICON=assets/icon-256.png
  BIN="${1:-}"
  if [ -z "$BIN" ]; then
    BIN=$(ls dist/ccsync-*-linux-*-desktop 2>/dev/null | head -1 || true)
  fi
fi
[ -n "$BIN" ] && [ -f "$BIN" ] || { echo "desktop binary not found; run: ./build/desktop.sh"; exit 1; }

APPS="$HOME/.local/share/applications"
ICONS="$HOME/.local/share/icons/hicolor/256x256/apps"
BINDIR="$HOME/.local/bin"
mkdir -p "$APPS" "$ICONS" "$BINDIR"

install -m755 "$BIN" "$BINDIR/ccsync"
install -m644 "$ICON" "$ICONS/ccsync.png"

cat > "$APPS/ccsync.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=ccsync
Comment=Sync Claude Code sessions via an encrypted git store
Exec=$BINDIR/ccsync
Icon=ccsync
Terminal=false
Categories=Development;Utility;
EOF

command -v update-desktop-database >/dev/null && update-desktop-database "$APPS" 2>/dev/null || true
command -v gtk-update-icon-cache >/dev/null && gtk-update-icon-cache "$HOME/.local/share/icons/hicolor" 2>/dev/null || true

echo "Installed:"
echo "  binary   $BINDIR/ccsync"
echo "  icon     $ICONS/ccsync.png"
echo "  launcher $APPS/ccsync.desktop"
echo
echo "Search 'ccsync' in your app menu, or run: ccsync gui"
echo "(ensure $BINDIR is on your PATH for the CLI)"
