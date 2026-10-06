#!/bin/sh
# Assembles AirMockSetup-<version>.exe from a goreleaser snapshot/release
# build. Run this AFTER `goreleaser release --snapshot --clean` (or a real
# release) has populated dist/ — it copies the two Windows binaries and the
# app icon in here, then invokes makensis.
#
# makensis is not installed on the primary dev machine this was written on
# (Linux, no way to run a real Windows executable to verify), so this
# script itself is only syntax/logic-verified, not a confirmed
# install-and-run pass — install NSIS (`sudo apt install nsis` on
# Debian/Ubuntu, or the "nsis" choco/scoop package on Windows) and run this
# script to actually produce and test AirMockSetup-*.exe.
set -e

SCRIPT_DIR="$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)"
REPO_ROOT="$(CDPATH= cd -- "$SCRIPT_DIR/../.." && pwd)"
DIST_DIR="$REPO_ROOT/dist"

if ! command -v makensis >/dev/null 2>&1; then
  echo "makensis not found on PATH. Install NSIS first:" >&2
  echo "  Debian/Ubuntu: sudo apt install nsis" >&2
  echo "  Windows:       choco install nsis   (or download from nsis.sourceforge.io)" >&2
  exit 1
fi

ARCH="${AIRMOCK_ARCH:-amd64}"

# Explicitly scoped to one architecture — goreleaser's dist/ has both amd64
# and arm64 builds side by side, and this .nsi (InstallDir under
# $PROGRAMFILES64, one un-suffixed OutFile) is written for a single-arch
# installer, not a fat one; picking "whichever find happens to list first"
# would silently ship the wrong architecture's binaries.
AIRMOCK_EXE=$(find "$DIST_DIR" -type f -name "airmock.exe" -path "*windows*${ARCH}*" ! -path "*gui*" | head -1)
AIRMOCKW_EXE=$(find "$DIST_DIR" -type f -name "airmockw.exe" -path "*windows*${ARCH}*" | head -1)

if [ -z "$AIRMOCK_EXE" ] || [ -z "$AIRMOCKW_EXE" ]; then
  echo "Could not find a windows/$ARCH airmock.exe/airmockw.exe under $DIST_DIR — run goreleaser first, or set AIRMOCK_ARCH=arm64." >&2
  exit 1
fi

cp "$AIRMOCK_EXE" "$SCRIPT_DIR/airmock.exe"
cp "$AIRMOCKW_EXE" "$SCRIPT_DIR/airmockw.exe"

VERSION="${1:-0.0.0-next}"
makensis "-DVERSION=$VERSION" "$SCRIPT_DIR/airmock.nsi"

echo "Built: $SCRIPT_DIR/AirMockSetup-$VERSION.exe"
