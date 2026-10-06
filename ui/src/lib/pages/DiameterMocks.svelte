<script>
  import { onMount, tick } from 'svelte';
  import { api } from '../api.js';
  import { showToast } from '../toast.js';
  import { copyText } from '../clipboard.js';
  import InfoTooltip from '../InfoTooltip.svelte';
  import UsageSnippet from '../UsageSnippet.svelte';
  import VersionHistory from '../VersionHistory.svelte';
  import SessionsPanel from '../SessionsPanel.svelte';
  import { usageForDiameter } from '../mockUsage.js';
  import { pendingFocus, consumePendingFocus } from '../router.js';
  import WorkspaceUnlockModal from '../WorkspaceUnlockModal.svelte';
  import { runWithWorkspaceUnlock } from '../workspaceLock.js';
  import ContextMenu from '../ContextMenu.svelte';

  const CC_REQUEST_TYPES = [
    { value: 0, label: 'any' },
    { value: 1, label: '1 — INITIAL' },
    { value: 2, label: '2 — UPDATE' },
    { value: 3, label: '3 — TERMINATION' },
    { value: 4, label: '4 — EVENT' },
  ];

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
    return (m.name || '').toLowerCase().includes(needle) || String(m.diameter?.port ?? '').includes(needle);
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
    return { ccRequestType: 0, sessionIdMatch: '', matchType: 'contains', resultCode: 2001 };
  }

  let form = emptyForm();
  function emptyForm() {
    return {
      name: '',
      port: 3868,
      originHost: '',
      originRealm: '',
      defaultResultCode: '',
      rules: [{ ccRequestType: 1, sessionIdMatch: '', matchType: 'contains', resultCode: 2001 }],
      workspaceId: '',
    };
  }

  // Optional numbers: a blank input is "not configured" (omitted), distinct
  // from an explicit 0 (e.g. a zero grant).
  function optNum(v) {
    if (v === '' || v === null || v === undefined) return undefined;
    const n = Number(v);
    return Number.isFinite(n) ? n : undefined;
  }

  // Flattens a stored rule into the editable form shape (fault -> three
  // plain inputs).
  function ruleToForm(r) {
    return {
      ...r,
      faultErrorRate: r.fault?.errorRatePercent ?? '',
      faultTimeoutRate: r.fault?.timeoutRatePercent ?? '',
      faultJitter: r.fault?.latencyJitterMs ?? '',
    };
  }

  function ruleFromForm(r) {
    const faultErr = optNum(r.faultErrorRate);
    const faultTimeout = optNum(r.faultTimeoutRate);
    const faultJitter = optNum(r.faultJitter);
    const hasFault = faultErr || faultTimeout || faultJitter;
    return {
      ccRequestType: Number(r.ccRequestType) || 0,
      sessionIdMatch: r.sessionIdMatch,
      subscriptionIdMatch: r.subscriptionIdMatch || undefined,
      ratingGroup: optNum(r.ratingGroup) || undefined,
      matchType: r.matchType,
      resultCode: Number(r.resultCode) || 0,
      grantedTotalOctets: optNum(r.grantedTotalOctets),
      grantedTime: optNum(r.grantedTime),
      grantedServiceSpecificUnits: optNum(r.grantedServiceSpecificUnits),
      validityTime: optNum(r.validityTime),
      finalUnitAction: r.finalUnitAction || undefined,
      delayMs: optNum(r.delayMs) || undefined,
      fault: hasFault
        ? { errorRatePercent: faultErr || 0, timeoutRatePercent: faultTimeout || 0, latencyJitterMs: faultJitter || 0, errorStatusCodes: [500] }
        : undefined,
    };
  }

  function grantSummary(r) {
    const parts = [];
    if (r.grantedTotalOctets != null) parts.push(`${r.grantedTotalOctets} octets`);
    if (r.grantedTime != null) parts.push(`${r.grantedTime}s`);
    if (r.grantedServiceSpecificUnits != null) parts.push(`${r.grantedServiceSpecificUnits} units`);
    if (r.validityTime != null) parts.push(`valid ${r.validityTime}s`);
    if (r.finalUnitAction) parts.push(`FUI ${r.finalUnitAction}`);
    return parts.join(' · ');
  }

  function formFromMock(m) {
    return {
      name: m.name,
      port: m.diameter?.port ?? 3868,
      originHost: m.diameter?.originHost ?? '',
      originRealm: m.diameter?.originRealm ?? '',
      defaultResultCode: m.diameter?.defaultResultCode ?? '',
      rules: (m.diameter?.rules ?? []).map(ruleToForm),
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
  // original. The port is bumped by 1 since two enabled Diameter mocks
  // can't share a listener port; the user can still change it before saving.
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
      mocks = ((await api.listMocks()) ?? []).filter((m) => m.protocolType === 'diameter');
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
  $: if ($pendingFocus?.pageId === 'diametermocks' && mocks.length) {
    const focus = consumePendingFocus('diametermocks');
    if (focus) expandedId = focus.itemId;
  }

  async function saveMock() {
    try {
      const def = {
        name: form.name || `diameter:${form.port}`,
        protocolType: 'diameter',
        workspaceId: form.workspaceId,
        diameter: {
          port: Number(form.port),
          originHost: form.originHost,
          originRealm: form.originRealm,
          defaultResultCode: optNum(form.defaultResultCode) || undefined,
          rules: form.rules.map(ruleFromForm),
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
      showToast(editingId ? 'Diameter mock updated' : 'Diameter mock created', 'ok');
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
    if (!confirm(`Delete Diameter mock "${m?.name ?? id}"? This cannot be undone.`)) return;
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
      { label: 'Copy port', onClick: () => copyText(String(m.diameter?.port ?? '')).then(() => showToast('Port copied', 'ok')) },
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
    <h1>Diameter Mocks</h1>
    <p class="sub">A minimal Diameter peer — CER/CEA handshake and DWR/DWA watchdog are handled automatically; test your app's own charging/credit-control integration (Gx/Gy-style) with Credit-Control-Request/Answer against a fake peer instead of a real PCRF/OCS. Each mock binds its own dedicated port.</p>
  </div>
  <button class="btn btn-primary" on:click={() => (showForm ? cancelForm() : startCreate())}>
    {showForm ? 'Cancel' : '+ New Diameter mock'}
  </button>
</div>

{#if showForm}
  <div class="card form-card" id="mock-edit-form">
    <button class="card-minus" on:click={cancelForm} title="Cancel" aria-label="Cancel">−</button>
    <h3 class="form-title">{editingId ? 'Edit Diameter mock' : 'New Diameter mock'}</h3>
    <div class="field-row">
      <label>
        Name
        <input type="text" bind:value={form.name} placeholder="fake-pcrf" />
      </label>
      <label class="port-field">
        Port
        <input type="number" bind:value={form.port} />
      </label>
    </div>
    <div class="field-row">
      <label>
        <span class="label-text">Origin-Host<InfoTooltip text="This mock's own Diameter identity, advertised in CER and echoed in every CCA. Left blank, defaults to 'airmock'." /></span>
        <input type="text" bind:value={form.originHost} placeholder="airmock (default)" />
      </label>
      <label>
        <span class="label-text">Origin-Realm<InfoTooltip text="Left blank, defaults to 'airmock.test'." /></span>
        <input type="text" bind:value={form.originRealm} placeholder="airmock.test (default)" />
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

    <label class="default-result">
      <span class="label-text">Result-Code when no rule matches<InfoTooltip text="Blank answers 2001 (DIAMETER_SUCCESS), as before. Set 3002 (UNABLE_TO_DELIVER) or 5012 (UNABLE_TO_COMPLY) so a request that no rule covers fails visibly in your test instead of looking like success. A rule that matches but leaves its own Result-Code blank still answers 2001." /></span>
      <input type="number" bind:value={form.defaultResultCode} placeholder="2001" />
    </label>

    <div class="rules">
      <h3>Rules (first match wins) — evaluated on every Credit-Control-Request received</h3>
      {#if form.rules.length === 0}
        <p class="sub">No rules yet — every CCR is answered with Result-Code 2001 (DIAMETER_SUCCESS).</p>
      {/if}
      {#each form.rules as r, i}
        <div class="field-row rule-row">
          <label class="type-field">
            CC-Request-Type
            <select bind:value={r.ccRequestType}>
              {#each CC_REQUEST_TYPES as t}
                <option value={t.value}>{t.label}</option>
              {/each}
            </select>
          </label>
          <label class="match-type-field">
            Session-Id match type
            <select bind:value={r.matchType}>
              <option value="contains">contains</option>
              <option value="exact">exact</option>
              <option value="regex">regex</option>
            </select>
          </label>
          <label class="match-field">
            Session-Id match (optional — blank matches any session)
            <input type="text" bind:value={r.sessionIdMatch} placeholder="vip" />
          </label>
          <label class="result-field">
            <span class="label-text">Result-Code<InfoTooltip text="2001 = DIAMETER_SUCCESS. 5012 = DIAMETER_UNABLE_TO_COMPLY (a common way to simulate rejection). Any Result-Code value is accepted." /></span>
            <input type="number" bind:value={r.resultCode} placeholder="2001" />
          </label>
          <button class="btn btn-ghost remove" on:click={() => removeRule(i)}>&times;</button>
          <details class="adv" open={!!(r.subscriptionIdMatch || r.ratingGroup || r.grantedTotalOctets != null || r.grantedTime != null || r.grantedServiceSpecificUnits != null || r.validityTime != null || r.finalUnitAction || r.delayMs || r.faultErrorRate || r.faultTimeoutRate || r.faultJitter)}>
            <summary>Granted units, extra matching, delay and fault</summary>
            <div class="adv-grid">
              <label>Subscription-Id match<InfoTooltip text="Matches the Subscription-Id-Data (e.g. an MSISDN or IMSI) of any Subscription-Id in the request, using the match type above. Blank matches any." />
                <input type="text" bind:value={r.subscriptionIdMatch} placeholder="9198" /></label>
              <label>Rating-Group match<InfoTooltip text="Matches when one of the request's Multiple-Services-Credit-Control entries has this Rating-Group. Blank or 0 matches any." />
                <input type="number" bind:value={r.ratingGroup} placeholder="any" /></label>
              <label>Grant: CC-Total-Octets<InfoTooltip text="Adds a Multiple-Services-Credit-Control with a Granted-Service-Unit to the answer, one per MSCC in the request (echoing its Rating-Group and Service-Identifier). Leave every grant field blank to send no MSCC. Never added to TERMINATION answers." />
                <input type="number" min="0" bind:value={r.grantedTotalOctets} placeholder="e.g. 5242880" /></label>
              <label>Grant: CC-Time (s)
                <input type="number" min="0" bind:value={r.grantedTime} placeholder="e.g. 3600" /></label>
              <label>Grant: CC-Service-Specific-Units
                <input type="number" min="0" bind:value={r.grantedServiceSpecificUnits} placeholder="e.g. 100" /></label>
              <label>Validity-Time (s)
                <input type="number" min="0" bind:value={r.validityTime} placeholder="e.g. 600" /></label>
              <label>Final-Unit-Indication<InfoTooltip text="Adds a Final-Unit-Indication with this action to the MSCC, telling the client this is the last grant. Use it on a CCR-Update rule to test quota exhaustion." />
                <select bind:value={r.finalUnitAction}>
                  <option value="">none</option>
                  <option value="terminate">TERMINATE</option>
                  <option value="redirect">REDIRECT</option>
                  <option value="restrict_access">RESTRICT_ACCESS</option>
                </select></label>
              <label>Answer delay (ms)
                <input type="number" min="0" bind:value={r.delayMs} placeholder="0" /></label>
              <label>Fault: error rate %<InfoTooltip text="Replaces the mock-level fault settings for requests this rule matches. An error or timeout means no answer is sent." />
                <input type="number" min="0" max="100" bind:value={r.faultErrorRate} placeholder="0" /></label>
              <label>Fault: timeout rate %
                <input type="number" min="0" max="100" bind:value={r.faultTimeoutRate} placeholder="0" /></label>
              <label>Fault: latency jitter (ms)
                <input type="number" min="0" bind:value={r.faultJitter} placeholder="0" /></label>
            </div>
          </details>
        </div>
      {/each}
      <button class="btn btn-ghost" on:click={addRule}>+ Add rule</button>
    </div>

    <button class="btn btn-primary" on:click={saveMock}>{editingId ? 'Save changes' : 'Create Diameter mock'}</button>
  </div>
{/if}

<div class="projects-bar">
  <input aria-label="Search Diameter mocks by name or port" type="text" class="search-input" bind:value={searchQuery} placeholder="Search Diameter mocks by name or port…" />
</div>

{#if loading}
  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading…</p>
{:else if mocks.length === 0}
  <div class="card empty">
    <span class="chip chip-stop">no diameter mocks</span>
    <p>No Diameter mocks configured yet — create one above.</p>
  </div>
{:else if searchQuery.trim() && filteredMocks.length === 0}
  <div class="card empty">
    <span class="chip badge-warn">no matches</span>
    <p>No Diameter mocks match "{searchQuery}".</p>
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
          <span class="badge badge-info">:{m.diameter?.port}</span>
          <span class="name">{m.name}</span>
          <span class="chip chip-tls">{m.diameter?.rules?.length ?? 0} rule{(m.diameter?.rules?.length ?? 0) === 1 ? '' : 's'}</span>
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
              <UsageSnippet command={usageForDiameter(m)} label="How to test this (requires a Diameter client library)" />
            </div>

            <div class="detail-section">
              <h4>Rules (first match wins)</h4>
              {#if !m.diameter?.rules?.length}
                <p class="sub">No rules configured — every CCR is answered with Result-Code 2001 (DIAMETER_SUCCESS).</p>
              {:else}
                {#each m.diameter.rules as r}
                  <div class="rule-card">
                    <code class="match-preview">{r.ccRequestType ? `type=${r.ccRequestType}` : '(any type)'}</code>
                    {#if r.sessionIdMatch}
                      <span class="chip chip-stat">{r.matchType || 'contains'}</span>
                      <code class="match-preview">{r.sessionIdMatch}</code>
                    {/if}
                    <span class="arrow">→</span>
                    <code class="response-preview">Result-Code {r.resultCode || 2001}</code>
                    {#if grantSummary(r)}<code class="response-preview">MSCC: {grantSummary(r)}</code>{/if}
                  </div>
                {/each}
              {/if}
            </div>

            <div class="detail-section">
              <h4>Connected sessions</h4>
              <SessionsPanel mockId={m.id} protocol="diameter" canSend={false} />
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
                <p class="sub">No requests received yet — send a CCR to port {m.diameter?.port} to generate some.</p>
              {:else}
                <div class="hit-list">
                  {#each hitsByMock[m.id] as h (h.id)}
                    <div class="hit-row">
                      <button class="hit-delete" title="Delete this log entry" on:click={() => deleteHitLogEntry(m.id, h.id)}>&times;</button>
                      <code class="hit-req">{h.path}: {h.requestBody}</code>
                      <div class="hit-line2">
                        <span class="arrow">→</span>
                        <code class="hit-resp">{h.responseBody}</code>
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

  .rule-row { flex-wrap: wrap; }
  .adv { flex: 1 1 100%; border: 1px solid var(--border); border-radius: 8px; padding: 6px 10px; }
  .adv summary { cursor: pointer; font-size: 12px; color: var(--muted); }
  .adv-grid { display: grid; grid-template-columns: repeat(auto-fill, minmax(190px, 1fr)); gap: 10px; margin-top: 10px; }
  .adv-grid label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; }
  .rules { border-top: 1px solid var(--border); padding-top: 12px; display: flex; flex-direction: column; gap: 10px; }
  .rules h3 { margin: 0; font-size: 12px; text-transform: uppercase; letter-spacing: .3px; color: var(--muted); }
  .rule-row { align-items: flex-end; }
  .type-field { flex: 0 0 170px; }
  .match-type-field { flex: 0 0 150px; }
  .match-field { flex: 2; }
  .result-field { flex: 0 0 140px; }

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
