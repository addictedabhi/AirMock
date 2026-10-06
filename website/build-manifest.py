#!/usr/bin/env python3
"""Regenerate checksums.txt and manifest.json for a downloads folder from the files in it.

    python3 build-manifest.py /var/www/airmock-site/downloads            # on the server, after adding a file
    python3 build-manifest.py website/downloads dist/metadata.json       # at build time (make-downloads.sh)

The page lists whatever manifest.json says, so any package (for example a Windows installer built
elsewhere) appears as soon as it is in the folder and this has been run. Version, commit and date come
from the goreleaser metadata.json when given, otherwise from the existing manifest.json.
"""
import hashlib, html, json, os, sys

folder = sys.argv[1] if len(sys.argv) > 1 else "."
meta_path = sys.argv[2] if len(sys.argv) > 2 else None
manifest_path = os.path.join(folder, "manifest.json")

meta = {}
if meta_path and os.path.exists(meta_path):
    meta = json.load(open(meta_path))
elif os.path.exists(manifest_path):
    meta = json.load(open(manifest_path))

SKIP = {"checksums.txt", "manifest.json", "index.html"}
files, sums = [], []
for name in sorted(os.listdir(folder)):
    path = os.path.join(folder, name)
    if name in SKIP or name.startswith(".") or not os.path.isfile(path):
        continue
    h = hashlib.sha256()
    with open(path, "rb") as f:
        for chunk in iter(lambda: f.read(1 << 20), b""):
            h.update(chunk)
    low = name.lower()
    os_name = next((o for o in ("linux", "darwin", "windows") if o in low), "windows" if low.endswith(".exe") else "")
    arch = next((a for a in ("amd64", "arm64", "x86_64", "x64") if a in low), "")
    if arch in ("x86_64", "x64"):
        arch = "amd64"
    kind = ("deb" if low.endswith(".deb") else "rpm" if low.endswith(".rpm") else
            "installer" if low.endswith(".exe") else "zip" if low.endswith(".zip") else "tarball")
    files.append({"name": name, "os": os_name, "arch": arch, "kind": kind, "size": os.path.getsize(path), "sha256": h.hexdigest()})
    sums.append(f"{h.hexdigest()}  {name}\n")

open(os.path.join(folder, "checksums.txt"), "w").writelines(sums)
manifest = {
    "version": meta.get("version", ""), "tag": meta.get("tag", ""),
    "commit": (meta.get("commit", "") or "")[:12], "date": meta.get("date", ""),
    "checksums": "checksums.txt", "files": files,
}
json.dump(manifest, open(manifest_path, "w"), indent=1)
# A plain page listing the same files, for visitors without JavaScript (the main page fills its
# download table from manifest.json with a script) and for anyone who just browses to downloads/.
def size(n):
    return f"{n / 1048576:.1f} MB" if n > 1048576 else f"{max(1, round(n / 1024))} KB"
rows = "\n".join(
    f'<tr><td><a href="{html.escape(f["name"])}">{html.escape(f["name"])}</a></td><td>{size(f["size"])}</td><td><code>{f["sha256"]}</code></td></tr>'
    for f in files)
open(os.path.join(folder, "index.html"), "w", encoding="utf-8").write(f"""<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>AirMock downloads</title>
<style>body{{font:16px/1.5 system-ui,sans-serif;max-width:60rem;margin:2rem auto;padding:0 1rem}}table{{border-collapse:collapse;width:100%}}th,td{{text-align:left;padding:.4rem .6rem;border-bottom:1px solid #ccd}}code{{font-size:.8rem;word-break:break-all}}</style>
</head><body>
<h1>AirMock downloads</h1>
<p>Version <b>{html.escape(manifest["version"] or "unknown")}</b>{(" · commit <b>" + html.escape(manifest["commit"]) + "</b>") if manifest["commit"] else ""}{(" · built " + html.escape(manifest["date"][:10])) if manifest["date"] else ""}.
Verify a file with <code>sha256sum -c checksums.txt</code> in this folder, or compare its SHA-256 below.</p>
<table><caption>Packages with their sizes and SHA-256 checksums</caption>
<thead><tr><th scope="col">Package</th><th scope="col">Size</th><th scope="col">SHA-256</th></tr></thead>
<tbody>
{rows}
</tbody></table>
<p><a href="checksums.txt">checksums.txt</a> · <a href="manifest.json">manifest.json</a> · <a href="../">Back to the AirMock site</a></p>
</body></html>
""")
print(f"{len(files)} files listed in manifest.json (version {manifest['version'] or 'unknown'})")
