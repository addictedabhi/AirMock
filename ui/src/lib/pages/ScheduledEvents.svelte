<script>
  import { onMount, tick } from 'svelte';
  import { api } from '../api.js';
  import { showToast } from '../toast.js';
  import { copyText } from '../clipboard.js';
  import InfoTooltip from '../InfoTooltip.svelte';
  import ContextMenu from '../ContextMenu.svelte';
  import CsvSourceField from '../CsvSourceField.svelte';

  let events = [];
  let loading = true;
  let searchQuery = '';

  let showForm = false;
  let editingId = ''; // '' means the form is creating a new event, not editing

  let expandedId = ''; // event id currently expanded, '' means none
  let firingId = '';

  function emptyForm() {
    return {
      name: '',
      enabled: true,
      intervalSecs: 30,
      targetUrl: '',
      method: 'POST',
      headersJson: '',
      bodyTemplate: '{\n  "event": "heartbeat",\n  "timestamp": "{{now}}"\n}',
    };
  }
  let form = emptyForm();

  function formFromEvent(e) {
    return {
      name: e.name,
      enabled: e.enabled !== false,
      intervalSecs: e.intervalSecs ?? 30,
      targetUrl: e.targetUrl ?? '',
      method: e.method || 'POST',
      headersJson: e.headers && Object.keys(e.headers).length ? JSON.stringify(e.headers, null, 2) : '',
      bodyTemplate: e.bodyTemplate ?? '',
    };
  }

  async function startEdit(e) {
    editingId = e.id;
    form = formFromEvent(e);
    showForm = true;
    await tick();
    document.getElementById('event-edit-form')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }

  function startCreate() {
    editingId = '';
    form = emptyForm();
    showForm = true;
  }

  function cancelForm() {
    showForm = false;
    editingId = '';
  }

  function matchesSearch(e, query) {
    const needle = query.toLowerCase();
    return (e.name || '').toLowerCase().includes(needle) || (e.targetUrl || '').toLowerCase().includes(needle);
  }
  $: filteredEvents = searchQuery.trim() ? events.filter((e) => matchesSearch(e, searchQuery)) : events;

  async function load() {
    loading = true;
    try {
      events = (await api.listScheduledEvents()) ?? [];
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      loading = false;
    }
  }

  onMount(async () => {
    await load();
  });

  async function saveEvent() {
    // Reject rather than silently rewrite: `Number(form.intervalSecs) || 30`
    // used to turn a typed "0" into 30 with no explanation, while the server
    // correctly rejects intervalSecs<=0 outright — the client should surface
    // the same rule, not paper over it.
    const intervalSecs = Number(form.intervalSecs);
    if (!Number.isFinite(intervalSecs) || intervalSecs <= 0) {
      showToast('Interval (secs) must be a positive number', 'err');
      return;
    }

    let headers;
    if (form.headersJson.trim()) {
      try {
        headers = JSON.parse(form.headersJson);
      } catch {
        showToast('Headers must be valid JSON', 'err');
        return;
      }
    }
    const def = {
      name: form.name,
      enabled: form.enabled,
      intervalSecs,
      targetUrl: form.targetUrl,
      method: form.method,
      headers,
      bodyTemplate: form.bodyTemplate,
    };
    try {
      if (editingId) {
        await api.updateScheduledEvent(editingId, def);
        showToast('Scheduled event updated', 'ok');
      } else {
        await api.createScheduledEvent(def);
        showToast('Scheduled event created', 'ok');
      }
      cancelForm();
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function toggleEnabled(e) {
    try {
      await api.updateScheduledEvent(e.id, { ...e, enabled: !e.enabled });
      await load();
    } catch (err) {
      showToast(err.message, 'err');
    }
  }

  async function removeEvent(id) {
    const e = events.find((x) => x.id === id);
    if (!confirm(`Delete scheduled event "${e?.name ?? id}"? This cannot be undone.`)) return;
    try {
      await api.deleteScheduledEvent(id);
      await load();
    } catch (err) {
      showToast(err.message, 'err');
    }
  }

  async function fireNow(id) {
    firingId = id;
    try {
      await api.fireScheduledEventNow(id);
      showToast('Fired', 'ok');
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      firingId = '';
    }
  }

  function toggleExpand(id) {
    expandedId = expandedId === id ? '' : id;
  }

  let contextMenu = null;
  function openContextMenu(domEvt, ev) {
    domEvt.preventDefault();
    contextMenu = { x: domEvt.clientX, y: domEvt.clientY, event: ev };
  }
  function contextMenuItems(ev) {
    return [
      { label: 'Edit', onClick: () => startEdit(ev) },
      { label: 'Fire now', onClick: () => fireNow(ev.id) },
      { label: ev.enabled ? 'Disable' : 'Enable', onClick: () => toggleEnabled(ev) },
      { label: 'Copy target URL', onClick: () => copyText(ev.targetUrl ?? '').then(() => showToast('Target URL copied', 'ok')) },
      { divider: true },
      { label: 'Delete', danger: true, onClick: () => removeEvent(ev.id) },
    ];
  }
</script>

<div class="head-row">
  <div>
    <h1>Scheduled Events</h1>
    <p class="sub">Fires an outbound HTTP callback on a fixed interval, independent of any incoming request — for simulating periodic push traffic (IoT readings, subscription renewals, heartbeats) a consuming app has to handle on its own.</p>
  </div>
  <button class="btn btn-primary" on:click={() => (showForm ? cancelForm() : startCreate())}>
    {showForm ? 'Cancel' : '+ New scheduled event'}
  </button>
</div>

{#if showForm}
  <div class="card form-card" id="event-edit-form">
    <h3 class="form-title">{editingId ? 'Edit scheduled event' : 'New scheduled event'}</h3>
    <div class="field-row">
      <label>
        Name
        <input type="text" bind:value={form.name} placeholder="heartbeat-ping" />
      </label>
      <label class="port-field">
        <span class="label-text">Interval (secs)<InfoTooltip text="How often this fires, from the moment it's saved. Editing any field restarts the countdown." /></span>
        <input type="number" min="1" bind:value={form.intervalSecs} />
      </label>
    </div>
    <div class="field-row">
      <label class="method-field">
        Method
        <select bind:value={form.method}>
          <option value="POST">POST</option>
          <option value="PUT">PUT</option>
          <option value="PATCH">PATCH</option>
          <option value="GET">GET</option>
          <option value="DELETE">DELETE</option>
        </select>
      </label>
      <label>
        Target URL
        <input type="text" bind:value={form.targetUrl} placeholder="https://example.com/webhook" />
      </label>
    </div>
    <label>
      <span class="label-text">Body template<InfoTooltip text={`Rendered via Go templates (text/template + sprig) — there's no triggering request, so {{.Request.*}} isn't available, but {{now}}, {{fake "uuid"}}, {{randInt 1 100}} etc. all work. {{counter "name"}} auto-increments (or {{counter "name" -1}} to decrement) and persists across restarts; {{csv "column"}} pulls from an attached CSV.`} /></span>
      <textarea rows="6" bind:value={form.bodyTemplate}></textarea>
    </label>
    {#if editingId}
      <CsvSourceField ownerPath={`/api/scheduled-events/${editingId}`} />
    {:else}
      <p class="sub">Save this event first, then come back to attach a CSV data source for {'{{csv "column"}}'}.</p>
    {/if}
    <label>
      Headers (JSON, optional)
      <textarea rows="2" bind:value={form.headersJson} placeholder={'{"Authorization": "Bearer ..."}'}></textarea>
    </label>
    <label class="rule-required">
      <input type="checkbox" bind:checked={form.enabled} />
      Enabled
    </label>
    <button class="btn btn-primary" on:click={saveEvent}>{editingId ? 'Save changes' : 'Create scheduled event'}</button>
  </div>
{/if}

<div class="projects-bar">
  <input aria-label="Search scheduled events by name or target URL" type="text" class="search-input" bind:value={searchQuery} placeholder="Search scheduled events by name or target URL…" />
</div>

{#if loading}
  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading…</p>
{:else if events.length === 0}
  <div class="card empty">
    <span class="chip chip-stop">no scheduled events</span>
    <p>No scheduled events configured yet — create one above.</p>
  </div>
{:else if searchQuery.trim() && filteredEvents.length === 0}
  <div class="card empty">
    <span class="chip badge-warn">no matches</span>
    <p>No scheduled events match "{searchQuery}".</p>
  </div>
{:else}
  <div class="mock-list">
    {#each filteredEvents as e (e.id)}
      <div class="card mock-card row-enter">
        <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
        <div
          class="mock-row"
          on:click={() => toggleExpand(e.id)}
          on:contextmenu={(domEvt) => openContextMenu(domEvt, e)}
        >
          <button type="button" class="expand-arrow" class:expanded={expandedId === e.id} aria-expanded={!!(expandedId === e.id)} aria-label="Show or hide details" on:click|stopPropagation={() => toggleExpand(e.id)}>▸</button>
          <span class="badge badge-info">{e.method}</span>
          <span class="name">{e.name}</span>
          <span class="chip chip-tls">every {e.intervalSecs}s</span>
          {#if e.lastStatus}<span class="chip {e.lastStatus < 400 ? 'badge-ok' : 'badge-err'}">last: {e.lastStatus}</span>{/if}
          {#if e.lastError}<span class="chip badge-err">error</span>{/if}
          <button class="chip {e.enabled ? 'chip-run' : 'chip-stop'}" on:click|stopPropagation={() => toggleEnabled(e)}>
            {e.enabled ? 'enabled' : 'disabled'}
          </button>
          <div class="row-actions">
            <button class="btn btn-ghost" on:click|stopPropagation={() => fireNow(e.id)} disabled={firingId === e.id}>{firingId === e.id ? 'Firing…' : 'Fire now'}</button>
            <button class="btn btn-ghost" on:click|stopPropagation={() => startEdit(e)}>Edit</button>
            <button class="btn btn-ghost remove" on:click|stopPropagation={() => removeEvent(e.id)}>Delete</button>
          </div>
        </div>

        {#if expandedId === e.id}
          <div class="mock-detail">
            <div class="detail-section">
              <h4>Configuration</h4>
              <div class="detail-row"><span class="detail-label">Target</span><code>{e.method} {e.targetUrl}</code></div>
              <div class="detail-row"><span class="detail-label">Interval</span><code>{e.intervalSecs}s</code></div>
              <div class="detail-row"><span class="detail-label">Next fire</span><code>{new Date(e.nextFireAt).toLocaleString()}</code></div>
              {#if e.lastFiredAt}
                <div class="detail-row"><span class="detail-label">Last fired</span><code>{new Date(e.lastFiredAt).toLocaleString()}{e.lastStatus ? ` — status ${e.lastStatus}` : ''}</code></div>
              {/if}
              {#if e.lastError}
                <div class="detail-row"><span class="detail-label">Last error</span><code>{e.lastError}</code></div>
              {/if}
            </div>
            <div class="detail-section">
              <h4>Body template</h4>
              <pre class="json-block">{e.bodyTemplate}</pre>
            </div>
          </div>
        {/if}
      </div>
    {/each}
  </div>
{/if}

{#if contextMenu}
  <ContextMenu x={contextMenu.x} y={contextMenu.y} items={contextMenuItems(contextMenu.event)} onClose={() => (contextMenu = null)} />
{/if}

<style>
  .head-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
  .sub { color: var(--muted); font-size: 13px; margin-top: 4px; }
  .form-card { margin-top: 16px; display: flex; flex-direction: column; gap: 12px; max-width: 640px; }
  .form-title { margin: 0; font-size: 14px; }
  .field-row { display: flex; gap: 12px; }
  .port-field { flex: 0 0 160px; }
  .method-field { flex: 0 0 120px; }
  label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: .3px; flex: 1; }
  .label-text { display: inline-flex; align-items: center; }
  .rule-required { flex-direction: row; align-items: center; gap: 6px; }
  input, select, textarea {
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 8px 10px; font-size: 13px; font-family: inherit; outline: none;
    transition: border .2s;
  }
  textarea { font-family: 'SFMono-Regular', Consolas, monospace; resize: vertical; }
  input:focus, select:focus, textarea:focus { border-color: var(--primary); }

  .projects-bar { display: flex; gap: 10px; margin-top: 14px; align-items: center; }
  .search-input {
    flex: 1; max-width: 360px; background: var(--surface2, var(--hover)); border: 1.5px solid var(--border);
    color: var(--text); border-radius: 6px; padding: 8px 10px; font-size: 13px; font-family: inherit; outline: none;
    transition: border .2s;
  }
  .search-input:focus { border-color: var(--primary); }

  .mock-list { margin-top: 16px; display: flex; flex-direction: column; gap: 10px; }
  .mock-card { padding: 0; overflow: hidden; }
  .mock-row { display: flex; align-items: center; gap: 14px; padding: 14px 18px; cursor: pointer; }
  .name { font-family: 'SFMono-Regular', Consolas, monospace; font-size: 13px; }
  .row-actions { display: flex; gap: 8px; margin-left: auto; }
  .empty { margin-top: 20px; display: flex; flex-direction: column; gap: 10px; align-items: flex-start; }

  .expand-arrow { display: inline-block; font-size: 11px; color: var(--muted); transition: transform .2s cubic-bezier(.4,0,.2,1); flex-shrink: 0; }
  .expand-arrow.expanded { transform: rotate(90deg); }

  .mock-detail { padding: 16px 18px 18px 46px; border-top: 1px solid var(--border); display: flex; flex-direction: column; gap: 18px; }
  .detail-section h4 { margin: 0 0 8px; font-size: 12px; text-transform: uppercase; letter-spacing: .3px; color: var(--muted); }
  .detail-row { display: flex; align-items: baseline; gap: 10px; font-size: 13px; margin-bottom: 6px; }
  .detail-row:last-child { margin-bottom: 0; }
  .detail-label { flex: 0 0 100px; color: var(--muted); font-size: 12px; font-weight: 600; }
  .detail-row code {
    font-family: 'SFMono-Regular', Consolas, monospace; font-size: 12px;
    background: var(--surface2, var(--hover)); border-radius: 4px; padding: 2px 6px;
    white-space: pre-wrap; word-break: break-word; margin: 0;
  }
  .json-block {
    font-family: 'SFMono-Regular', Consolas, monospace; font-size: 12px;
    background: var(--surface2, var(--hover)); border-radius: 6px; padding: 10px 12px;
    white-space: pre-wrap; word-break: break-word; margin: 0;
  }
</style>
