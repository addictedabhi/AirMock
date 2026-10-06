# AirMock website

A static site: `index.html`, `favicon.svg`, `fonts/`, two screenshots, `og-image.png`, and a generated
`downloads/` folder. Nothing is loaded from another host, so it works on an isolated network.

## Publish

```sh
# 1. Build the packages
goreleaser release --snapshot --clean --skip=before      # or a tagged release

# 2. Generate website/downloads/ (packages, checksums.txt, manifest.json) from dist/
website/make-downloads.sh

# 3. Copy the site to the server
rsync -av --delete website/ user@SERVER:/var/www/airmock-site/
```

The page reads `downloads/manifest.json` for the version, commit and package list, so republishing is
all it takes to update them. `downloads/` is generated and git-ignored; do not commit binaries.

## HTTPS (needed for the copy button's modern path, and good practice)

The Copy button works on plain HTTP too (it falls back to selecting the text), but HTTPS makes the
clipboard, caching headers and `Strict-Transport-Security` behave properly. For an internal CA:

```sh
# one-off: a CA, then a certificate for the site's name
openssl req -x509 -newkey rsa:3072 -nodes -days 3650 -subj "/CN=AirMock Internal CA" -keyout ca.key -out ca.crt
openssl req -newkey rsa:2048 -nodes -subj "/CN=airmock.example.internal" -keyout site.key -out site.csr
printf "subjectAltName=DNS:airmock.example.internal,IP:10.121.78.115\n" > san.ext
openssl x509 -req -in site.csr -CA ca.crt -CAkey ca.key -CAcreateserial -days 825 -extfile san.ext -out site.crt
sudo install -m 600 site.key /etc/ssl/airmock/site.key && sudo install -m 644 site.crt /etc/ssl/airmock/site.crt
```

Install `ca.crt` as a trusted root on the machines that browse the site. Then use `nginx.conf`
(HTTP-to-HTTPS redirect, security headers, caching).

You can also use AirMock's own certificate store (Certificates page) to generate the CA and server
certificate and download the PEM files.
