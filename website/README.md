# AirMock website

A static site served by GitHub Pages: `index.html`, `favicon.svg`, `fonts/`, `shots/` and `og-image.png`.
Everything except the download list is loaded from the site itself.

Live at <https://addictedabhi.github.io/website_airmock/>.

## Publish

1. In the repository settings, open **Pages** and set the source to **Deploy from a branch**, branch
   `main`, folder `/ (root)`.
2. Push to `main`. Pages redeploys on every push.

`.nojekyll` turns off Jekyll processing, so files are served as they are in the repository.

## Downloads

The download table is read live from the latest release of
[addictedabhi/AirMock](https://github.com/addictedabhi/AirMock/releases) through the GitHub API, so
publishing a new release there updates the page with no change to this repository. Do not commit
packages or binaries here.

## Site address

Link previews (`og:image`, `twitter:image`) and the canonical link need absolute URLs. They are set to
the Pages address above. If you move the site, for example to a custom domain with a `CNAME` file, run:

```sh
python3 set-site-url.py index.html https://your.domain
```

It is idempotent: running it again replaces the previous address.

## Security headers

GitHub Pages serves HTTPS but does not allow custom response headers. The Content-Security-Policy is
set with a `<meta>` tag in `index.html`; headers that only work as real HTTP headers
(`frame-ancestors`, `X-Frame-Options`, `Strict-Transport-Security`) cannot be set on Pages.
Enable **Enforce HTTPS** in the Pages settings.
