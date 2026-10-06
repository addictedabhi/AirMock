#!/bin/sh
set -e

if command -v systemctl >/dev/null 2>&1; then
    systemctl stop airmock.service >/dev/null 2>&1 || true
    systemctl disable airmock.service >/dev/null 2>&1 || true
fi
