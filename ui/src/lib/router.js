import { writable, get } from 'svelte/store';

// The nav icon for each page is rendered by <Icon name={p.id} /> in
// Sidebar.svelte — page id doubles as the icon name since they already
// match 1:1, rather than keeping a separate (and easy to let drift) icon
// field here.
export const pages = [
  { id: 'dashboard', label: 'Dashboard' },
  { id: 'mocks', label: 'Mocks' },
  { id: 'tcpmocks', label: 'TCP Mocks' },
  { id: 'smtpmocks', label: 'SMTP Mocks' },
  { id: 'wsmocks', label: 'WS Mocks' },
  { id: 'mqttmocks', label: 'MQTT Mocks' },
  { id: 'ftpmocks', label: 'FTP Mocks' },
  { id: 'kafkamocks', label: 'Kafka Mocks' },
  { id: 'smppmocks', label: 'SMPP Mocks' },
  { id: 'diametermocks', label: 'Diameter Mocks' },
  { id: 'jmsmocks', label: 'JMS Mocks' },
  { id: 'scheduledevents', label: 'Scheduled Events' },
  { id: 'collections', label: 'Collections' },
  { id: 'certificates', label: 'Certificates' },
  { id: 'smtp', label: 'SMTP Settings' },
  { id: 'logs', label: 'Log History' },
  { id: 'settings', label: 'Settings' },
];

function initial() {
  const hash = typeof location !== 'undefined' ? location.hash.replace('#/', '') : '';
  return pages.some((p) => p.id === hash) ? hash : 'dashboard';
}

export const currentPage = writable(initial());

export function navigate(id) {
  currentPage.set(id);
  if (typeof location !== 'undefined') location.hash = `/${id}`;
}

// pendingFocus carries a "jump to this specific item" request across a page
// navigation — e.g. the command palette (Ctrl/Cmd+K) resolving a search hit
// to a specific mock or collection request. Exported as a store (not just
// via consumePendingFocus) so a destination page can react to it with a
// top-level `$: if ($pendingFocus?.pageId === '...') {...}` statement
// instead of only reading it once in onMount — onMount alone would miss a
// palette selection that targets an item already on the CURRENTLY-showing
// page, since {#key $currentPage} in App.svelte only remounts a page when
// currentPage actually changes value (Svelte stores don't notify on a
// same-value .set), so navigating from Mocks to a different mock while
// already on Mocks would otherwise never re-run onMount at all.
export const pendingFocus = writable(null); // { pageId, itemId, extra } | null

// navigateToItem is navigate() plus a focus target for the destination page
// to pick up (see consumePendingFocus).
export function navigateToItem(pageId, itemId, extra = {}) {
  pendingFocus.set({ pageId, itemId, extra });
  navigate(pageId);
}

// consumePendingFocus returns the itemId/extra pair if this page was the
// intended target, clearing the store ONLY on that match — a page whose
// pageId doesn't match leaves the store untouched, so a fast second
// navigation (to a different page, before the first page's own consume
// runs) can't have its still-pending focus request wiped out by an
// unrelated page's mismatched check.
export function consumePendingFocus(pageId) {
  const val = get(pendingFocus);
  if (val && val.pageId === pageId) {
    pendingFocus.set(null);
    return val;
  }
  return null;
}

// Keep the SPA in sync with hash changes the router itself didn't cause —
// browser back/forward, a pasted/bookmarked #/mocks URL while the app is
// already loaded, etc. Without this, currentPage only ever reflects the
// hash read once at module load.
if (typeof window !== 'undefined') {
  window.addEventListener('hashchange', () => {
    const hash = location.hash.replace('#/', '');
    if (pages.some((p) => p.id === hash)) currentPage.set(hash);
  });
}
