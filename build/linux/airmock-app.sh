#!/bin/sh
# Launches AirMock's admin UI as a standalone app window (no address bar,
# tabs, or bookmarks — just the app) instead of a regular browser tab,
# starting the server when the app opens and stopping it again when the
# app window is closed — matching how a normal desktop app behaves, rather
# than leaving a server running in the background indefinitely.
#
# If a server is already reachable (e.g. a systemd-managed install left
# running persistently), this only ever opens a window against it and never
# touches its lifecycle either way — the start/stop behavior below applies
# only to a throwaway instance this script itself started.
#
# A dedicated --user-data-dir is used so this app's Chrome state (window
# size, etc.) stays isolated from your regular day-to-day browsing profile.
# It does NOT, on its own, mean the launching command blocks until the
# window closes — Chrome forks/daemonizes almost immediately regardless of
# profile, so the command below returns right away either way. What
# actually detects "the app was closed" is polling for any process still
# using this profile dir below; once none remain, the window is gone.
set -e

ADMIN_URL="${AIRMOCK_ADMIN_URL:-http://localhost:8080}"
PROFILE_DIR="${AIRMOCK_APP_PROFILE_DIR:-$HOME/.local/share/airmock/app-profile}"

is_up() {
  curl -s -o /dev/null -m 1 "$ADMIN_URL/healthz"
}

SERVER_PID=""

# Only stop the server on exit if THIS script is the one that started it —
# a systemd-managed install, or one you started yourself in a terminal, is
# left running untouched; only a throwaway instance this script itself
# launched gets torn down once the window closes.
cleanup() {
  if [ -n "$SERVER_PID" ] && kill -0 "$SERVER_PID" 2>/dev/null; then
    kill "$SERVER_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT INT TERM

if ! is_up; then
  if command -v airmock >/dev/null 2>&1; then
    airmock serve --headless >/tmp/airmock-app.log 2>&1 &
  else
    REPO_DIR="$(CDPATH= cd -- "$(dirname -- "$0")/../.." && pwd)"
    "$REPO_DIR/airmock" serve --headless --data-dir "$REPO_DIR/.airmock-dev" >/tmp/airmock-app.log 2>&1 &
  fi
  SERVER_PID=$!

  for _ in $(seq 1 20); do
    is_up && break
    sleep 0.5
  done
fi

mkdir -p "$PROFILE_DIR"

# --app=URL is what makes Chrome/Chromium open a chromeless window instead
# of a normal browser tab. --no-first-run/--no-default-browser-check keep
# the dedicated profile from nagging about setup on first launch. --class
# sets the window's WM_CLASS to "AirMock" — without it, an --app window's
# WM_CLASS is just "Google-chrome" like any other Chrome window, so the
# taskbar/window switcher has no way to match it back to this launcher's
# .desktop entry (StartupWMClass=AirMock below) and falls back to showing
# Chrome's own icon instead of AirMock's. --disable-dev-tools removes
# DevTools entirely — right-click "Inspect", F12/Ctrl+Shift+I, and the
# hamburger-menu entry all stop doing anything — so this reads as a normal
# packaged app rather than "a webpage anyone can pop the console open on."
launched=0
for browser in google-chrome google-chrome-stable chromium chromium-browser; do
  if command -v "$browser" >/dev/null 2>&1; then
    "$browser" --app="$ADMIN_URL" --user-data-dir="$PROFILE_DIR" --class=AirMock --no-first-run --no-default-browser-check --disable-dev-tools >/tmp/airmock-app-chrome.log 2>&1 &
    launched=1
    break
  fi
done

if [ "$launched" = "0" ]; then
  # No Chrome-family browser found — fall back to the system's default
  # handler as a normal tab; there's no chromeless-window equivalent here,
  # and no reliable way to know when a plain `xdg-open` tab gets closed, so
  # the server this started (if any) is left running rather than guessed at.
  xdg-open "$ADMIN_URL"
  SERVER_PID=""
  exit 0
fi

# Give Chrome a moment to actually start before polling for it.
sleep 2
while pgrep -f "user-data-dir=$PROFILE_DIR" >/dev/null 2>&1; do
  sleep 2
done
# Falls through to `trap cleanup EXIT` once every process tied to this
# profile (window, renderers, GPU process, etc.) has exited.
