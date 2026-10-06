<script>
  import { onMount, tick } from 'svelte';
  import { api } from '../api.js';
  import { showToast } from '../toast.js';
  import { copyText } from '../clipboard.js';
  import InfoTooltip from '../InfoTooltip.svelte';
  import UsageSnippet from '../UsageSnippet.svelte';
  import VersionHistory from '../VersionHistory.svelte';
  import SessionsPanel from '../SessionsPanel.svelte';
  import { usageForSmtp } from '../mockUsage.js';
  import { pendingFocus, consumePendingFocus } from '../router.js';
  import WorkspaceUnlockModal from '../WorkspaceUnlockModal.svelte';
  import { runWithWorkspaceUnlock } from '../workspaceLock.js';
  import ContextMenu from '../ContextMenu.svelte';

  let mocks = [];
  let certificates = [];
  let certBundles = [];
  // Collections workspaces — an optional mapping (workspaceId) that gates
  // editing/deleting a mock behind that workspace's own lock, if it has
  // one. See workspaceLock.js.
  let workspaces = [];
  let pendingUnlock = null;
  $: serverCerts = certificates.filter((c) => c.kind === 'server');
  $: caCerts = certificates.filter((c) => c.kind === 'ca');
  let loading = true;
  let searchQuery = '';

  function matchesSearch(m, query) {
    const needle = query.toLowerCase();
    return (m.name || '').toLowerCase().includes(needle) || String(m.smtp?.port ?? '').includes(needle);
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
    return { matchField: 'subject', match: '', matchType: 'contains', accept: false, responseCode: '', responseMessage: '' };
  }

  let form = emptyForm();
  function emptyForm() {
    return {
      name: '',
      port: 2525,
      hostname: 'airmock',
      banner: '',
      defaultAccept: true,
      rules: [],
      tlsEnabled: false,
      tlsBundleId: '',
      tlsCertificateId: '',
      tlsClientCertMode: '',
      tlsClientCaId: '',
      workspaceId: '',
    };
  }

  function formFromMock(m) {
    return {
      name: m.name,
      port: m.smtp?.port ?? 2525,
      hostname: m.smtp?.hostname ?? 'airmock',
      banner: m.smtp?.banner ?? '',
      defaultAccept: m.smtp?.defaultAccept ?? true,
      rules: (m.smtp?.rules ?? []).map((r) => ({ ...r, responseCode: r.responseCode ?? '', responseMessage: r.responseMessage ?? '' })),
      tlsEnabled: !!m.smtp?.tls,
      tlsBundleId: m.smtp?.tls?.bundleId ?? '',
      tlsCertificateId: m.smtp?.tls?.certificateId ?? '',
      tlsClientCertMode: m.smtp?.tls?.clientCertMode ?? '',
      tlsClientCaId: m.smtp?.tls?.clientCaId ?? '',
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
  // original. The port is bumped by 1 since two enabled SMTP mocks can't
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
      mocks = ((await api.listMocks()) ?? []).filter((m) => m.protocolType === 'smtp');
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      loading = false;
    }
  }

  async function loadCertificates() {
    try {
      certificates = (await api.listCertificates()) ?? [];
    } catch {
      certificates = []; // TLS section just shows "no certificates" — not fatal to the page
    }
  }

  async function loadCertBundles() {
    try {
      certBundles = (await api.listCertBundles()) ?? [];
    } catch {
      certBundles = []; // TLS section just falls back to picking certs individually
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
    loadCertificates();
    loadCertBundles();
    loadWorkspaces();
  });

  // Reactive rather than a one-shot onMount check — see TCPMocks.svelte
  // for why (a same-page palette jump doesn't remount this page).
  $: if ($pendingFocus?.pageId === 'smtpmocks' && mocks.length) {
    const focus = consumePendingFocus('smtpmocks');
    if (focus) expandedId = focus.itemId;
  }

  // A bundle's own server cert/CA take precedence server-side (see
  // resolveBundleTLSRefs), so bundleId and certificateId are mutually
  // exclusive here rather than both being sent whenever a bundle happens
  // to be selected.
  function buildSmtpTls(enabled, bundleId, certificateId, clientCertMode, clientCaId) {
    if (!enabled || (!bundleId && !certificateId)) return null;
    const requiresCa = clientCertMode === 'optional' || clientCertMode === 'required';
    return {
      bundleId: bundleId || '',
      certificateId: bundleId ? '' : certificateId,
      clientCertMode: clientCertMode || '',
      clientCaId: requiresCa ? clientCaId : '',
    };
  }

  async function saveMock() {
    try {
      const tls = buildSmtpTls(form.tlsEnabled, form.tlsBundleId, form.tlsCertificateId, form.tlsClientCertMode, form.tlsClientCaId);
      const def = {
        name: form.name || `smtp:${form.port}`,
        protocolType: 'smtp',
        workspaceId: form.workspaceId,
        smtp: {
          port: Number(form.port),
          hostname: form.hostname,
          banner: form.banner,
          defaultAccept: !!form.defaultAccept,
          rules: form.rules
            .filter((r) => r.match.trim())
            .map((r) => ({
              matchField: r.matchField,
              match: r.match,
              matchType: r.matchType,
              accept: !!r.accept,
              ...(r.responseCode !== '' ? { responseCode: Number(r.responseCode) } : {}),
              ...(r.responseMessage ? { responseMessage: r.responseMessage } : {}),
            })),
          ...(tls ? { tls } : {}),
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
      showToast(editingId ? 'SMTP mock updated' : 'SMTP mock created', 'ok');
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
    if (!confirm(`Delete SMTP mock "${m?.name ?? id}"? This cannot be undone.`)) return;
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
      { label: 'Copy port', onClick: () => copyText(String(m.smtp?.port ?? '')).then(() => showToast('Port copied', 'ok')) },
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
    // Always fetch fresh rather than caching — a mock likely has new hits
    // since it was last expanded, and this is a low-traffic admin UI where
    // an extra request on open is cheap.
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
    <h1>SMTP Mocks</h1>
    <p class="sub">Fake inbound mail servers — test your app's own outbound-email code against a listener instead of a real mailbox. Each mock binds its own dedicated port.</p>
  </div>
  <button class="btn btn-primary" on:click={() => (showForm ? cancelForm() : startCreate())}>
    {showForm ? 'Cancel' : '+ New SMTP mock'}
  </button>
</div>

{#if showForm}
  <div class="card form-card" id="mock-edit-form">
    <button class="card-minus" on:click={cancelForm} title="Cancel" aria-label="Cancel">−</button>
    <h3 class="form-title">{editingId ? 'Edit SMTP mock' : 'New SMTP mock'}</h3>
    <div class="field-row">
      <label>
        Name
        <input type="text" bind:value={form.name} placeholder="fake-mailbox" />
      </label>
      <label class="port-field">
        Port
        <input type="number" bind:value={form.port} />
      </label>
      <label>
        Hostname (used in the EHLO/banner greeting)
        <input type="text" bind:value={form.hostname} placeholder="airmock" />
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

    <label>
      Banner (optional — sent verbatim as the greeting, so it must start with a reply code such as "220 ". Defaults to "220 &lbrace;hostname&rbrace; ESMTP AirMock")
      <input type="text" bind:value={form.banner} placeholder="220 mail.example.com ESMTP" />
    </label>

    <div class="rules">
      <h3>Accept / reject rules (first match wins)</h3>
      <p class="sub">From and To rules are checked as soon as the sender (MAIL FROM) or each recipient (RCPT TO) is given, so a refusal reaches the client immediately. Subject and Body rules can only be checked after the message arrives.</p>
      {#if form.rules.length === 0}
        <p class="sub">No rules yet — every message falls through to the default action below.</p>
      {/if}
      {#each form.rules as r, i}
        <div class="field-row rule-row">
          <label class="field-field">
            Field
            <select bind:value={r.matchField}>
              <option value="from">From</option>
              <option value="to">To</option>
              <option value="subject">Subject</option>
              <option value="body">Body</option>
            </select>
          </label>
          <label class="type-field">
            <span class="label-text">Type<InfoTooltip text="contains: matches if the field includes this text anywhere. exact: the whole field must match exactly. regex: treated as a regular expression." /></span>
            <select bind:value={r.matchType}>
              <option value="contains">contains</option>
              <option value="exact">exact</option>
              <option value="regex">regex</option>
            </select>
          </label>
          <label class="match-field">
            Match
            <input type="text" bind:value={r.match} placeholder="spam" />
          </label>
          <label class="accept-field">
            Action
            <select value={r.accept ? 'accept' : 'reject'} on:change={(e) => (r.accept = e.currentTarget.value === 'accept')}>
              <option value="accept">Accept</option>
              <option value="reject">Reject</option>
            </select>
          </label>
          <button class="btn btn-ghost remove" on:click={() => removeRule(i)}>&times;</button>
        </div>
        <div class="field-row rule-response-row">
          <label class="code-field">
            Response code (optional)
            <input type="number" bind:value={r.responseCode} placeholder={r.accept ? '250' : '550'} />
          </label>
          <label>
            Response message (optional)
            <input type="text" bind:value={r.responseMessage} placeholder={r.accept ? 'OK' : 'Rejected'} />
          </label>
        </div>
      {/each}
      <button class="btn btn-ghost" on:click={addRule}>+ Add rule</button>
    </div>

    <label class="toggle-label">
      <input type="checkbox" bind:checked={form.defaultAccept} />
      Accept messages that don't match any rule (unchecked = reject by default)
    </label>

    <div class="advanced-block">
      <label class="toggle-label">
        <input type="checkbox" bind:checked={form.tlsEnabled} />
        Enable TLS on this mock's port
      </label>
      {#if form.tlsEnabled}
        {#if certificates.length === 0}
          <p class="sub">No certificates in the store yet — create one on the Certificates page first.</p>
        {:else}
          <label>
            Bundle (optional — supplies the server cert + CA together)
            <select bind:value={form.tlsBundleId}>
              <option value="">None — pick individually below</option>
              {#each certBundles as b}<option value={b.id}>{b.name}</option>{/each}
            </select>
          </label>
          <label>
            Server certificate
            <select bind:value={form.tlsCertificateId} disabled={!!form.tlsBundleId}>
              <option value="">Select a certificate…</option>
              {#each serverCerts as c}
                <option value={c.id}>{c.name}</option>
              {/each}
            </select>
          </label>
          <label>
            Client certificate
            <select bind:value={form.tlsClientCertMode}>
              <option value="">None required</option>
              <option value="optional">Optional — verified if presented</option>
              <option value="required">Required — reject connections without one</option>
            </select>
          </label>
          {#if (form.tlsClientCertMode === 'optional' || form.tlsClientCertMode === 'required') && !form.tlsBundleId}
            <label>
              Trusted client CA
              <select bind:value={form.tlsClientCaId}>
                <option value="">Select a CA…</option>
                {#each caCerts as c}<option value={c.id}>{c.name}</option>{/each}
              </select>
            </label>
          {/if}
        {/if}
      {/if}
    </div>

    <button class="btn btn-primary" on:click={saveMock}>{editingId ? 'Save changes' : 'Create SMTP mock'}</button>
  </div>
{/if}

<div class="projects-bar">
  <input aria-label="Search SMTP mocks by name or port" type="text" class="search-input" bind:value={searchQuery} placeholder="Search SMTP mocks by name or port…" />
</div>

{#if loading}
  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading…</p>
{:else if mocks.length === 0}
  <div class="card empty">
    <span class="chip chip-stop">no smtp mocks</span>
    <p>No SMTP mocks configured yet — create one above.</p>
  </div>
{:else if searchQuery.trim() && filteredMocks.length === 0}
  <div class="card empty">
    <span class="chip badge-warn">no matches</span>
    <p>No SMTP mocks match "{searchQuery}".</p>
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
          <span class="badge badge-info">:{m.smtp?.port}</span>
          <span class="name">{m.name}</span>
          <span class="chip chip-tls">{m.smtp?.rules?.length ?? 0} rule{(m.smtp?.rules?.length ?? 0) === 1 ? '' : 's'}</span>
          <span class="chip {m.smtp?.defaultAccept ? 'chip-run' : 'badge-warn'}">default {m.smtp?.defaultAccept ? 'accept' : 'reject'}</span>
          {#if m.smtp?.tls}<span class="chip chip-stat">tls</span>{/if}
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
              <UsageSnippet command={usageForSmtp(m)} />
            </div>

            <div class="detail-section">
              <h4>Configuration</h4>
              <div class="detail-row"><span class="detail-label">Hostname</span><code>{m.smtp?.hostname || 'airmock'}</code></div>
              <div class="detail-row"><span class="detail-label">Banner</span><code>{m.smtp?.banner || '(default ESMTP banner)'}</code></div>
              <div class="detail-row"><span class="detail-label">Default action</span><code>{m.smtp?.defaultAccept ? 'accept' : 'reject'}</code></div>
              <div class="detail-row"><span class="detail-label">TLS</span><code>{m.smtp?.tls ? 'enabled' : 'disabled'}</code></div>
            </div>

            <div class="detail-section">
              <h4>Rules (first match wins)</h4>
              {#if !m.smtp?.rules?.length}
                <p class="sub">No rules configured — every message gets the default action above.</p>
              {:else}
                {#each m.smtp.rules as r}
                  <div class="rule-card">
                    <span class="chip chip-stat">{r.matchField}</span>
                    <span class="chip chip-stat">{r.matchType || 'contains'}</span>
                    <code class="match-preview">{r.match}</code>
                    <span class="arrow">→</span>
                    <span class="chip {r.accept ? 'chip-run' : 'badge-warn'}">{r.accept ? 'accept' : 'reject'}</span>
                    {#if r.responseCode}<code>{r.responseCode}</code>{/if}
                    {#if r.responseMessage}<code>{r.responseMessage}</code>{/if}
                  </div>
                {/each}
              {/if}
            </div>

            <div class="detail-section">
              <h4>Connected sessions</h4>
              <SessionsPanel mockId={m.id} protocol="smtp" canSend={false} />
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
                <p class="sub">No messages received yet — send mail to port {m.smtp?.port} to generate some.</p>
              {:else}
                <div class="hit-list">
                  {#each hitsByMock[m.id] as h (h.id)}
                    <div class="hit-row">
                      <button class="hit-delete" title="Delete this log entry" on:click={() => deleteHitLogEntry(m.id, h.id)}>&times;</button>
                      <code class="hit-req">{h.requestBody}</code>
                      <div class="hit-line2">
                        <span class="arrow">→</span>
                        <code class="hit-resp">{h.responseStatus} {h.responseBody || ''}</code>
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
  .field-field { flex: 0 0 110px; }
  .type-field { flex: 0 0 110px; }
  .match-field { flex: 2; }
  .accept-field { flex: 0 0 120px; }
  .rule-response-row { margin-top: -4px; }
  .code-field { flex: 0 0 140px; }

  .advanced-block { border-top: 1px solid var(--border); padding-top: 12px; display: flex; flex-direction: column; gap: 10px; }
  .toggle-label {
    flex-direction: row; align-items: center; gap: 8px; text-transform: none; font-weight: 600;
    font-size: 13px; color: var(--text); cursor: pointer;
  }
  .toggle-label input { width: 15px; height: 15px; }

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
  .detail-label { flex: 0 0 180px; color: var(--muted); font-size: 12px; font-weight: 600; }
  .detail-row code {
    font-family: 'SFMono-Regular', Consolas, monospace; font-size: 12px;
    background: var(--surface2, var(--hover)); border-radius: 4px; padding: 2px 6px;
    white-space: pre-wrap; word-break: break-word; margin: 0;
  }

  .rule-card {
    display: flex; align-items: center; gap: 10px; padding: 9px 12px; font-size: 13px;
    border: 1px solid var(--border); border-radius: 8px; margin-bottom: 8px;
  }
  .rule-card:last-child { margin-bottom: 0; }
  .rule-card code {
    font-family: 'SFMono-Regular', Consolas, monospace; font-size: 12px;
    background: var(--surface2, var(--hover)); border-radius: 4px; padding: 2px 6px;
  }
  .match-preview { max-width: 220px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .arrow { color: var(--muted); flex-shrink: 0; }

  /* Fixed, scrollable viewport so a mock with a long history doesn't push
     the rest of the page down indefinitely. */
  .hit-list {
    display: flex; flex-direction: column; gap: 6px;
    max-height: 260px; overflow-y: auto; padding-right: 4px;
  }
  .hit-row {
    display: flex; flex-direction: column; gap: 3px; font-size: 12px;
    padding: 8px 10px; background: var(--surface2, var(--hover)); border-radius: 6px;
  }
  .hit-req {
    font-family: 'SFMono-Regular', Consolas, monospace; white-space: pre-wrap; word-break: break-word;
  }
  .hit-line2 { display: flex; align-items: center; gap: 6px; }
  .hit-resp {
    font-family: 'SFMono-Regular', Consolas, monospace; color: var(--muted);
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .hit-time { color: var(--muted); }

</style>
