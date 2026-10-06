<script>
  import { tick } from 'svelte';
  import { api } from './api.js';
  import { pages, navigate, navigateToItem } from './router.js';

  // Must match Collections.svelte's own WORKSPACE_STORAGE_KEY — read
  // (never written) here so the palette only ever searches the collection
  // tree the user would actually land on by navigating to Collections
  // themselves, keeping every search hit's target page consistent with
  // where consumePendingFocus will actually find it.
  const WORKSPACE_STORAGE_KEY = 'airmock-active-workspace';

  // Kept in sync with each *Mocks.svelte page's own client-side filter by
  // protocolType — the single /api/mocks endpoint returns every protocol
  // type in one list, so this is what maps a hit back to its page.
  const PROTOCOL_PAGE = { rest: 'mocks', soap: 'mocks', graphql: 'mocks', tcp: 'tcpmocks', smtp: 'smtpmocks', ws: 'wsmocks', mqtt: 'mqttmocks', ftp: 'ftpmocks', kafka: 'kafkamocks', smpp: 'smppmocks', diameter: 'diametermocks', jms: 'jmsmocks' };

  let open = false;
  let query = '';
  let inputEl;
  let highlighted = 0;
  let loaded = false;
  let entries = []; // flattened, searchable: { kind, label, sublabel, action }
  let itemEls = []; // result row elements, indexed the same as `filtered` — lets keyboard nav scroll the highlighted one into view

  // Keeping the highlighted row visible: class:highlighted alone only
  // changes its background, it doesn't move the (overflow-y:auto)
  // .cmdk-results scroll position — with 30 possible matches and a fixed
  // max-height, arrowing down past the visible window previously moved the
  // selection with no visual feedback at all until it wrapped back around.
  async function scrollHighlightedIntoView() {
    await tick(); // wait for class:highlighted's DOM update before measuring
    itemEls[highlighted]?.scrollIntoView({ block: 'nearest' });
  }

  function handleGlobalKeydown(e) {
    const isOpenShortcut = (e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 'k';
    if (isOpenShortcut) {
      e.preventDefault();
      open ? close() : openPalette();
      return;
    }
    if (!open) return;
    if (e.key === 'Escape') {
      e.preventDefault();
      close();
    } else if (e.key === 'ArrowDown') {
      e.preventDefault();
      if (filtered.length) highlighted = (highlighted + 1) % filtered.length;
      scrollHighlightedIntoView();
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      if (filtered.length) highlighted = (highlighted - 1 + filtered.length) % filtered.length;
      scrollHighlightedIntoView();
    } else if (e.key === 'Enter') {
      e.preventDefault();
      if (filtered[highlighted]) select(filtered[highlighted]);
    }
  }

  async function openPalette() {
    open = true;
    query = '';
    highlighted = 0;
    requestAnimationFrame(() => inputEl?.focus());
    if (!loaded) await loadEntries();
  }

  function close() {
    open = false;
  }

  // Loaded once per app session on first open, not on every keystroke —
  // mocks/collections don't change often enough while the palette itself
  // is the thing open to justify a refetch per open, and a stale list for
  // the rest of the session is a fair trade against re-fetching on every
  // Ctrl/Cmd+K.
  async function loadEntries() {
    const out = pages.map((p) => ({ kind: 'page', label: p.label, sublabel: 'Page', action: () => navigate(p.id) }));

    try {
      const mocks = (await api.listMocks()) ?? [];
      for (const m of mocks) {
        const pageId = PROTOCOL_PAGE[m.protocolType] ?? 'mocks';
        out.push({
          kind: 'mock',
          label: m.name,
          sublabel: `${m.protocolType?.toUpperCase() ?? ''} mock`,
          action: () => navigateToItem(pageId, m.id),
        });
      }
    } catch {
      // non-fatal: page navigation and whatever else loaded still works
    }

    try {
      const workspaceId = localStorage.getItem(WORKSPACE_STORAGE_KEY) ?? '';
      const collections = (await api.listCollections(workspaceId)) ?? [];
      for (const c of collections) walkCollectionItems(c.items ?? [], c, out);
    } catch {
      // non-fatal, same reasoning as the mocks fetch above
    }

    entries = out;
    loaded = true;
  }

  function walkCollectionItems(items, collection, out) {
    for (const it of items) {
      if (it.type === 'folder') {
        walkCollectionItems(it.items ?? [], collection, out);
      } else {
        out.push({
          kind: 'request',
          label: it.name,
          sublabel: `${collection.name} · ${it.request?.method ?? 'WS'}`,
          action: () => navigateToItem('collections', it.id, { collectionId: collection.id }),
        });
      }
    }
  }

  function select(entry) {
    close();
    entry.action();
  }

  $: filtered = filterEntries(entries, query);
  $: highlighted = Math.min(highlighted, Math.max(filtered.length - 1, 0));

  function filterEntries(all, q) {
    const needle = q.trim().toLowerCase();
    if (!needle) return all.slice(0, 30);
    // Prefix matches on the label rank above a match found anywhere else
    // (label substring or sublabel) — typing "tcp" should surface a mock
    // literally named "TCP health check" before one whose sublabel merely
    // happens to contain "tcp" (protocol name, collection name, etc.).
    const starts = [];
    const contains = [];
    for (const e of all) {
      const label = e.label.toLowerCase();
      if (label.startsWith(needle)) starts.push(e);
      else if (label.includes(needle) || e.sublabel.toLowerCase().includes(needle)) contains.push(e);
    }
    return [...starts, ...contains].slice(0, 30);
  }

  const KIND_ICON = { page: '▸', mock: '⚡', request: '↗' };
</script>

<svelte:window on:keydown={handleGlobalKeydown} />

{#if open}
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="cmdk-backdrop" on:click={close} role="presentation">
    <!-- svelte-ignore a11y_click_events_have_key_events -->
    <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
    <div class="cmdk-panel" on:click|stopPropagation role="dialog" tabindex="-1" aria-modal="true" aria-label="Quick navigation">
      <input aria-label="Jump to a mock, collection request, or page"
        type="text"
        class="cmdk-input"
        bind:value={query}
        bind:this={inputEl}
        placeholder="Jump to a mock, collection request, or page…"
      />
      <div class="cmdk-results">
        {#if !loaded}
          <p class="sub cmdk-empty"><span class="loader-spin"></span>&nbsp; Loading…</p>
        {:else if filtered.length === 0}
          <p class="sub cmdk-empty">No matches for "{query}".</p>
        {:else}
          {#each filtered as entry, i (entry.kind + entry.label + i)}
            <button
              type="button"
              class="cmdk-item"
              class:highlighted={i === highlighted}
              bind:this={itemEls[i]}
              on:mouseenter={() => (highlighted = i)}
              on:click={() => select(entry)}
            >
              <span class="cmdk-icon">{KIND_ICON[entry.kind]}</span>
              <span class="cmdk-label">{entry.label}</span>
              <span class="cmdk-sublabel">{entry.sublabel}</span>
            </button>
          {/each}
        {/if}
      </div>
      <div class="cmdk-footer">
        <span>↑↓ navigate</span><span>↵ open</span><span>esc close</span>
      </div>
    </div>
  </div>
{/if}

<style>
  .cmdk-backdrop {
    position: fixed; inset: 0; background: rgba(0,0,0,.45);
    display: flex; align-items: flex-start; justify-content: center; padding-top: 12vh;
    z-index: 2000; animation: modal-backdrop-in .15s ease both;
  }
  .cmdk-panel {
    width: 560px; max-width: 92vw; max-height: 60vh; display: flex; flex-direction: column;
    background: var(--card); border: 1px solid var(--border); border-radius: 12px;
    box-shadow: 0 24px 64px rgba(0,0,0,.4); overflow: hidden;
    animation: theme-menu-in .16s cubic-bezier(.22,1,.36,1) both;
  }
  .cmdk-input {
    border: none; border-bottom: 1px solid var(--border); border-radius: 0;
    padding: 14px 16px; font-size: 15px; background: none; color: var(--text);
  }
  .cmdk-input:focus { outline: none; }
  .cmdk-results { overflow-y: auto; padding: 6px; flex: 1; }
  .cmdk-empty { margin: 14px 16px; }
  .cmdk-item {
    display: flex; align-items: center; gap: 10px; width: 100%; text-align: left;
    padding: 9px 10px; border-radius: 8px; background: none; border: none; cursor: pointer;
    color: var(--text); font-size: 13px;
  }
  .cmdk-item.highlighted { background: var(--hover); }
  .cmdk-icon { flex-shrink: 0; width: 16px; text-align: center; color: var(--muted); }
  .cmdk-label { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .cmdk-sublabel { flex-shrink: 0; font-size: 11px; color: var(--muted); max-width: 40%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .cmdk-footer {
    display: flex; gap: 14px; padding: 8px 14px; border-top: 1px solid var(--border);
    font-size: 11px; color: var(--muted); flex-shrink: 0;
  }

  @keyframes modal-backdrop-in { from { opacity: 0; } to { opacity: 1; } }
</style>
