#!/bin/sh
# Assembles AirMock-<version>-<arch>.zip: the AirMock.app bundle template in
# this directory, with the goreleaser-built darwin binary dropped into
# Contents/MacOS/ alongside the launcher script, so the .app is fully
# self-contained — drag it into /Applications and go, no separate install
# step, no PATH entry, no admin privileges needed.
#
# This produces a real .app bundle structure (Info.plist/icns validated
# with Python's plistlib/Pillow, and the launcher's shell logic exercised
# on Linux with a stand-in browser), but the actual "double-click it on a
# Mac" experience has NOT been run on real macOS — there's no Mac hardware
# available to verify Gatekeeper behavior, code-signing prompts, etc. An
# unsigned/unnotarized .app will need a right-click-Open (or
# `xattr -d com.apple.quarantine`) the first time; proper distribution
# would need an Apple Developer ID to sign and notarize it, which is out of
# scope here.
set -e

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
REPO_ROOT="$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)"
DIST_DIR="$REPO_ROOT/dist"

ARCH="${AIRMOCK_ARCH:-arm64}"
VERSION="${1:-0.0.0-next}"

AIRMOCK_BIN=$(find "$DIST_DIR" -type f -name "airmock" -path "*darwin*${ARCH}*" | head -1)
if [ -z "$AIRMOCK_BIN" ]; then
  echo "Could not find a darwin/$ARCH airmock binary under $DIST_DIR — run goreleaser first, or set AIRMOCK_ARCH=amd64." >&2
  exit 1
fi

WORK_DIR=$(mktemp -d)
trap 'rm -rf "$WORK_DIR"' EXIT

cp -R "$SCRIPT_DIR/AirMock.app" "$WORK_DIR/AirMock.app"
cp "$AIRMOCK_BIN" "$WORK_DIR/AirMock.app/Contents/MacOS/airmock"
chmod +x "$WORK_DIR/AirMock.app/Contents/MacOS/airmock" "$WORK_DIR/AirMock.app/Contents/MacOS/airmock-launcher"

OUT="$SCRIPT_DIR/AirMock-$VERSION-$ARCH.zip"
rm -f "$OUT"
(cd "$WORK_DIR" && zip -qr "$OUT" "AirMock.app")

echo "Built: $OUT"
