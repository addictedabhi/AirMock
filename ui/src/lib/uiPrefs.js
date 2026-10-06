// Two small per-browser UI preferences — deliberately NOT instance-wide
// Settings (unlike the Settings page's own backend-persisted fields):
// purely cosmetic/local choices that make sense to differ between
// whoever's sitting at a given browser, same reasoning theme.js already
// applies to the theme/accent choice. Mirrors its writable-store +
// localStorage + apply-on-subscribe pattern.
import { writable } from 'svelte/store';

const DENSITY_KEY = 'airmock-ui-density';
const CONFIRM_BEFORE_DELETE_KEY = 'airmock-confirm-before-delete';

function readStorage(key, fallback) {
  if (typeof localStorage === 'undefined') return fallback;
  try {
    return localStorage.getItem(key) ?? fallback;
  } catch {
    return fallback;
  }
}

function writeStorage(key, value) {
  if (typeof localStorage === 'undefined') return;
  try {
    localStorage.setItem(key, value);
  } catch {
    // Private browsing / storage disabled — the preference just won't
    // survive a reload, not worth surfacing as an error.
  }
}

export const density = writable(readStorage(DENSITY_KEY, 'comfortable') === 'compact' ? 'compact' : 'comfortable');
density.subscribe((value) => {
  if (typeof document !== 'undefined') document.documentElement.dataset.density = value;
  writeStorage(DENSITY_KEY, value);
});

export function setDensity(value) {
  density.set(value === 'compact' ? 'compact' : 'comfortable');
}

export const confirmBeforeDelete = writable(readStorage(CONFIRM_BEFORE_DELETE_KEY, 'true') !== 'false');
confirmBeforeDelete.subscribe((value) => {
  writeStorage(CONFIRM_BEFORE_DELETE_KEY, value ? 'true' : 'false');
});

export function setConfirmBeforeDelete(value) {
  confirmBeforeDelete.set(!!value);
}

// installConfirmOverride wraps the browser's own window.confirm once (call
// from App.svelte's onMount) so every existing `confirm("Delete …?")` call
// site across the app — there are dozens, one per delete/restore/bulk-
// action button — automatically skips the dialog (auto-confirming true)
// when the preference is off, with zero changes needed at each call site.
// Idempotent: calling it more than once is harmless (the second call just
// re-wraps the same already-wrapped function).
export function installConfirmOverride() {
  if (typeof window === 'undefined' || !window.confirm) return;
  const nativeConfirm = window.confirm.bind(window);
  let allowConfirmations = true;
  confirmBeforeDelete.subscribe((value) => {
    allowConfirmations = value;
  });
  window.confirm = (message) => (allowConfirmations ? nativeConfirm(message) : true);
}
