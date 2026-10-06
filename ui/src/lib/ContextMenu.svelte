<script>
  import { onMount, onDestroy } from 'svelte';

  // A generic custom right-click menu, replacing the browser's own context
  // menu (see App.svelte's global contextmenu handler) with app-specific
  // actions — reused across every Mocks page for a row's Edit/Duplicate/
  // Enable-Disable/Delete etc. without each page reimplementing
  // positioning, viewport-clamping, or close-on-outside-click/scroll.
  //
  // x/y are viewport coordinates (an event's clientX/clientY — the point
  // the user actually right-clicked). items is an array of
  // { label, onClick, danger?, disabled?, divider? } — a divider-only entry
  // needs no other fields.
  export let x = 0;
  export let y = 0;
  export let items = [];
  export let onClose = () => {};

  let menuEl;

  // Portals straight to <body>, the same fix (and for the same reason —
  // an ancestor's transform/overflow silently detaching or clipping a
  // position:fixed element) already used for Suggestions.svelte's dropdown
  // and Collections.svelte's own "⋮" menus.
  function portal(node) {
    document.body.appendChild(node);
    return {
      destroy() {
        node.remove();
      },
    };
  }

  // Keeps the menu fully on-screen regardless of how close to an edge the
  // right-click landed — same reasoning as InfoTooltip's own clamp, adapted
  // for a point position (x, y) instead of centering on an anchor element.
  function clampToViewport(node) {
    const margin = 8;
    const rect = node.getBoundingClientRect();
    let left = x;
    let top = y;
    if (left + rect.width > window.innerWidth - margin) {
      left = window.innerWidth - margin - rect.width;
    }
    if (left < margin) left = margin;
    if (top + rect.height > window.innerHeight - margin) {
      top = window.innerHeight - margin - rect.height;
    }
    if (top < margin) top = margin;
    node.style.left = `${left}px`;
    node.style.top = `${top}px`;
  }

  // Chrome's scroll-anchoring can fire a genuine but incidental `scroll`
  // event the instant this menu is portaled into <body> near a scroll
  // boundary, which would otherwise close the menu before the user ever
  // sees it — the same false-positive already found and guarded against
  // for Collections.svelte's own menus. Two rAFs is enough to get past
  // that incidental settling while still catching any real scroll the
  // user causes afterward.
  let menuJustOpened = true;
  function armScrollGuard() {
    menuJustOpened = true;
    requestAnimationFrame(() => requestAnimationFrame(() => { menuJustOpened = false; }));
  }

  function handleWindowScroll() {
    if (menuJustOpened) return;
    onClose();
  }

  function handleOutsideClick(e) {
    if (menuEl && !menuEl.contains(e.target)) onClose();
  }

  function handleKeydown(e) {
    if (e.key === 'Escape') onClose();
  }

  function choose(item) {
    if (item.disabled) return;
    item.onClick?.();
    onClose();
  }

  onMount(() => {
    armScrollGuard();
    // capture phase so this also catches an inner scrollable list's own
    // scroll, which doesn't bubble.
    window.addEventListener('scroll', handleWindowScroll, true);
    document.addEventListener('mousedown', handleOutsideClick);
    document.addEventListener('keydown', handleKeydown);
  });
  onDestroy(() => {
    window.removeEventListener('scroll', handleWindowScroll, true);
    document.removeEventListener('mousedown', handleOutsideClick);
    document.removeEventListener('keydown', handleKeydown);
  });
</script>

<div class="context-menu" role="menu" bind:this={menuEl} use:portal use:clampToViewport>
  {#each items as item, i (i)}
    {#if item.divider}
      <div class="context-menu-divider"></div>
    {:else}
      <button
        type="button"
        role="menuitem"
        class="context-menu-item"
        class:danger={item.danger}
        disabled={item.disabled}
        on:click={() => choose(item)}
      >{item.label}</button>
    {/if}
  {/each}
</div>

<style>
  .context-menu {
    position: fixed; z-index: 1000; min-width: 180px;
    background: var(--card); border: 1px solid var(--border); border-radius: 8px;
    box-shadow: 0 10px 28px rgba(0,0,0,.22); padding: 4px;
    display: flex; flex-direction: column; gap: 1px;
    animation: theme-menu-in .12s ease both;
  }
  .context-menu-item {
    display: block; width: 100%; text-align: left; padding: 7px 10px; border-radius: 6px;
    background: none; border: none; color: var(--text); font-size: 13px; font-weight: 500; cursor: pointer;
    font-family: inherit;
  }
  .context-menu-item:hover:not(:disabled) { background: var(--hover); }
  .context-menu-item:disabled { color: var(--muted); cursor: not-allowed; }
  .context-menu-item.danger { color: var(--error, #dc2626); }
  .context-menu-item.danger:hover:not(:disabled) { background: rgba(220,38,38,.1); }
  .context-menu-divider { height: 1px; background: var(--border); margin: 4px 2px; }
</style>
