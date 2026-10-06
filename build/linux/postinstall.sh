#!/bin/sh
set -e

if ! getent group airmock >/dev/null 2>&1; then
    groupadd --system airmock
fi
if ! getent passwd airmock >/dev/null 2>&1; then
    useradd --system --gid airmock --home-dir /var/lib/airmock --no-create-home \
        --shell /usr/sbin/nologin airmock
fi

mkdir -p /var/lib/airmock
chown -R airmock:airmock /var/lib/airmock

if command -v systemctl >/dev/null 2>&1; then
    systemctl daemon-reload
    systemctl enable airmock.service >/dev/null 2>&1 || true
    systemctl restart airmock.service >/dev/null 2>&1 || true
fi

# Registers the "AirMock" app entry (icon, launch command) with the
# desktop so it shows up in the application menu/launcher right away,
# rather than only after the next login.
if command -v update-desktop-database >/dev/null 2>&1; then
    update-desktop-database /usr/share/applications >/dev/null 2>&1 || true
fi
if command -v gtk-update-icon-cache >/dev/null 2>&1; then
    gtk-update-icon-cache /usr/share/pixmaps >/dev/null 2>&1 || true
fi

echo "AirMock installed. Admin UI: http://localhost:8080"
echo "Launch it as an app from your application menu (search \"AirMock\"), or run: airmock-app"
