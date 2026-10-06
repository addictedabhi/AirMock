<script>
  import { onMount, onDestroy } from 'svelte';
  import { api, hitLogExportUrl } from '../api.js';
  import { showToast } from '../toast.js';
  import { copyText } from '../clipboard.js';
  import ContextMenu from '../ContextMenu.svelte';

  const DIRECTIONS = [
    { value: '', label: 'All directions' },
    { value: 'inbound', label: 'Mock hit' },
    { value: 'proxy-capture', label: 'Proxy capture' },
    { value: 'outbound-client-call', label: 'API client call' },
    { value: 'callback', label: 'Async callback' },
  ];
  const STATUS_CLASSES = [
    { value: '', label: 'All statuses' },
    { value: '2xx', label: '2xx success' },
    { value: '3xx', label: '3xx redirect' },
    { value: '4xx', label: '4xx client error' },
    { value: '5xx', label: '5xx server error' },
    { value: 'none', label: 'No status (error)' },
  ];
  // Fixed rather than derived from whatever's currently loaded — deriving
  // them from `entries` meant applying any one filter shrank the OTHER
  // dropdowns' own options down to just what survived that filter, so you
  // could never discover (or switch to) a protocol/method that wasn't in
  // the current filtered result.
  const PROTOCOLS = ['rest', 'soap', 'graphql', 'tcp'];
  const METHODS = ['GET', 'POST', 'PUT', 'PATCH', 'DELETE', 'HEAD', 'OPTIONS'];

  let entries = [];
  let mocks = [];
  let loading = true;
  let expandedId = '';
  let live = false;
  // Collapsed by default only below the same 640px breakpoint the rest of
  // this file's mobile fixes use — nine filter fields stacked one-per-row
  // push the actual log entries below the fold on a phone; on desktop the
  // filter row already fits comfortably, so it stays open there by default.
  let filtersExpanded = typeof window === 'undefined' || window.innerWidth > 640;
  let ws = null;
  let searchDebounceTimer = null;

  // datetime-local wants "YYYY-MM-DDTHH:mm" in LOCAL time — no timezone
  // conversion, unlike toRFC3339 below which is for the outgoing request.
  function todayStartLocal() {
    const d = new Date();
    const pad = (n) => String(n).padStart(2, '0');
    return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T00:00`;
  }

  // All filters are server-side now: every one of them is passed to the
  // query so a filter combination sees the whole table, not just whatever
  // page happened to already be loaded client-side.
  let filterMockId = '';
  // Defaults to today only — a long-running instance can accumulate weeks
  // of hits, and showing all of them by default meant the most likely
  // thing anyone actually wants (what just happened) was buried under
  // everything else. "Show all history" below expands back out to
  // everything.
  let filterSince = todayStartLocal();
  let filterUntil = '';
  let filterLimit = 200;
  let filterDirection = '';
  let filterProtocol = '';
  let filterMethod = '';
  let filterStatusClass = '';
  let filterSearch = '';

  // filterSince defaults to "today only" rather than empty (see below), so
  // it no longer belongs in this check — otherwise every page load would
  // count as "filters active" and permanently show "Reset filters" plus
  // misattribute an ordinary quiet day to "no entries match your filters."
  $: hasActiveFilters =
    filterMockId || (filterSince && !scopedToToday) || filterUntil || filterDirection || filterProtocol || filterMethod || filterStatusClass || filterSearch;

  // Same filter set load() sends — deliberately without filterLimit: an
  // export should cover everything matching the filters, not just however
  // many rows the on-screen table happens to be capped at.
  $: exportOpts = {
    mockId: filterMockId,
    since: toRFC3339(filterSince),
    until: toRFC3339(filterUntil),
    protocolType: filterProtocol,
    direction: filterDirection,
    method: filterMethod,
    statusClass: filterStatusClass,
    search: filterSearch.trim(),
  };

  function onSearchInput() {
    clearTimeout(searchDebounceTimer);
    searchDebounceTimer = setTimeout(load, 350);
  }

  function mockName(id) {
    return mocks.find((m) => m.id === id)?.name ?? id;
  }

  function toRFC3339(datetimeLocal) {
    if (!datetimeLocal) return '';
    const d = new Date(datetimeLocal);
    return Number.isNaN(d.getTime()) ? '' : d.toISOString();
  }

  async function load() {
    loading = true;
    try {
      entries =
        (await api.listHitLog({
          mockId: filterMockId,
          since: toRFC3339(filterSince),
          until: toRFC3339(filterUntil),
          protocolType: filterProtocol,
          direction: filterDirection,
          method: filterMethod,
          statusClass: filterStatusClass,
          search: filterSearch.trim(),
          limit: filterLimit,
        })) ?? [];
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      loading = false;
    }
  }

  async function loadMocks() {
    try {
      mocks = (await api.listMocks()) ?? [];
    } catch {
      // non-fatal: the mock filter dropdown just falls back to raw IDs
    }
  }

  onMount(async () => {
    await Promise.all([load(), loadMocks()]);
  });

  function resetFilters() {
    filterMockId = '';
    filterSince = todayStartLocal();
    filterUntil = '';
    filterDirection = '';
    filterProtocol = '';
    filterMethod = '';
    filterStatusClass = '';
    filterSearch = '';
    load();
  }

  $: scopedToToday = filterSince === todayStartLocal() && !filterUntil;

  function showAllHistory() {
    filterSince = '';
    filterUntil = '';
    load();
  }
  function scopeToToday() {
    filterSince = todayStartLocal();
    filterUntil = '';
    load();
  }

  // The live tail is a firehose of every new hit, unfiltered by the
  // server (it's a push subscription, not a query) — mirrors the same
  // filters load() sends so a filtered view doesn't suddenly get
  // unfiltered rows appended the moment "Live" is toggled on.
  function matchesFilters(e) {
    if (filterMockId && e.mockId !== filterMockId) return false;
    if (filterDirection && e.direction !== filterDirection) return false;
    if (filterProtocol && e.protocolType !== filterProtocol) return false;
    if (filterMethod && e.method !== filterMethod) return false;
    if (filterStatusClass) {
      if (filterStatusClass === 'none') {
        if (e.responseStatus) return false;
      } else if (!e.responseStatus || String(e.responseStatus)[0] !== filterStatusClass[0]) {
        return false;
      }
    }
    if (filterSearch.trim()) {
      const needle = filterSearch.trim().toLowerCase();
      const haystack = [e.path, e.targetUrl, e.requestBody, e.responseBody, e.method].filter(Boolean).join(' \n ').toLowerCase();
      if (!haystack.includes(needle)) return false;
    }
    return true;
  }

  function toggleLive() {
    live = !live;
    if (live) {
      const proto = location.protocol === 'https:' ? 'wss' : 'ws';
      ws = new WebSocket(`${proto}://${location.host}/api/hitlog/tail`);
      ws.onmessage = (ev) => {
        const entry = JSON.parse(ev.data);
        if (!matchesFilters(entry)) return;
        entries = [entry, ...entries].slice(0, 200);
      };
      ws.onerror = () => showToast('Live tail connection error', 'err');
    } else if (ws) {
      ws.close();
      ws = null;
    }
  }
  onDestroy(() => {
    ws?.close();
    clearTimeout(searchDebounceTimer);
  });

  async function promote(id) {
    try {
      await api.promoteToMock(id);
      showToast('Promoted to a real mock — see the Mocks page', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function deleteEntry(id) {
    if (!confirm('Delete this log entry? This cannot be undone.')) return;
    try {
      await api.deleteHitLogEntry(id);
      entries = entries.filter((e) => e.id !== id);
      if (expandedId === id) expandedId = '';
      showToast('Log entry deleted', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  let contextMenu = null;
  function openContextMenu(domEvt, entry) {
    domEvt.preventDefault();
    contextMenu = { x: domEvt.clientX, y: domEvt.clientY, entry };
  }
  function contextMenuItems(entry) {
    const items = [];
    if (entry.direction === 'proxy-capture') {
      items.push({ label: 'Promote to mock', onClick: () => promote(entry.id) });
    }
    items.push({ label: 'Copy path/target', onClick: () => copyText(entry.path || entry.targetUrl || '').then(() => showToast('Copied', 'ok')) });
    if (entry.mockId) {
      items.push({ label: 'Copy mock name', onClick: () => copyText(mockName(entry.mockId)).then(() => showToast('Mock name copied', 'ok')) });
    }
    items.push({ divider: true });
    items.push({ label: 'Delete', danger: true, onClick: () => deleteEntry(entry.id) });
    return items;
  }

  // filterSummary describes exactly what "matches the current filters"
  // means in plain words, for the delete-matching confirm dialog — a bare
  // "delete everything?" prompt gives no chance to notice a broader filter
  // (or an accidentally-cleared one) before an unrecoverable bulk delete.
  function filterSummary() {
    const parts = [];
    parts.push(scopedToToday ? 'today only' : filterSince || filterUntil ? `${filterSince || 'the beginning'} through ${filterUntil || 'now'}` : 'all time');
    if (filterMockId) parts.push(`mock "${mockName(filterMockId)}"`);
    if (filterDirection) parts.push(`direction "${DIRECTIONS.find((d) => d.value === filterDirection)?.label ?? filterDirection}"`);
    if (filterProtocol) parts.push(`protocol "${filterProtocol}"`);
    if (filterMethod) parts.push(`method "${filterMethod}"`);
    if (filterStatusClass) parts.push(`status "${STATUS_CLASSES.find((s) => s.value === filterStatusClass)?.label ?? filterStatusClass}"`);
    if (filterSearch.trim()) parts.push(`search "${filterSearch.trim()}"`);
    return parts.join(', ');
  }

  async function deleteMatching() {
    if (!confirm(`Delete every log entry matching: ${filterSummary()}?\n\nThis cannot be undone.`)) return;
    try {
      const { deleted } = await api.deleteHitLogMatching(exportOpts);
      showToast(`Deleted ${deleted} log ${deleted === 1 ? 'entry' : 'entries'}`, 'ok');
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  function statusClass(code) {
    if (!code) return 'badge-err';
    return code < 300 ? 'badge-ok' : code < 400 ? 'badge-info' : 'badge-err';
  }
  function directionChip(dir) {
    return dir === 'proxy-capture' ? 'chip-tls' : dir === 'outbound-client-call' ? 'chip-stat' : 'chip-run';
  }

  // Pretty-prints a JSON body for readability; falls back to the raw text
  // untouched for a non-JSON (or empty) body rather than erroring.
  function formatBody(text) {
    if (!text) return '';
    try {
      return JSON.stringify(JSON.parse(text), null, 2);
    } catch {
      return text;
    }
  }
</script>

<div class="head-row">
  <div>
    <h1>Log History</h1>
    <p class="sub">
      {#if scopedToToday}
        Showing today's hits only.
        <button class="link-btn" on:click={showAllHistory}>Show all history</button>
      {:else}
        Showing all history.
        <button class="link-btn" on:click={scopeToToday}>Show today only</button>
      {/if}
    </p>
  </div>
  <button class="btn {live ? 'btn-primary' : 'btn-ghost'}" on:click={toggleLive}>
    <span class="dot-alive" class:paused={!live}></span>&nbsp; {live ? 'Live' : 'Go live'}
  </button>
</div>

<div class="card filter-card">
  <button
    type="button"
    class="filter-toggle"
    on:click={() => (filtersExpanded = !filtersExpanded)}
    aria-expanded={filtersExpanded}
  >
    <span class="filter-toggle-arrow" class:expanded={filtersExpanded}>▸</span>
    Filters
    {#if !filtersExpanded && hasActiveFilters}<span class="chip chip-stat">active</span>{/if}
  </button>
  {#if filtersExpanded}
  <div class="filter-row">
    <label class="filter-field flex-1">
      Search
      <input type="text" bind:value={filterSearch} on:input={onSearchInput} placeholder="path, body, target URL…" />
    </label>
    <label class="filter-field">
      Mock
      <select bind:value={filterMockId} on:change={load}>
        <option value="">All mocks</option>
        {#each mocks as m (m.id)}<option value={m.id}>{m.name}</option>{/each}
      </select>
    </label>
    <label class="filter-field">
      Direction
      <select bind:value={filterDirection} on:change={load}>
        {#each DIRECTIONS as d}<option value={d.value}>{d.label}</option>{/each}
      </select>
    </label>
    <label class="filter-field">
      Protocol
      <select bind:value={filterProtocol} on:change={load}>
        <option value="">All protocols</option>
        {#each PROTOCOLS as p}<option value={p}>{p}</option>{/each}
      </select>
    </label>
    <label class="filter-field">
      Method
      <select bind:value={filterMethod} on:change={load}>
        <option value="">All methods</option>
        {#each METHODS as m}<option value={m}>{m}</option>{/each}
      </select>
    </label>
    <label class="filter-field">
      Status
      <select bind:value={filterStatusClass} on:change={load}>
        {#each STATUS_CLASSES as s}<option value={s.value}>{s.label}</option>{/each}
      </select>
    </label>
  </div>
  <div class="filter-row">
    <label class="filter-field">
      Since
      <input type="datetime-local" bind:value={filterSince} on:change={load} />
    </label>
    <label class="filter-field">
      Until
      <input type="datetime-local" bind:value={filterUntil} on:change={load} />
    </label>
    <label class="filter-field">
      Load up to
      <select bind:value={filterLimit} on:change={load}>
        <option value={200}>200 rows</option>
        <option value={500}>500 rows</option>
        <option value={1000}>1000 rows</option>
      </select>
    </label>
    {#if hasActiveFilters}
      <button class="btn btn-ghost small" on:click={resetFilters}>Reset filters</button>
    {/if}
    <span class="sub filter-count">{entries.length} matching{entries.length === filterLimit ? ' (more may exist)' : ''}</span>
    <span class="export-links">
      Export:
      <a class="btn btn-ghost small" href={hitLogExportUrl(exportOpts, 'csv')} download title="Every entry matching the current filters, not just what's loaded on screen">CSV</a>
      <a class="btn btn-ghost small" href={hitLogExportUrl(exportOpts, 'json')} download title="Every entry matching the current filters, not just what's loaded on screen">JSON</a>
    </span>
    <button
      class="btn btn-ghost small"
      on:click={deleteMatching}
      title="Delete every log entry matching the current filters, not just what's loaded on screen"
    >
      Delete matching
    </button>
  </div>
  {/if}
</div>

{#if loading}
  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading…</p>
{:else if entries.length === 0 && hasActiveFilters}
  <div class="card empty"><p>No log entries match the current filters.</p></div>
{:else if entries.length === 0 && scopedToToday}
  <div class="card empty">
    <p>No hits yet today. <button class="link-btn" on:click={showAllHistory}>Show all history</button> to see older entries.</p>
  </div>
{:else if entries.length === 0}
  <div class="card empty"><p>No hits recorded yet — hit a mock, proxy through one, or call something from the API client.</p></div>
{:else}
  <div class="log-list">
    {#each entries as e (e.id)}
      <div class="card log-row row-enter">
        <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
        <div
          class="log-summary"
          on:click={() => (expandedId = expandedId === e.id ? '' : e.id)}
          on:contextmenu={(domEvt) => openContextMenu(domEvt, e)}
        >
          <button
            type="button"
            class="expand-arrow"
            class:expanded={expandedId === e.id}
            aria-expanded={expandedId === e.id}
            aria-label="Show or hide request and response details"
            on:click|stopPropagation={() => (expandedId = expandedId === e.id ? '' : e.id)}
          >▸</button>
          <span class="chip {directionChip(e.direction)}">{e.direction}</span>
          {#if e.protocolType === 'tcp'}
            <span class="chip badge-info">tcp</span>
          {:else}
            <span class="method">{e.method}</span>
          {/if}
          <span class="path">{e.path || e.targetUrl || e.requestBody}</span>
          {#if e.mockId}<span class="chip chip-stat">{mockName(e.mockId)}</span>{/if}
          {#if e.responseStatus}
            <span class="badge {statusClass(e.responseStatus)}">{e.responseStatus}</span>
          {/if}
          <span class="sub latency">{e.latencyMs}ms</span>
          <span class="sub time">{new Date(e.createdAt).toLocaleString()}</span>
          {#if e.direction === 'proxy-capture'}
            <button class="btn btn-primary small" on:click|stopPropagation={() => promote(e.id)}>Promote to mock</button>
          {/if}
          <button class="btn btn-ghost small" on:click|stopPropagation={() => deleteEntry(e.id)} title="Delete this entry">Delete</button>
        </div>
        {#if expandedId === e.id}
          <div class="log-detail">
            {#if e.targetUrl}
              <div class="detail-row"><span class="detail-label">Target</span><code>{e.targetUrl}</code></div>
            {/if}

            <div class="detail-section">
              <h4>Request</h4>
              {#if Object.keys(e.requestHeaders ?? {}).length}
                <div class="kv-block">
                  {#each Object.entries(e.requestHeaders) as [k, v]}
                    <div class="kv-row"><span class="kv-key">{k}</span><span class="kv-val">{v}</span></div>
                  {/each}
                </div>
              {:else}
                <p class="sub">No headers recorded.</p>
              {/if}
              {#if e.requestBody}
                <pre class="resp-body">{formatBody(e.requestBody)}</pre>
              {:else}
                <p class="sub">No body.</p>
              {/if}
            </div>

            <div class="detail-section">
              <h4>Response</h4>
              {#if Object.keys(e.responseHeaders ?? {}).length}
                <div class="kv-block">
                  {#each Object.entries(e.responseHeaders) as [k, v]}
                    <div class="kv-row"><span class="kv-key">{k}</span><span class="kv-val">{v}</span></div>
                  {/each}
                </div>
              {:else}
                <p class="sub">No headers recorded.</p>
              {/if}
              {#if e.responseBody}
                <pre class="resp-body">{formatBody(e.responseBody)}</pre>
              {:else}
                <p class="sub">No body.</p>
              {/if}
            </div>
          </div>
        {/if}
      </div>
    {/each}
  </div>
{/if}

{#if contextMenu}
  <ContextMenu x={contextMenu.x} y={contextMenu.y} items={contextMenuItems(contextMenu.entry)} onClose={() => (contextMenu = null)} />
{/if}

<style>
  .head-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
  .sub { color: var(--muted); font-size: 13px; margin-top: 4px; }
  .link-btn {
    background: none; border: none; padding: 0; margin-left: 4px; font: inherit;
    color: var(--primary); font-weight: 600; cursor: pointer; text-decoration: underline;
  }
  .empty { margin-top: 20px; }
  .log-list { margin-top: 20px; display: flex; flex-direction: column; gap: 10px; }
  .log-row { padding: 0; overflow: hidden; }
  .expand-arrow { display: inline-block; font-size: 11px; color: var(--muted); transition: transform .2s cubic-bezier(.4,0,.2,1); flex-shrink: 0; }
  .expand-arrow.expanded { transform: rotate(90deg); }
  .log-summary { display: flex; align-items: center; gap: 12px; padding: 12px 16px; cursor: pointer; }
  .method { font-weight: 700; font-size: 12px; }
  .path { font-family: 'SFMono-Regular', Consolas, monospace; font-size: 13px; flex: 1; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .latency, .time { flex-shrink: 0; }
  .btn.small { padding: 5px 10px; font-size: 12px; }
  .log-detail { padding: 12px 16px; border-top: 1px solid var(--border); font-size: 13px; }
  .resp-body { background: var(--surface2, var(--hover)); border-radius: 6px; padding: 10px; font-size: 12px; overflow-x: auto; white-space: pre-wrap; word-break: break-word; margin-top: 8px; font-family: 'SFMono-Regular', Consolas, monospace; }
  .detail-row { display: flex; gap: 8px; margin-bottom: 10px; }
  .detail-label { font-size: 11px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: .3px; flex-shrink: 0; }
  .detail-section { margin-top: 12px; }
  .detail-section h4 { margin: 0 0 6px; font-size: 12px; text-transform: uppercase; letter-spacing: .3px; color: var(--muted); }
  .kv-block { display: flex; flex-direction: column; gap: 3px; }
  .kv-row { display: flex; gap: 10px; font-size: 12px; font-family: 'SFMono-Regular', Consolas, monospace; }
  .kv-key { color: var(--muted); flex: 0 0 auto; min-width: 140px; }
  .kv-val { color: var(--text); word-break: break-word; }
  .dot-alive.paused { animation: none; background: var(--muted); }

  .filter-card { margin-top: 16px; display: flex; flex-direction: column; gap: 10px; }
  .filter-row { display: flex; align-items: flex-end; gap: 12px; flex-wrap: wrap; }
  .filter-field {
    display: flex; flex-direction: column; gap: 4px; font-size: 11px; font-weight: 700;
    color: var(--muted); text-transform: uppercase; letter-spacing: .3px;
  }
  .flex-1 { flex: 1; min-width: 160px; }
  .filter-field input, .filter-field select {
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 7px 10px; font-size: 13px; font-weight: 500; font-family: inherit; outline: none;
  }
  .filter-field input:focus, .filter-field select:focus { border-color: var(--primary); }
  .filter-count { margin-left: auto; align-self: center; flex-shrink: 0; }
  .export-links { display: flex; align-items: center; gap: 6px; flex-shrink: 0; font-size: 12px; color: var(--muted); }
  .export-links a { text-decoration: none; }

  .filter-toggle {
    display: flex; align-items: center; gap: 8px; background: none; border: none; padding: 0;
    font-size: 13px; font-weight: 700; color: var(--text); cursor: pointer; align-self: flex-start;
  }
  .filter-toggle-arrow { display: inline-block; font-size: 11px; color: var(--muted); transition: transform .15s ease; }
  .filter-toggle-arrow.expanded { transform: rotate(90deg); }

  /* Deliberately last: at this row's narrowest, .method/.chip(direction)/
     .chip-stat(mock name)/.badge(status)/.latency/.time already crowd out
     .path's flex:1 down to 0 width — same "flexible item gets 0 leftover
     space" bug already fixed on Dashboard's .activity-path and the mock
     pages' .mock-row .name/.path, same fix (its own full-width line via
     order+flex-basis). .chip-stat gets its own guard here too: unlike a
     short protocol/status chip, a mock's NAME has no length limit, and a
     .chip has no white-space:nowrap of its own — under width pressure it
     was wrapping internally into an ugly multi-line pill instead of
     truncating, which is what actually broke this row's alignment. */
  @media (max-width: 640px) {
    .log-summary { flex-wrap: wrap; row-gap: 6px; }
    .path {
      order: -1; flex: 1 1 100%;
      white-space: normal; overflow-wrap: anywhere; overflow: visible; text-overflow: clip;
    }
    .log-summary .chip-stat {
      max-width: 100%; overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
    }
  }
</style>
