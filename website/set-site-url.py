#!/usr/bin/env python3
"""Make the social-preview URLs absolute and add the canonical link, once you know the site's address.

    python3 set-site-url.py index.html https://airmock.example.internal
    python3 set-site-url.py /var/www/airmock-site/index.html http://10.121.78.115:8090

Link-preview crawlers (chat, email, social) need absolute URLs for og:image and twitter:image, and a
canonical link must be absolute too. The address is not known when the page is built, so run this
after publishing, on the copy that is served. It is idempotent: running it again with a new address
replaces the old one. Without it the page still works; only previews and the canonical link are missing.
"""
import re, sys

if len(sys.argv) != 3:
    sys.exit(__doc__)
path, base = sys.argv[1], sys.argv[2].rstrip("/")
if not re.match(r"^https?://[^/\s]+$", base) and not re.match(r"^https?://[^\s]+$", base):
    sys.exit("site URL must start with http:// or https://")

html = open(path, encoding="utf-8").read()

# og:image / twitter:image -> absolute
html, n = re.subn(r'(<meta (?:property="og:image"|name="twitter:image") content=")[^"]*(og-image\.png")', lambda m: m.group(1) + base + "/" + m.group(2), html)

block = (
    "<!-- site-url:start -->\n"
    f'<link rel="canonical" href="{base}/">\n'
    f'<meta property="og:url" content="{base}/">\n'
    "<!-- site-url:end -->"
)
if "<!-- site-url:start -->" in html:
    html = re.sub(r"<!-- site-url:start -->.*?<!-- site-url:end -->", lambda m: block, html, flags=re.S)
else:
    html = html.replace("</head>", block + "\n</head>", 1)

open(path, "w", encoding="utf-8").write(html)
print(f"set site URL to {base}/ ({n} image tags made absolute, canonical + og:url written)")
