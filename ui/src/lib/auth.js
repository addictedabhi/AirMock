// Talks to the optional admin login (see internal/auth, internal/web/api/
// auth.go) — AirMock's real, server-enforced access control for an instance
// deployed somewhere reachable by more than just the one trusted local
// machine. authState is null until the very first status check resolves,
// so App.svelte can show a brief loading state rather than flashing the
// wrong view (login vs. the real app) for a moment on load.
import { writable, get } from 'svelte/store';

export const authState = writable(null);

async function rawRequest(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: body ? { 'Content-Type': 'application/json' } : undefined,
    body: body ? JSON.stringify(body) : undefined,
  });
  let data = null;
  try {
    data = await res.json();
  } catch {
    // a body-less response (shouldn't happen for these endpoints, but
    // don't let a parse failure mask the real status)
  }
  if (!res.ok) {
    const err = new Error(data?.error ?? `${method} ${path} failed (${res.status})`);
    err.status = res.status;
    throw err;
  }
  return data;
}

// A phone reopening a backgrounded/"closed" browser tab commonly fires
// this exact check before the OS has actually reestablished network
// connectivity — a real-world race, not a rare edge case — so the first
// failure gets one short retry before giving up, rather than immediately
// treating a one-off blip as gospel.
async function fetchAuthStatusOnce() {
  return rawRequest('GET', '/api/auth/status');
}

export async function checkAuthStatus() {
  try {
    const data = await fetchAuthStatusOnce();
    authState.set(data);
    return data;
  } catch {
    try {
      await new Promise((resolve) => setTimeout(resolve, 1200));
      const data = await fetchAuthStatusOnce();
      authState.set(data);
      return data;
    } catch {
      // Still failing after the retry — default to showing the login
      // screen rather than the real app (the safer side when unsure
      // whether auth is actually required), but keep whatever
      // credentialType was last known rather than wiping it: this used to
      // unconditionally drop it, which silently regressed the login
      // screen from a PIN pad to a plain password field for anyone whose
      // session had ALSO expired during the same network blip (the
      // credentialType a device last knew about doesn't stop being true
      // just because this one status check failed).
      const previous = get(authState);
      const fallback = { authRequired: true, authenticated: false, credentialType: previous?.credentialType ?? '', pinLength: previous?.pinLength ?? 0 };
      authState.set(fallback);
      return fallback;
    }
  }
}

export async function login(password) {
  await rawRequest('POST', '/api/auth/login', { password });
  await checkAuthStatus();
}

export async function logout() {
  await rawRequest('POST', '/api/auth/logout');
  await checkAuthStatus();
}

// setPassword both enables login (the very first time it's called) and
// changes an already-set password — the backend applies RequireAuth to
// this endpoint per-route, which is a no-op while disabled, so both cases
// hit the same call here. Re-checks status afterward since the first-time
// case logs the caller in immediately (a fresh session cookie).
// credentialType is 'pin' or 'password' — purely which input widget the
// login screen uses and which format the backend validates against; both
// are checked identically underneath.
export async function setPassword(credentialType, newPassword) {
  await rawRequest('POST', '/api/auth/set-password', { credentialType, newPassword });
  await checkAuthStatus();
}

// clearPassword disables login entirely (the Settings page's "Turn off"
// action) — requires an existing session, same as any other admin action.
export async function clearPassword() {
  await rawRequest('POST', '/api/auth/clear-password');
  await checkAuthStatus();
}

// Called by api.js's shared request()/requestForm() helpers whenever any
// API call comes back 401 mid-session (an expired or invalidated session,
// not just a fresh page load) — flips the app back to the login screen
// instead of leaving whatever page was open showing a confusing generic
// error toast for a request that was never going to succeed. Preserves
// the rest of the existing state (credentialType above all — dropping it
// used to make the login screen fall back to a plain password field even
// when a PIN was actually configured) since none of that changed just
// because this one request found the session gone; only checkAuthStatus's
// real GET actually refreshes it from the server.
export function markUnauthenticated() {
  authState.update((s) => ({ ...(s ?? {}), authRequired: true, authenticated: false }));
}

// Periodically re-checks /api/auth/status while a session is believed
// active, so a session that expires (the rolling server-side timeout
// elapsing) is noticed and reflected — login screen shown, correct
// credentialType and all — within one poll interval, rather than only on
// whatever next incidental API call happens to 401. Idempotent (same
// pattern as uiPrefs.js's installConfirmOverride) — call once from
// App.svelte's onMount.
//
// Also re-checks immediately on visibilitychange (tab becoming visible
// again) — a setInterval in a BACKGROUND browser tab gets aggressively
// throttled by the browser (to save power) and can go long stretches
// without firing at all, so relying on the interval alone meant this
// silently stopped working the moment the tab wasn't the foreground one
// (e.g. plain browser access, as opposed to the .deb package's dedicated
// single-tab app-mode window, which rarely gets treated as "background"
// the same way).
let pollTimer = null;
export function installSessionExpiryPoller() {
  if (typeof window === 'undefined' || pollTimer) return;

  const check = () => {
    const current = get(authState);
    if (current?.authRequired && current?.authenticated) {
      checkAuthStatus();
    }
  };
  pollTimer = setInterval(check, 30000);
  document.addEventListener('visibilitychange', () => {
    if (document.visibilityState === 'visible') check();
  });
}
