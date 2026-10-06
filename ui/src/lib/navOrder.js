// Lets the sidebar's nav item sequence be drag-and-drop reordered and
// remembered per-browser — same writable-store + localStorage +
// apply-on-subscribe pattern as theme.js/uiPrefs.js. Stores only the
// ORDER (an array of page ids), never a snapshot of the page list itself,
// so a later release renaming a label or adding a brand-new page still
// picks that up correctly (see effectiveOrderIds below) instead of a
// stale copy silently drifting out of sync with router.js's own list.
import { derived, writable } from 'svelte/store';
import { pages } from './router.js';

const STORAGE_KEY = 'airmock-sidebar-nav-order';

function readOrderIds() {
  if (typeof localStorage === 'undefined') return [];
  try {
    const parsed = JSON.parse(localStorage.getItem(STORAGE_KEY) || '[]');
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

// effectiveOrderIds reconciles a saved id list against the real, current
// `pages` list: an id that no longer exists (a page removed in a later
// release) is dropped; a current page id NOT yet in the saved list (a
// brand-new page in a later release, or simply the very first load before
// any reorder happened at all) is appended at the end in its own natural
// router.js order, rather than silently disappearing from the sidebar.
function effectiveOrderIds(ids) {
  const known = new Set(pages.map((p) => p.id));
  const ordered = ids.filter((id) => known.has(id));
  const seen = new Set(ordered);
  for (const p of pages) {
    if (!seen.has(p.id)) ordered.push(p.id);
  }
  return ordered;
}

export const navOrderIds = writable(readOrderIds());
navOrderIds.subscribe((ids) => {
  if (typeof localStorage === 'undefined') return;
  try {
    localStorage.setItem(STORAGE_KEY, JSON.stringify(ids));
  } catch {
    // Private browsing / storage disabled — the custom order just won't
    // survive a reload, not worth surfacing as an error.
  }
});

// orderedPages is what Sidebar.svelte (and, for a consistent Ctrl/Cmd+K
// list, CommandPalette.svelte) actually iterates — the real page objects
// from router.js, in the user's own remembered order.
export const orderedPages = derived(navOrderIds, (ids) => {
  const byId = new Map(pages.map((p) => [p.id, p]));
  return effectiveOrderIds(ids).map((id) => byId.get(id));
});

// reorderNav moves fromId to sit exactly where toId currently is (the
// standard "drop onto this row" reordering semantics) — a no-op if either
// id is unknown or they're the same row.
export function reorderNav(fromId, toId) {
  if (fromId === toId) return;
  navOrderIds.update((ids) => {
    const order = effectiveOrderIds(ids);
    const fromIdx = order.indexOf(fromId);
    const toIdx = order.indexOf(toId);
    if (fromIdx === -1 || toIdx === -1) return order;
    order.splice(fromIdx, 1);
    order.splice(toIdx, 0, fromId);
    return order;
  });
}

// resetNavOrder restores router.js's own natural page order — surfaced on
// the Settings page's Preferences card alongside the other per-browser
// UI prefs, for anyone who's reordered things into a state they no longer
// want without having to drag every item back by hand.
export function resetNavOrder() {
  navOrderIds.set(pages.map((p) => p.id));
}
