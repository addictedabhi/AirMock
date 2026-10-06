import { writable } from 'svelte/store';

export const toast = writable(null); // { id, message, kind: 'ok' | 'warn' | 'err', visible }

let hideTimer;
let nextId = 0;

// Warnings returned by the API alongside a successful save (see api.js).
// They ride on the success toast the page shows right after, rather than
// being replaced by it, and are dropped if no toast follows soon.
let pendingWarnings = [];
let pendingTimer;

export function queueWarnings(warnings) {
  clearTimeout(pendingTimer);
  pendingWarnings = Array.isArray(warnings) ? warnings : [];
  if (pendingWarnings.length) pendingTimer = setTimeout(() => (pendingWarnings = []), 2000);
}

export function showToast(message, kind = 'ok') {
  clearTimeout(hideTimer);
  let duration = 2600;
  if (kind === 'ok' && pendingWarnings.length) {
    message = `${message}. Warning: ${pendingWarnings.join(' ')}`;
    kind = 'warn';
    pendingWarnings = [];
    duration = 9000;
  }
  // id is stable across the show -> hide transition of ONE toast (only the
  // `visible` flag flips) and changes only when a genuinely new toast
  // starts — the UI keys its countdown-bar restart animation on it, since
  // keying on the whole object would also fire (and wrongly restart it) on
  // the object recreated by the hide transition below.
  const id = nextId++;
  toast.set({ id, message, kind, visible: true });
  hideTimer = setTimeout(() => {
    toast.update((t) => (t ? { ...t, visible: false } : t));
  }, duration);
}
