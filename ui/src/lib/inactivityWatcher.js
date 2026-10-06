// Real, server-enforced auto-lock on inactivity — a SEPARATE, independently
// configured mechanism from the rolling session timeout in
// internal/auth.Manager (Settings' "Session timeout" field), which only
// extends on actual API calls and so can stay alive well past when a
// person actually stopped looking at the screen if something on the page
// happens to poll in the background. This watches real mouse/keyboard/
// scroll/touch activity and calls the same logout() a manual "Log out"
// click would, ending the session for real — not just a client-side
// overlay sitting on top of a still-valid session. Configured via
// Settings' own "Auto-lock after inactivity" field (settings.
// InactivityLockMinutes) — 0 means off.
//
// Tracks a real lastActivityAt timestamp and re-derives elapsed time on
// every check, rather than just trusting a single scheduled setTimeout to
// fire on time — browsers aggressively throttle timers in BACKGROUND tabs
// (to save power), which silently broke this in an ordinary browser tab
// that the user switched away from, while working fine in the .deb
// package's dedicated app-mode window (a single-tab window rarely
// considered "background" the same way). A visibilitychange listener
// forces an immediate real-time catch-up check the moment the tab becomes
// visible again, rather than waiting on a timer that may have been
// suspended the whole time it was hidden.
import { get } from 'svelte/store';
import { authState, logout } from './auth.js';
import { api } from './api.js';

let timeoutMs = 0;
let lastActivityAt = Date.now();
let checkTimer = null;
let listenersInstalled = false;
let wasAuthenticated = false;

function checkNow() {
  const state = get(authState);
  if (!state?.authRequired || !state?.authenticated || !timeoutMs) return;
  if (Date.now() - lastActivityAt >= timeoutMs) {
    logout();
  }
}

// Schedules the NEXT check, but checkNow() always re-derives elapsed time
// from real Date.now() timestamps rather than trusting this timer's own
// fire time — so even a badly-throttled/delayed fire still gets the
// correct answer, it just might answer it a bit late (bounded by however
// long the tab stayed backgrounded, closed by the visibilitychange catch-
// up below).
function scheduleCheck() {
  if (checkTimer) clearTimeout(checkTimer);
  checkTimer = null;
  if (!timeoutMs) return;
  const remaining = Math.max(0, timeoutMs - (Date.now() - lastActivityAt));
  checkTimer = setTimeout(checkNow, remaining + 250);
}

function noteActivity() {
  lastActivityAt = Date.now();
  scheduleCheck();
}

// Exported so Settings.svelte can apply a just-saved value immediately,
// without waiting for the next login to pick it up.
export function setInactivityTimeoutMinutes(minutes) {
  timeoutMs = Math.max(0, Number(minutes) || 0) * 60 * 1000;
  lastActivityAt = Date.now();
  scheduleCheck();
}

// Learns the configured timeout on its own the moment a session becomes
// authenticated (fresh login, or an already-valid cookie on page load) —
// relying on someone happening to open Settings first would leave
// auto-lock silently inactive for the rest of that session otherwise.
async function loadTimeoutFromSettings() {
  try {
    const s = await api.getSettings();
    setInactivityTimeoutMinutes(s.inactivityLockMinutes);
  } catch {
    // Settings unreachable for some reason — leave whatever timeout is
    // already in effect (or disabled) rather than failing loudly here.
  }
}

// Installs the activity listeners exactly once (idempotent, same pattern
// as uiPrefs.js's installConfirmOverride) — call from App.svelte's
// onMount. Broad event coverage (mouse/keyboard/scroll/touch) matches how
// an OS-level screen lock treats "still here" activity; throttled to once
// a second rather than resetting the timer on every single mousemove.
export function installInactivityWatcher() {
  if (typeof window === 'undefined' || listenersInstalled) return;
  listenersInstalled = true;

  let throttled = false;
  const onActivity = () => {
    if (throttled) return;
    throttled = true;
    setTimeout(() => {
      throttled = false;
    }, 1000);
    noteActivity();
  };
  ['mousemove', 'mousedown', 'keydown', 'scroll', 'touchstart'].forEach((evt) =>
    window.addEventListener(evt, onActivity, { passive: true }),
  );

  // The fix for background-tab timer throttling: the instant this tab
  // becomes visible again, immediately re-check real elapsed time rather
  // than waiting for whatever scheduled timer may have been suspended (or
  // hugely delayed) the whole time it was hidden.
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') {
      checkNow();
      scheduleCheck();
    }
  });

  authState.subscribe((state) => {
    const authenticated = !!(state?.authRequired && state?.authenticated);
    if (authenticated && !wasAuthenticated) {
      loadTimeoutFromSettings();
    } else {
      scheduleCheck();
    }
    wasAuthenticated = authenticated;
  });
}
