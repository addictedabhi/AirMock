<script>
  import { onMount, tick } from 'svelte';
  import { api } from '../api.js';
  import { showToast } from '../toast.js';
  import { copyText } from '../clipboard.js';
  import InfoTooltip from '../InfoTooltip.svelte';
  import UsageSnippet from '../UsageSnippet.svelte';
  import VersionHistory from '../VersionHistory.svelte';
  import SessionsPanel from '../SessionsPanel.svelte';
  import { usageForJms } from '../mockUsage.js';
  import { pendingFocus, consumePendingFocus } from '../router.js';
  import WorkspaceUnlockModal from '../WorkspaceUnlockModal.svelte';
  import { runWithWorkspaceUnlock } from '../workspaceLock.js';
  import ContextMenu from '../ContextMenu.svelte';
  import CsvSourceField from '../CsvSourceField.svelte';

  let mocks = [];
  // Collections workspaces — an optional mapping (workspaceId) that gates
  // editing/deleting a mock behind that workspace's own lock, if it has
  // one. See workspaceLock.js.
  let workspaces = [];
  let pendingUnlock = null;
  let loading = true;
  let searchQuery = '';

  function matchesSearch(m, query) {
    const needle = query.toLowerCase();
    return (m.name || '').toLowerCase().includes(needle) || String(m.jms?.port ?? '').includes(needle);
  }
  $: filteredMocks = searchQuery.trim() ? mocks.filter((m) => matchesSearch(m, searchQuery)) : mocks;

  let showForm = false;
  let editingId = ''; // '' means the form is creating a new mock, not editing

  let expandedId = ''; // mock id currently expanded, '' means none
  let hitsByMock = {}; // mockId -> hit-log entries, fetched fresh on every expand
  let hitsLoading = '';
  let versionsByMock = {}; // mockId -> version history, fetched fresh on every expand
  let restoringVersionId = '';

  function emptyRule() {
    return { addressPattern: '', payloadMatch: '', matchType: 'contains', replyAddress: '', replyPayload: '' };
  }

  let form = emptyForm();
  function emptyForm() {
    return {
      name: '',
      port: 5672,
      rules: [{ addressPattern: 'orders', payloadMatch: '', matchType: 'contains', replyAddress: 'receipts', replyPayload: '{"status":"ok"}' }],
      workspaceId: '',
    };
  }

  function formFromMock(m) {
    return {
      name: m.name,
      port: m.jms?.port ?? 5672,
      rules: (m.jms?.rules ?? []).map((r) => ({ ...r })),
      workspaceId: m.workspaceId ?? '',
    };
  }

  async function startEdit(m) {
    editingId = m.id;
    form = formFromMock(m);
    showForm = true;
    // The edit form renders above the (potentially long) mock list, so
    // clicking Edit on a mock scrolled far down the page would otherwise
    // silently open the form off-screen with no visible change at all —
    // wait for Svelte to actually render it, then bring it into view.
    await tick();
    document.getElementById('mock-edit-form')?.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }

  function startCreate() {
    editingId = '';
    form = emptyForm();
    showForm = true;
  }

  // Opens the create form pre-filled from an existing mock — not editingId,
  // so saving creates a brand-new mock rather than overwriting the
  // original. The port is bumped by 1 since two enabled JMS mocks can't
  // share a listener port; the user can still change it before saving.
  function duplicateMock(m) {
    editingId = '';
    form = formFromMock(m);
    form.name = `${form.name} (copy)`;
    form.port = Number(form.port) + 1;
    showForm = true;
  }

  function cancelForm() {
    showForm = false;
    editingId = '';
  }

  function addRule() {
    form.rules = [...form.rules, emptyRule()];
  }
  function removeRule(i) {
    form.rules = form.rules.filter((_, idx) => idx !== i);
  }

  async function load() {
    loading = true;
    try {
      mocks = ((await api.listMocks()) ?? []).filter((m) => m.protocolType === 'jms');
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      loading = false;
    }
  }

  async function loadWorkspaces() {
    try {
      workspaces = (await api.listWorkspaces()) ?? [];
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  onMount(async () => {
    await load();
    loadWorkspaces();
  });

  // Reactive rather than a one-shot onMount check — see TCPMocks.svelte
  // for why (a same-page palette jump doesn't remount this page).
  $: if ($pendingFocus?.pageId === 'jmsmocks' && mocks.length) {
    const focus = consumePendingFocus('jmsmocks');
    if (focus) expandedId = focus.itemId;
  }

  async function saveMock() {
    try {
      const def = {
        name: form.name || `jms:${form.port}`,
        protocolType: 'jms',
        workspaceId: form.workspaceId,
        jms: {
          port: Number(form.port),
          rules: form.rules
            .filter((r) => r.addressPattern.trim())
            .map((r) => ({
              addressPattern: r.addressPattern,
              payloadMatch: r.payloadMatch,
              matchType: r.matchType,
              replyAddress: r.replyAddress,
              replyPayload: r.replyPayload,
            })),
        },
      };
      await runWithWorkspaceUnlock(
        () => {
          if (editingId) {
            // Preserve the mock's current enabled/disabled state — editing
            // fields like the port or a rule shouldn't silently re-enable a
            // mock the user deliberately turned off.
            const existing = mocks.find((m) => m.id === editingId);
            return api.updateMock(editingId, { ...existing, ...def });
          }
          return api.createMock({ ...def, enabled: true });
        },
        (p) => (pendingUnlock = p)
      );
      showToast(editingId ? 'JMS mock updated' : 'JMS mock created', 'ok');
      cancelForm();
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function toggleEnabled(m) {
    try {
      await runWithWorkspaceUnlock(() => api.updateMock(m.id, { ...m, enabled: !m.enabled }), (p) => (pendingUnlock = p));
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function removeMock(id) {
    const m = mocks.find((x) => x.id === id);
    if (!confirm(`Delete JMS mock "${m?.name ?? id}"? This cannot be undone.`)) return;
    try {
      await runWithWorkspaceUnlock(() => api.deleteMock(id), (p) => (pendingUnlock = p));
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Right-click on a mock row — see ContextMenu.svelte. contextMenu is null
  // when closed, else { x, y, mock }: the mock is captured at open time so
  // the menu's own item actions don't need a second lookup by id.
  let contextMenu = null;
  function openContextMenu(e, m) {
    e.preventDefault();
    contextMenu = { x: e.clientX, y: e.clientY, mock: m };
  }
  function contextMenuItems(m) {
    return [
      { label: 'Edit', onClick: () => startEdit(m) },
      { label: 'Duplicate', onClick: () => duplicateMock(m) },
      { label: m.enabled ? 'Disable' : 'Enable', onClick: () => toggleEnabled(m) },
      { label: 'Copy port', onClick: () => copyText(String(m.jms?.port ?? '')).then(() => showToast('Port copied', 'ok')) },
      { divider: true },
      { label: 'Delete', danger: true, onClick: () => removeMock(m.id) },
    ];
  }

  async function deleteHitLogEntry(mockId, hitId) {
    try {
      await api.deleteHitLogEntry(hitId);
      hitsByMock = { ...hitsByMock, [mockId]: (hitsByMock[mockId] ?? []).filter((h) => h.id !== hitId) };
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function clearMockLogs(id) {
    if (!confirm('Delete all log history for this mock? This cannot be undone.')) return;
    try {
      await api.deleteHitLogMatching({ mockId: id });
      hitsByMock = { ...hitsByMock, [id]: [] };
      showToast('Log history cleared', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function toggleExpand(id) {
    if (expandedId === id) {
      expandedId = '';
      return;
    }
    expandedId = id;
    hitsLoading = id;
    try {
      const [hits, versions] = await Promise.all([
        api.listHitLog({ mockId: id }),
        api.listMockVersions(id).catch(() => []),
      ]);
      hitsByMock = { ...hitsByMock, [id]: hits ?? [] };
      versionsByMock = { ...versionsByMock, [id]: versions ?? [] };
    } catch (e) {
      showToast(e.message, 'err');
      hitsByMock = { ...hitsByMock, [id]: [] };
    } finally {
      hitsLoading = '';
    }
  }

  async function restoreVersion(mockId, versionId) {
    if (!confirm('Restore this version? The current state will be saved to history first, so this can be undone.')) return;
    restoringVersionId = versionId;
    try {
      await api.restoreMockVersion(mockId, versionId);
      showToast('Mock restored', 'ok');
      versionsByMock = { ...versionsByMock, [mockId]: (await api.listMockVersions(mockId)) ?? [] };
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      restoringVersionId = '';
    }
  }

  // Returns the mapped workspace's name only when it's actually locked —
  // an ungrouped mock, or one mapped to an unlocked workspace, gets no
  // chip at all, since there's nothing for the lock icon to warn about.
  function lockedWorkspaceName(workspaceId) {
    if (!workspaceId) return '';
    const w = workspaces.find((w) => w.id === workspaceId);
    return w?.locked ? w.name : '';
  }

  // A plain function call inside {#if} isn't tracked by Svelte's per-item
  // reactivity the way a directly-referenced variable is — workspaces
  // changing (loadWorkspaces() resolving after mocks already rendered, the
  // normal case on a fresh page load) silently never re-ran the check,
  // leaving every lock chip missing until some UNRELATED re-render (e.g.
  // typing in the search box) happened to force the whole list to
  // recompute. Referencing this reactive Set directly in the template's
  // {#if} (instead of only inside the function) is what makes Svelte
  // actually track it as a per-row dependency.
  $: lockedWorkspaceIds = new Set(workspaces.filter((w) => w.locked).map((w) => w.id));
</script>

<div class="head-row">
  <div>
    <h1>JMS Mocks</h1>
    <p class="sub">A minimal AMQP 1.0 peer — the wire protocol real JMS providers (Qpid JMS, ActiveMQ Artemis) actually speak. Test your app's own produce/consume code against a fake broker instead of a real one. Each mock binds its own dedicated port; no SASL/credentials required.</p>
  </div>
  <button class="btn btn-primary" on:click={() => (showForm ? cancelForm() : startCreate())}>
    {showForm ? 'Cancel' : '+ New JMS mock'}
  </button>
</div>

{#if showForm}
  <div class="card form-card" id="mock-edit-form">
    <button class="card-minus" on:click={cancelForm} title="Cancel" aria-label="Cancel">−</button>
    <h3 class="form-title">{editingId ? 'Edit JMS mock' : 'New JMS mock'}</h3>
    <div class="field-row">
      <label>
        Name
        <input type="text" bind:value={form.name} placeholder="fake-broker" />
      </label>
      <label class="port-field">
        Port
        <input type="number" bind:value={form.port} />
      </label>
    </div>
    <div class="field-row">
      <label class="port-field">
        Workspace <InfoTooltip text="Optional — maps this mock to a Collections workspace. If that workspace is locked, editing or deleting this mock later requires its password." />
        <select bind:value={form.workspaceId}>
          <option value="">Ungrouped</option>
          {#each workspaces as w (w.id)}<option value={w.id}>{w.name}{w.locked ? ' 🔒' : ''}</option>{/each}
        </select>
      </label>
    </div>

    <div class="rules">
      <h3>Rules (first match wins) — evaluated on every message sent</h3>
      {#if form.rules.length === 0}
        <p class="sub">No rules yet — every message is accepted silently with no reply.</p>
      {/if}
      {#each form.rules as r, i}
        <div class="field-row rule-row">
          <label class="topic-field">
            <span class="label-text">Address<InfoTooltip text="The exact JMS destination/queue name a message must be sent to for this rule to match. Blank matches any address." /></span>
            <input type="text" bind:value={r.addressPattern} placeholder="orders" />
          </label>
          <label class="type-field">
            Payload match type
            <select bind:value={r.matchType}>
              <option value="contains">contains</option>
              <option value="exact">exact</option>
              <option value="regex">regex</option>
            </select>
          </label>
          <label class="match-field">
            Payload match (optional — blank matches any payload)
            <input type="text" bind:value={r.payloadMatch} placeholder="cancel" />
          </label>
          <button class="btn btn-ghost remove" on:click={() => removeRule(i)}>&times;</button>
        </div>
        <div class="field-row rule-reply-row">
          <label class="topic-field">
            Reply address (optional — blank means no reply is sent)
            <input type="text" bind:value={r.replyAddress} placeholder="receipts" />
          </label>
          <label>
            Reply payload
            <input type="text" bind:value={r.replyPayload} placeholder={'{"status":"ok"}'} />
          </label>
        </div>
      {/each}
      <button class="btn btn-ghost" on:click={addRule}>+ Add rule</button>
    </div>

    <p class="sub">
      A matched rule's reply is delivered to any consumer currently attached (with available credit) to the reply
      address — a consumer that attaches later, or with no credit granted yet, won't see it; this mock holds no
      durable queue behind an address. Reply payloads are templated the same way as REST —
      <code>&#123;&#123;.Request.Body&#125;&#125;</code> is bound to the received message body. <code>&#123;&#123;fake "uuid"&#125;&#125;</code>/<code>&#123;&#123;fake "email"&#125;&#125;</code>
      generate fake data; sprig helpers like <code>&#123;&#123;now&#125;&#125;</code> and <code>&#123;&#123;upper .x&#125;&#125;</code> also work; <code>&#123;&#123;counter "name"&#125;&#125;</code> auto-increments (persists across restarts) and <code>&#123;&#123;csv "column"&#125;&#125;</code> pulls from an attached CSV.
    </p>

    {#if editingId}
      <CsvSourceField ownerPath={`/api/mocks/${editingId}`} />
    {:else}
      <p class="sub">Save this mock first, then come back to attach a CSV data source for {'{{csv "column"}}'}.</p>
    {/if}

    <button class="btn btn-primary" on:click={saveMock}>{editingId ? 'Save changes' : 'Create JMS mock'}</button>
  </div>
{/if}

<div class="projects-bar">
  <input aria-label="Search JMS mocks by name or port" type="text" class="search-input" bind:value={searchQuery} placeholder="Search JMS mocks by name or port…" />
</div>

{#if loading}
  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading…</p>
{:else if mocks.length === 0}
  <div class="card empty">
    <span class="chip chip-stop">no jms mocks</span>
    <p>No JMS mocks configured yet — create one above.</p>
  </div>
{:else if searchQuery.trim() && filteredMocks.length === 0}
  <div class="card empty">
    <span class="chip badge-warn">no matches</span>
    <p>No JMS mocks match "{searchQuery}".</p>
  </div>
{:else}
  <div class="mock-list">
    {#each filteredMocks as m (m.id)}
      <div class="card mock-card row-enter">
        <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
        <div
          class="mock-row"
          on:click={() => toggleExpand(m.id)}
          on:contextmenu={(e) => openContextMenu(e, m)}
        >
          <button type="button" class="expand-arrow" class:expanded={expandedId === m.id} aria-expanded={!!(expandedId === m.id)} aria-label="Show or hide details" on:click|stopPropagation={() => toggleExpand(m.id)}>▸</button>
          <span class="badge badge-info">:{m.jms?.port}</span>
          <span class="name">{m.name}</span>
          <span class="chip chip-tls">{m.jms?.rules?.length ?? 0} rule{(m.jms?.rules?.length ?? 0) === 1 ? '' : 's'}</span>
          {#if m.workspaceId && lockedWorkspaceIds.has(m.workspaceId)}<span class="chip badge-warn" title="Editing/deleting this mock requires this workspace's password">🔒 {lockedWorkspaceName(m.workspaceId)}</span>{/if}
          <button class="chip {m.enabled ? 'chip-run' : 'chip-stop'}" on:click|stopPropagation={() => toggleEnabled(m)}>
            {m.enabled ? 'enabled' : 'disabled'}
          </button>
          <div class="row-actions">
            <button class="btn btn-ghost" on:click|stopPropagation={() => startEdit(m)}>Edit</button>
            <button class="btn btn-ghost" on:click|stopPropagation={() => duplicateMock(m)}>Duplicate</button>
            <button class="btn btn-ghost remove" on:click|stopPropagation={() => removeMock(m.id)}>Delete</button>
          </div>
        </div>

        {#if expandedId === m.id}
          <div class="mock-detail">
            <div class="detail-section">
              <UsageSnippet command={usageForJms(m)} label="How to test this (requires an AMQP 1.0 client)" />
            </div>

            <div class="detail-section">
              <h4>Rules (first match wins)</h4>
              {#if !m.jms?.rules?.length}
                <p class="sub">No rules configured — every message is accepted silently.</p>
              {:else}
                {#each m.jms.rules as r}
                  <div class="rule-card">
                    <code class="match-preview">{r.addressPattern}</code>
                    {#if r.payloadMatch}
                      <span class="chip chip-stat">{r.matchType || 'contains'}</span>
                      <code class="match-preview">{r.payloadMatch}</code>
                    {/if}
                    <span class="arrow">→</span>
                    {#if r.replyAddress}
                      <code class="match-preview">{r.replyAddress}</code>
                      <code class="response-preview">{r.replyPayload}</code>
                    {:else}
                      <span class="sub">(no reply)</span>
                    {/if}
                  </div>
                {/each}
              {/if}
            </div>

            <div class="detail-section">
              <h4>Connected sessions</h4>
              <SessionsPanel mockId={m.id} protocol="jms" canSend={true} />
            </div>

            <div class="detail-section">
              <h4>Version history</h4>
              <VersionHistory
                versions={versionsByMock[m.id]}
                current={m}
                {restoringVersionId}
                onRestore={(versionId) => restoreVersion(m.id, versionId)}
              />
            </div>

            <div class="detail-section">
              <div class="detail-section-header">
                <h4>Logs History</h4>
                {#if hitsByMock[m.id]?.length}
                  <button class="btn btn-ghost small remove" on:click={() => clearMockLogs(m.id)}>Clear logs</button>
                {/if}
              </div>
              {#if hitsLoading === m.id}
                <p class="sub"><span class="loader-spin"></span>&nbsp; Loading…</p>
              {:else if !hitsByMock[m.id]?.length}
                <p class="sub">No messages received yet — send to port {m.jms?.port} to generate some.</p>
              {:else}
                <div class="hit-list">
                  {#each hitsByMock[m.id] as h (h.id)}
                    <div class="hit-row">
                      <button class="hit-delete" title="Delete this log entry" on:click={() => deleteHitLogEntry(m.id, h.id)}>&times;</button>
                      <code class="hit-req">{h.path}: {h.requestBody}</code>
                      <div class="hit-line2">
                        <span class="arrow">→</span>
                        <code class="hit-resp">{h.responseBody || '(no reply)'}</code>
                      </div>
                      <span class="sub hit-time">{new Date(h.createdAt).toLocaleString()}</span>
                    </div>
                  {/each}
                </div>
              {/if}
            </div>
          </div>
        {/if}
      </div>
    {/each}
  </div>
{/if}

{#if contextMenu}
  <ContextMenu x={contextMenu.x} y={contextMenu.y} items={contextMenuItems(contextMenu.mock)} onClose={() => (contextMenu = null)} />
{/if}

{#if pendingUnlock}
  <WorkspaceUnlockModal
    workspace={workspaces.find((w) => w.id === pendingUnlock.workspaceId)}
    onUnlock={pendingUnlock.onUnlock}
    onCancel={pendingUnlock.onCancel}
  />
{/if}

<style>
  .detail-section-header { display: flex; align-items: center; justify-content: space-between; margin-bottom: 8px; }
  .detail-section-header h4 { margin: 0; }
  .hit-row { position: relative; padding-right: 30px; }
  .hit-delete {
    position: absolute; top: 6px; right: 6px;
    background: none; border: none; color: var(--muted); cursor: pointer; font-size: 14px; line-height: 1;
    padding: 2px 5px; border-radius: 4px;
  }
  .hit-delete:hover { color: var(--error, #dc2626); background: rgba(220,38,38,.1); }

  .head-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
  .head-row > div { min-width: 0; }
  .head-row .btn { flex-shrink: 0; white-space: nowrap; }
  .sub { color: var(--muted); font-size: 13px; margin-top: 4px; }
  .form-card { margin-top: 16px; display: flex; flex-direction: column; gap: 12px; position: relative; padding-right: 40px; }
  .card-minus {
    position: absolute; top: 10px; right: 10px;
    width: 22px; height: 22px; display: flex; align-items: center; justify-content: center;
    background: none; border: 1px solid var(--border); border-radius: 6px; color: var(--muted);
    cursor: pointer; font-size: 15px; line-height: 1; padding: 0;
    transition: color .15s, border-color .15s, background .15s;
  }
  .card-minus:hover { color: var(--error, #dc2626); border-color: var(--error, #dc2626); background: rgba(220,38,38,.1); }
  .form-title { margin: 0; font-size: 14px; }
  .field-row { display: flex; gap: 12px; }
  .port-field { flex: 0 0 120px; }
  label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: .3px; flex: 1; }
  .label-text { display: inline-flex; align-items: center; }
  input, select {
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 8px 10px; font-size: 13px; font-family: inherit; outline: none;
    transition: border .2s;
  }
  input:focus, select:focus { border-color: var(--primary); }

  .rules { border-top: 1px solid var(--border); padding-top: 12px; display: flex; flex-direction: column; gap: 10px; }
  .rules h3 { margin: 0; font-size: 12px; text-transform: uppercase; letter-spacing: .3px; color: var(--muted); }
  .rule-row { align-items: flex-end; }
  .topic-field { flex: 1; }
  .type-field { flex: 0 0 150px; }
  .match-field { flex: 2; }
  .rule-reply-row { margin-top: -4px; }

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

  .rule-card {
    display: flex; align-items: center; gap: 10px; padding: 9px 12px; font-size: 13px;
    border: 1px solid var(--border); border-radius: 8px; margin-bottom: 8px;
  }
  .rule-card:last-child { margin-bottom: 0; }
  .rule-card code {
    font-family: 'SFMono-Regular', Consolas, monospace; font-size: 12px;
    background: var(--surface2, var(--hover)); border-radius: 4px; padding: 2px 6px;
  }
  .match-preview { max-width: 200px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .response-preview { flex: 1; max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--muted); }
  .arrow { color: var(--muted); flex-shrink: 0; }

  .hit-list {
    display: flex; flex-direction: column; gap: 6px;
    max-height: 260px; overflow-y: auto; padding-right: 4px;
  }
  .hit-row {
    display: flex; flex-direction: column; gap: 3px; font-size: 12px;
    padding: 8px 10px; background: var(--surface2, var(--hover)); border-radius: 6px;
  }
  .hit-req {
    font-family: 'SFMono-Regular', Consolas, monospace; overflow: hidden; text-overflow: ellipsis;
    white-space: nowrap;
  }
  .hit-line2 { display: flex; align-items: center; gap: 6px; }
  .hit-resp {
    font-family: 'SFMono-Regular', Consolas, monospace; color: var(--muted);
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .hit-time { color: var(--muted); }
</style>
