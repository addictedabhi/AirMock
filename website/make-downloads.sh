#!/bin/sh
# Builds website/downloads/ from a goreleaser dist/ directory: the packages,
# a checksums.txt and a manifest.json that the download section of index.html
# reads (so the page never hard-codes a version).
#
#   goreleaser release --snapshot --clean --skip=before   # or a tagged release
#   website/make-downloads.sh                              # defaults to ../dist
#   rsync -av website/ user@server:/var/www/airmock-site/
#
# The downloads/ folder is generated and git-ignored; do not commit binaries.
set -eu
here=$(cd "$(dirname "$0")" && pwd)
dist=${1:-"$here/../dist"}
out="$here/downloads"

[ -f "$dist/metadata.json" ] || { echo "no $dist/metadata.json: run goreleaser first" >&2; exit 1; }
rm -rf "$out" && mkdir -p "$out"

# Only the distributable artifacts, not goreleaser's per-target build folders.
for f in "$dist"/airmock_*.tar.gz "$dist"/airmock_*.zip "$dist"/airmock_*.deb "$dist"/airmock_*.rpm "$dist"/AirMockSetup-*.exe; do
  [ -f "$f" ] && cp "$f" "$out/"
done
# Anything else you want offered (for example a Windows installer built on another machine): put it in
# website/downloads-extra/ and it is copied in too, so a re-sync never removes it from the server.
if [ -d "$here/downloads-extra" ]; then
  for f in "$here"/downloads-extra/*; do [ -f "$f" ] && cp "$f" "$out/"; done
fi

python3 "$here/build-manifest.py" "$out" "$dist/metadata.json"
