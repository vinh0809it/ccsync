#!/usr/bin/env bash
# Install the ccsync Linux desktop app for the current user: copies the
# native-window binary, the icon, and a .desktop launcher into ~/.local so
# ccsync shows up in the application menu with its icon. No root needed.
#
# Usage: ./packaging/linux/install.sh [path-to-desktop-binary]
set -euo pipefail
cd "$(dirname "$0")/../.."

BIN="${1:-}"
if [ -z "$BIN" ]; then
  BIN=$(ls dist/ccsync-*-linux-amd64-desktop 2>/dev/null | head -1 || true)
fi
[ -n "$BIN" ] && [ -f "$BIN" ] || { echo "desktop binary not found; run: ./build/dist.sh desktop"; exit 1; }

APPS="$HOME/.local/share/applications"
ICONS="$HOME/.local/share/icons/hicolor/256x256/apps"
BINDIR="$HOME/.local/bin"
mkdir -p "$APPS" "$ICONS" "$BINDIR"

install -m755 "$BIN" "$BINDIR/ccsync"
install -m644 assets/icon-256.png "$ICONS/ccsync.png"

cat > "$APPS/ccsync.desktop" <<EOF
[Desktop Entry]
Type=Application
Name=ccsync
Comment=Sync Claude Code sessions via an encrypted git store
Exec=$BINDIR/ccsync gui
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
