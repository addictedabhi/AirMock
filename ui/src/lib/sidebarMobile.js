// Whether the sidebar is currently shown as a mobile off-canvas overlay —
// separate from Sidebar.svelte's own `collapsed` (a persisted desktop
// icon-only preference toggle). This one is pure transient UI state: never
// persisted, always starts closed, toggled by App.svelte's hamburger
// button (itself only visible below the mobile breakpoint via CSS, so
// there's no way to set this true on a desktop-width viewport in the
// first place) and closed again by Sidebar.svelte on backdrop click or
// picking a nav item.
import { writable } from 'svelte/store';

export const mobileSidebarOpen = writable(false);

export function toggleMobileSidebar() {
  mobileSidebarOpen.update((v) => !v);
}

export function closeMobileSidebar() {
  mobileSidebarOpen.set(false);
}
