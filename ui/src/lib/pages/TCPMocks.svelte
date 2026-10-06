<script>
  import { onMount, tick } from 'svelte';
  import { api } from '../api.js';
  import { showToast } from '../toast.js';
  import { copyText } from '../clipboard.js';
  import InfoTooltip from '../InfoTooltip.svelte';
  import CsvSourceField from '../CsvSourceField.svelte';
  import UsageSnippet from '../UsageSnippet.svelte';
  import VersionHistory from '../VersionHistory.svelte';
  import SessionsPanel from '../SessionsPanel.svelte';
  import ContextMenu from '../ContextMenu.svelte';
  import { usageForTcp } from '../mockUsage.js';
  import { pendingFocus, consumePendingFocus } from '../router.js';
  import WorkspaceUnlockModal from '../WorkspaceUnlockModal.svelte';
  import { runWithWorkspaceUnlock } from '../workspaceLock.js';

  // '' (the empty string) means "no delimiter configured" — left as the
  // engine's own default, which writes real CRLF for Banner/Response/login
  // success-or-failure text (see tcpengine's writeEndingFor) since a bare
  // LF alone isn't guaranteed to move a real telnet terminal's cursor to a
  // new line. '\n' is kept as its own explicit preset (not folded into '')
  // for a scripted/automated client that specifically wants bare LF.
  const PRESET_DELIMITERS = ['', '\n', '\r\n', '\r'];
  function isPresetDelimiter(d) {
    return PRESET_DELIMITERS.includes(d);
  }

  let mocks = [];
  let certificates = [];
  let certBundles = [];
  // Collections workspaces — an optional mapping (workspaceId, unrelated
  // to certificates/certBundles above) that gates editing/deleting a mock
  // behind that workspace's own lock, if it has one. See workspaceLock.js.
  let workspaces = [];
  let pendingUnlock = null;
  let emailTemplates = [];
  let loading = true;
  let searchQuery = '';
  $: serverCerts = certificates.filter((c) => c.kind === 'server');
  $: caCerts = certificates.filter((c) => c.kind === 'ca');

  function matchesSearch(m, query) {
    const needle = query.toLowerCase();
    return (m.name || '').toLowerCase().includes(needle) || String(m.tcp?.port ?? '').includes(needle);
  }
  $: filteredMocks = searchQuery.trim() ? mocks.filter((m) => matchesSearch(m, searchQuery)) : mocks;

  function emptyInteractionAsync() {
    return {
      enabled: false, channel: 'http', targetMode: 'fixed', fixedTarget: '', extractPath: '',
      delayMs: 0, bodyTemplate: '', emailTemplateId: '', emailSubjectTemplate: '',
    };
  }

  // Backend shape (mock.AsyncConfig) <-> this flat per-interaction form
  // shape, mirroring the same split Mocks.svelte uses for a REST mock's
  // async callback — reusing the exact same channel/target/template
  // fields since a TCP interaction's Async is the identical AsyncConfig.
  function asyncFromInteraction(async) {
    if (!async) return emptyInteractionAsync();
    return {
      enabled: true,
      channel: async.callbackChannel || 'http',
      targetMode: async.callbackTargetMode || 'fixed',
      fixedTarget: async.callbackFixedUrl ?? '',
      extractPath: async.callbackExtractPath || 'body.callbackUrl',
      delayMs: async.callbackDelayMs ?? 0,
      bodyTemplate: async.callbackBodyTemplate ?? '',
      emailTemplateId: async.emailTemplateId ?? '',
      emailSubjectTemplate: async.emailSubjectTemplate ?? '',
    };
  }

  function interactionAsyncToBackend(a) {
    if (!a?.enabled) return undefined;
    const async = {
      callbackChannel: a.channel,
      callbackTargetMode: a.targetMode,
      callbackFixedUrl: a.targetMode === 'fixed' ? a.fixedTarget : '',
      callbackExtractPath: a.targetMode === 'extracted' ? a.extractPath : '',
      callbackBodyTemplate: a.bodyTemplate,
      callbackDelayMs: Number(a.delayMs) || 0,
    };
    if (a.channel === 'email') {
      async.emailTemplateId = a.emailTemplateId;
      async.emailSubjectTemplate = a.emailSubjectTemplate;
    }
    return async;
  }
  let showForm = false;
  let editingId = ''; // '' means the form is creating a new mock, not editing

  let expandedId = ''; // mock id currently expanded, '' means none
  let expandedInteraction = ''; // `${mockId}:${index}` of the interaction expanded for full detail
  let hitsByMock = {}; // mockId -> hit-log entries, fetched fresh on every expand
  let hitsLoading = '';
  let versionsByMock = {}; // mockId -> version history, fetched fresh on every expand
  let restoringVersionId = '';

  let form = emptyForm();
  function emptyForm() {
    return {
      name: '',
      port: 9000,
      banner: '220 welcome',
      lineDelimiter: '',
      defaultResponse: 'ERR unknown command',
      interactions: [{ match: 'PING', matchType: 'exact', response: 'PONG', closeAfter: false, async: emptyInteractionAsync() }],
      loginEnabled: false,
      loginMode: '',
      loginLineFormat: '',
      loginLineHint: '',
      loginUsernamePrompt: 'Username: ',
      loginPasswordPrompt: 'Password: ',
      loginUsername: '',
      loginPassword: '',
      loginSuccessMessage: 'Login OK',
      loginFailureMessage: 'Login incorrect',
      loginMaxAttempts: 3,
      sessionTimeoutSecs: 0,
      responseDelayMs: 0,
      tlsEnabled: false,
      tlsBundleId: '',
      tlsCertificateId: '',
      tlsClientCertMode: '',
      tlsClientCaId: '',
      workspaceId: '',
    };
  }

  function formFromMock(m) {
    const login = m.tcp?.login;
    return {
      name: m.name,
      port: m.tcp?.port ?? 9000,
      banner: m.tcp?.banner ?? '',
      lineDelimiter: m.tcp?.lineDelimiter ?? '',
      defaultResponse: m.tcp?.defaultResponse ?? '',
      interactions: (m.tcp?.interactions ?? []).map((it) => ({ ...it, async: asyncFromInteraction(it.async) })),
      loginEnabled: !!login,
      loginMode: login?.mode ?? '',
      loginLineFormat: login?.lineFormat ?? '',
      loginLineHint: login?.lineHint ?? '',
      loginUsernamePrompt: login?.usernamePrompt || 'Username: ',
      loginPasswordPrompt: login?.passwordPrompt || 'Password: ',
      loginUsername: login?.username ?? '',
      loginPassword: login?.password ?? '',
      loginSuccessMessage: login?.successMessage ?? 'Login OK',
      loginFailureMessage: login?.failureMessage ?? 'Login incorrect',
      loginMaxAttempts: login?.maxAttempts || 3,
      sessionTimeoutSecs: m.tcp?.sessionTimeoutSecs ?? 0,
      responseDelayMs: m.tcp?.responseDelayMs ?? 0,
      tlsEnabled: !!m.tcp?.tls,
      tlsBundleId: m.tcp?.tls?.bundleId ?? '',
      tlsCertificateId: m.tcp?.tls?.certificateId ?? '',
      tlsClientCertMode: m.tcp?.tls?.clientCertMode ?? '',
      tlsClientCaId: m.tcp?.tls?.clientCaId ?? '',
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
  // original. The port is bumped by 1 since two enabled TCP mocks can't
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

  function addInteraction() {
    form.interactions = [...form.interactions, { match: '', matchType: 'contains', response: '', closeAfter: false, async: emptyInteractionAsync() }];
  }
  function removeInteraction(i) {
    form.interactions = form.interactions.filter((_, idx) => idx !== i);
  }

  async function load() {
    loading = true;
    try {
      mocks = ((await api.listMocks()) ?? []).filter((m) => m.protocolType === 'tcp');
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

  async function loadEmailTemplates() {
    try {
      emailTemplates = (await api.listEmailTemplates()) ?? [];
    } catch {
      // non-fatal: the email-template picker just falls back to "Custom" only
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
    loadEmailTemplates();
    loadWorkspaces();
  });

  // Reactive rather than a one-shot onMount check: a command-palette jump
  // to a mock already on THIS page (no navigation, so no remount) still
  // needs to react to a new pendingFocus value; gating on mocks.length too
  // means this naturally waits for load() above to finish first.
  $: if ($pendingFocus?.pageId === 'tcpmocks' && mocks.length) {
    const focus = consumePendingFocus('tcpmocks');
    if (focus) expandedId = focus.itemId;
  }

  // A bundle's own server cert/CA take precedence server-side (see
  // resolveBundleTLSRefs), so bundleId and certificateId are mutually
  // exclusive here rather than both being sent whenever a bundle happens to
  // be selected — mirrors Mocks.svelte's buildProjectTls.
  function buildTcpTls(enabled, bundleId, certificateId, clientCertMode, clientCaId) {
    if (!enabled || (!bundleId && !certificateId)) return undefined;
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
      const tls = buildTcpTls(form.tlsEnabled, form.tlsBundleId, form.tlsCertificateId, form.tlsClientCertMode, form.tlsClientCaId);
      const def = {
        name: form.name || `tcp:${form.port}`,
        protocolType: 'tcp',
        workspaceId: form.workspaceId,
        tcp: {
          port: Number(form.port),
          banner: form.banner,
          lineDelimiter: form.lineDelimiter ?? '',
          defaultResponse: form.defaultResponse,
          interactions: form.interactions
            .filter((it) => it.match.trim())
            .map((it) => ({
              match: it.match, matchType: it.matchType, response: it.response, closeAfter: it.closeAfter,
              ...(interactionAsyncToBackend(it.async) ? { async: interactionAsyncToBackend(it.async) } : {}),
            })),
          sessionTimeoutSecs: Number(form.sessionTimeoutSecs) || 0,
          responseDelayMs: Number(form.responseDelayMs) || 0,
          ...(form.loginEnabled
            ? {
                login: {
                  mode: form.loginMode,
                  lineFormat: form.loginLineFormat,
                  lineHint: form.loginLineHint,
                  usernamePrompt: form.loginUsernamePrompt,
                  passwordPrompt: form.loginPasswordPrompt,
                  username: form.loginUsername,
                  password: form.loginPassword,
                  successMessage: form.loginSuccessMessage,
                  failureMessage: form.loginFailureMessage,
                  maxAttempts: Number(form.loginMaxAttempts) || 3,
                },
              }
            : {}),
          ...(tls ? { tls } : {}),
        },
      };
      await runWithWorkspaceUnlock(
        () => {
          if (editingId) {
            // Preserve the mock's current enabled/disabled state — editing
            // fields like the port or an interaction shouldn't silently
            // re-enable a mock the user deliberately turned off.
            const existing = mocks.find((m) => m.id === editingId);
            return api.updateMock(editingId, { ...existing, ...def });
          }
          return api.createMock({ ...def, enabled: true });
        },
        (p) => (pendingUnlock = p)
      );
      showToast(editingId ? 'TCP mock updated' : 'TCP mock created', 'ok');
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
    if (!confirm(`Delete TCP mock "${m?.name ?? id}"? This cannot be undone.`)) return;
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
      { label: 'Copy port', onClick: () => copyText(String(m.tcp?.port ?? '')).then(() => showToast('Port copied', 'ok')) },
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
    expandedInteraction = '';
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

  function toggleInteraction(key) {
    expandedInteraction = expandedInteraction === key ? '' : key;
  }

  function delimiterLabel(d) {
    if (d === undefined || d === null || d === '') return 'Default (LF)';
    if (d === '\n') return 'LF';
    if (d === '\r\n') return 'CRLF';
    if (d === '\r') return 'CR';
    return JSON.stringify(d);
  }

  // Mirrors the backend's buildLoginLineRegex validation (see
  // internal/engine/tcp/session.go) so a mistyped format is caught right
  // in the form, before save, rather than only discovered later as a
  // login that silently never succeeds. Returns null when the format is
  // fine, else a short message to show under the field.
  function loginLineFormatIssue(format) {
    if (!format || !format.trim()) return 'Required — write the login line using {username} and {password}.';
    const userCount = (format.match(/\{username\}/g) || []).length;
    const passCount = (format.match(/\{password\}/g) || []).length;
    if (userCount === 0 && passCount === 0) return 'Missing {username} and {password} — the format needs both placeholders.';
    if (userCount === 0) return 'Missing {username}.';
    if (passCount === 0) return 'Missing {password}.';
    if (userCount > 1) return '{username} can only appear once.';
    if (passCount > 1) return '{password} can only appear once.';
    return null;
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
    <h1>TCP Mocks</h1>
    <p class="sub">Raw line-oriented (Telnet-style) mocks — each one binds its own dedicated port.</p>
  </div>
  <button class="btn btn-primary" on:click={() => (showForm ? cancelForm() : startCreate())}>
    {showForm ? 'Cancel' : '+ New TCP mock'}
  </button>
</div>

{#if showForm}
  <div class="card form-card" id="mock-edit-form">
    <button class="card-minus" on:click={cancelForm} title="Cancel" aria-label="Cancel">−</button>
    <h3 class="form-title">{editingId ? 'Edit TCP mock' : 'New TCP mock'}</h3>
    <div class="field-row">
      <label>
        Name
        <input type="text" bind:value={form.name} placeholder="telnet-echo" />
      </label>
      <label class="port-field">
        Port
        <input type="number" bind:value={form.port} />
      </label>
      <label class="delim-field">
        <span class="label-text">Line delimiter<InfoTooltip text="How this mock recognizes a complete incoming line from the client — a custom value like '###' works fine for a scripted client's own framing. This only affects READING input; the banner and every response always end with a real line break on their way out, regardless of what's picked here." /></span>
        <select
          value={isPresetDelimiter(form.lineDelimiter) ? form.lineDelimiter : 'custom'}
          on:change={(e) => (form.lineDelimiter = e.currentTarget.value === 'custom' ? null : e.currentTarget.value)}
        >
          <option value={''}>Default (LF)</option>
          <option value={'\n'}>LF (\n)</option>
          <option value={'\r\n'}>CRLF (\r\n)</option>
          <option value={'\r'}>CR (\r)</option>
          <option value="custom">Custom…</option>
        </select>
      </label>
      {#if !isPresetDelimiter(form.lineDelimiter)}
        <label class="delim-field">
          Custom delimiter
          <input type="text" bind:value={form.lineDelimiter} placeholder="e.g. ; or ## or " />
        </label>
      {/if}
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
      Banner (sent immediately on connect, optional)
      <textarea rows="2" bind:value={form.banner}></textarea>
    </label>

    <div class="interactions">
      <h3>Interactions (first match wins)</h3>
      {#each form.interactions as it, i}
        <div class="field-row interaction-row">
          <label class="match-field">
            Match
            <input type="text" bind:value={it.match} placeholder="PING" />
          </label>
          <label class="type-field">
            <span class="label-text">Type<InfoTooltip text="contains: matches if the received line includes this text anywhere. exact: the whole line must match exactly. regex: treated as a regular expression." /></span>
            <select bind:value={it.matchType}>
              <option value="contains">contains</option>
              <option value="exact">exact</option>
              <option value="regex">regex</option>
            </select>
          </label>
          <label class="response-field">
            <span class="label-text">Response<InfoTooltip text={`Rendered via Go templates (text/template + sprig) — {{.Request.Body}} is bound to the received line (parsed as JSON when possible). {{fake "uuid"}}/{{fake "email"}}/{{fake "name"}}/{{fake "phone"}} generate fake data; sprig helpers like {{now}}, {{upper .x}}, {{add 1 2}} also work. {{counter "name"}} auto-increments and persists across restarts ({{counter "name" -1}} decrements); {{csv "column"}} pulls from an attached CSV (see below).`} /></span>
            <input type="text" bind:value={it.response} placeholder="PONG\r\n" />
          </label>
          <label class="close-field">
            Close after
            <input type="checkbox" checked={it.closeAfter} on:change={(e) => (it.closeAfter = e.currentTarget.checked)} />
          </label>
          <button class="btn btn-ghost remove" on:click={() => removeInteraction(i)}>&times;</button>
        </div>
        <div class="async-toggle-row">
          <label class="rule-required">
            <input type="checkbox" checked={it.async.enabled} on:change={(e) => (it.async.enabled = e.currentTarget.checked)} />
            Also trigger an async callback (HTTP webhook or email) when this matches
          </label>
        </div>
        {#if it.async.enabled}
          <div class="async-panel">
            <div class="field-row">
              <label class="mode-field">
                Channel
                <select bind:value={it.async.channel}>
                  <option value="http">HTTP webhook</option>
                  <option value="email">Email</option>
                </select>
              </label>
              <label class="mode-field">
                Target
                <select bind:value={it.async.targetMode}>
                  <option value="fixed">Fixed {it.async.channel === 'email' ? 'address' : 'URL'}</option>
                  <option value="extracted">Extracted from line</option>
                </select>
              </label>
              <label class="days-field">
                Delay (ms)
                <input type="number" bind:value={it.async.delayMs} />
              </label>
            </div>
            {#if it.async.targetMode === 'fixed'}
              <label>
                {it.async.channel === 'email' ? 'Recipient email address' : 'Callback URL'}
                <input
                  type="text"
                  bind:value={it.async.fixedTarget}
                  placeholder={it.async.channel === 'email' ? 'customer@example.com' : 'https://example.com/webhook'}
                />
              </label>
            {:else}
              <label>
                Extract path <span class="sub-hint">(only works if the received line is JSON, e.g. body.callbackUrl)</span>
                <input type="text" bind:value={it.async.extractPath} placeholder="body.callbackUrl" />
              </label>
            {/if}
            {#if it.async.channel === 'email'}
              <label>
                Email template
                <select bind:value={it.async.emailTemplateId}>
                  <option value="">Custom (inline subject/body below)</option>
                  {#each emailTemplates as t (t.id)}<option value={t.id}>{t.name}</option>{/each}
                </select>
              </label>
              {#if !it.async.emailTemplateId}
                <label>
                  Subject
                  <input type="text" bind:value={it.async.emailSubjectTemplate} placeholder="Order shipped" />
                </label>
                <label>
                  Email HTML body
                  <textarea rows="3" bind:value={it.async.bodyTemplate}></textarea>
                </label>
              {/if}
            {:else}
              <label>
                Callback body template
                <textarea rows="3" bind:value={it.async.bodyTemplate}></textarea>
              </label>
            {/if}
          </div>
        {/if}
      {/each}
      <button class="btn btn-ghost" on:click={addInteraction}>+ Add interaction</button>
    </div>

    <label>
      Default response (used when no interaction matches, optional)
      <input type="text" bind:value={form.defaultResponse} />
    </label>
    {#if editingId}
      <CsvSourceField ownerPath={`/api/mocks/${editingId}`} />
    {:else}
      <p class="sub">Save this mock first, then come back to attach a CSV data source for {'{{csv "column"}}'}.</p>
    {/if}

    <div class="advanced-block">
      <h3>Session behavior</h3>
      <div class="field-row">
        <label>
          Session timeout (seconds, 0 = none)
          <input type="number" min="0" bind:value={form.sessionTimeoutSecs} />
        </label>
        <label>
          Response delay (ms, 0 = none)
          <input type="number" min="0" bind:value={form.responseDelayMs} />
        </label>
      </div>
    </div>

    <div class="advanced-block">
      <label class="toggle-label">
        <input type="checkbox" bind:checked={form.loginEnabled} />
        Require login before interactions
      </label>
      {#if form.loginEnabled}
        <label>
          <span class="label-text">Login style<InfoTooltip text="Interactive: prompts Username:/Password: as two separate lines, like a real telnet login. Single line: the client sends its own login command as ONE line (e.g. LOGIN:admin:secret) matching a plain-text format you define below, for a scripted client's own login command instead of answering two prompts." /></span>
          <select
            value={form.loginMode || 'interactive'}
            on:change={(e) => (form.loginMode = e.currentTarget.value === 'interactive' ? '' : e.currentTarget.value)}
          >
            <option value="interactive">Interactive (Username:/Password: prompts)</option>
            <option value="line">Single login line (custom format)</option>
          </select>
        </label>
        <div class="field-row">
          <label>
            Username
            <input type="text" bind:value={form.loginUsername} placeholder="admin" />
          </label>
          <label>
            Password
            <input type="text" bind:value={form.loginPassword} placeholder="secret" />
          </label>
          <label class="max-attempts-field">
            Max attempts
            <input type="number" min="1" bind:value={form.loginMaxAttempts} />
          </label>
        </div>
        {#if form.loginMode === 'line'}
          <label>
            <span class="label-text">Login line format<InfoTooltip text={"Write the login command exactly as the client sends it, using the literal placeholders {username} and {password} where those values go. No regular expressions needed — everything else is matched word-for-word. Example: LOGIN:{username}:{password} matches a line like LOGIN:admin:secret."} /></span>
            <input type="text" bind:value={form.loginLineFormat} placeholder={"LOGIN:{username}:{password}"} />
          </label>
          {#if loginLineFormatIssue(form.loginLineFormat)}
            <p class="format-hint invalid">{loginLineFormatIssue(form.loginLineFormat)}</p>
          {/if}
          <label>
            <span class="label-text">Login hint<InfoTooltip text="Optional — sent once before reading the client's login line. Independent of the interactive mode's Username/Password prompts below (switching Login style back and forth never overwrites one with the other). Leave blank for a fully silent login command with no hint at all." /></span>
            <input type="text" bind:value={form.loginLineHint} placeholder="e.g. Send LOGIN:<user>:<pass>" />
          </label>
        {:else}
          <div class="field-row">
            <label>
              Username prompt
              <input type="text" bind:value={form.loginUsernamePrompt} />
            </label>
            <label>
              Password prompt
              <input type="text" bind:value={form.loginPasswordPrompt} />
            </label>
          </div>
        {/if}
        <div class="field-row">
          <label>
            Success message
            <input type="text" bind:value={form.loginSuccessMessage} />
          </label>
          <label>
            Failure message
            <input type="text" bind:value={form.loginFailureMessage} />
          </label>
        </div>
      {/if}
    </div>

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

    <button class="btn btn-primary" on:click={saveMock}>{editingId ? 'Save changes' : 'Create TCP mock'}</button>
  </div>
{/if}

<div class="projects-bar">
  <input aria-label="Search TCP mocks by name or port" type="text" class="search-input" bind:value={searchQuery} placeholder="Search TCP mocks by name or port…" />
</div>

{#if loading}
  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading…</p>
{:else if mocks.length === 0}
  <div class="card empty">
    <span class="chip chip-stop">no tcp mocks</span>
    <p>No TCP mocks configured yet — create one above.</p>
  </div>
{:else if searchQuery.trim() && filteredMocks.length === 0}
  <div class="card empty">
    <span class="chip badge-warn">no matches</span>
    <p>No TCP mocks match "{searchQuery}".</p>
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
          <span class="badge badge-info">:{m.tcp?.port}</span>
          <span class="name">{m.name}</span>
          <span class="chip chip-tls">{m.tcp?.interactions?.length ?? 0} interaction{(m.tcp?.interactions?.length ?? 0) === 1 ? '' : 's'}</span>
          {#if m.tcp?.login}<span class="chip badge-warn">login</span>{/if}
          {#if m.tcp?.tls}<span class="chip chip-stat">tls</span>{/if}
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
              <UsageSnippet command={usageForTcp(m)} />
            </div>

            <div class="detail-section">
              <h4>Configuration</h4>
              <div class="detail-row"><span class="detail-label">Banner</span><code>{m.tcp?.banner || '(none)'}</code></div>
              <div class="detail-row"><span class="detail-label">Line delimiter</span><code>{delimiterLabel(m.tcp?.lineDelimiter)}</code></div>
              <div class="detail-row"><span class="detail-label">Default response</span><code>{m.tcp?.defaultResponse || '(none — unmatched input gets no reply)'}</code></div>
              <div class="detail-row"><span class="detail-label">Session timeout</span><code>{m.tcp?.sessionTimeoutSecs ? m.tcp.sessionTimeoutSecs + 's idle' : 'none'}</code></div>
              <div class="detail-row"><span class="detail-label">Response delay</span><code>{m.tcp?.responseDelayMs ? m.tcp.responseDelayMs + 'ms' : 'none'}</code></div>
              <div class="detail-row"><span class="detail-label">Login</span><code>{m.tcp?.login ? `required (${m.tcp.login.mode === 'line' ? 'single line' : 'interactive'}, user: ${m.tcp.login.username})` : 'not required'}</code></div>
              <div class="detail-row"><span class="detail-label">TLS</span><code>{m.tcp?.tls ? 'enabled' : 'disabled'}</code></div>
            </div>

            <div class="detail-section">
              <h4>Interactions (first match wins)</h4>
              {#if !m.tcp?.interactions?.length}
                <p class="sub">No interactions configured — every line gets the default response.</p>
              {:else}
                {#each m.tcp.interactions as it, i}
                  {@const key = `${m.id}:${i}`}
                  <div class="interaction-card">
                    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
                    <div
                      class="interaction-summary"
                      on:click={() => toggleInteraction(key)}
                    >
                      <button type="button" class="expand-arrow small" class:expanded={expandedInteraction === key} aria-expanded={!!(expandedInteraction === key)} aria-label="Show or hide details" on:click|stopPropagation={() => toggleInteraction(key)}>▸</button>
                      <span class="chip chip-stat">{it.matchType || 'contains'}</span>
                      <code class="match-preview">{it.match}</code>
                      <span class="arrow">→</span>
                      <code class="response-preview">{it.response}</code>
                      {#if it.closeAfter}<span class="chip badge-warn">closes connection</span>{/if}
                      {#if it.async}<span class="chip chip-tls">async {it.async.callbackChannel || 'http'}</span>{/if}
                    </div>
                    {#if expandedInteraction === key}
                      <div class="interaction-full">
                        <div class="detail-row"><span class="detail-label">Match ({it.matchType || 'contains'})</span><code>{it.match}</code></div>
                        <div class="detail-row"><span class="detail-label">Response template</span><pre>{it.response}</pre></div>
                        <div class="detail-row"><span class="detail-label">Closes connection after</span><code>{it.closeAfter ? 'yes' : 'no'}</code></div>
                        {#if it.async}
                          <div class="detail-row"><span class="detail-label">Async channel</span><code>{it.async.callbackChannel || 'http'}</code></div>
                          <div class="detail-row"><span class="detail-label">Async target</span><code>{it.async.callbackTargetMode === 'extracted' ? it.async.callbackExtractPath : (it.async.callbackFixedUrl || '(none)')}</code></div>
                          <div class="detail-row"><span class="detail-label">Async delay</span><code>{it.async.callbackDelayMs ?? 0}ms</code></div>
                        {/if}
                      </div>
                    {/if}
                  </div>
                {/each}
              {/if}
            </div>

            <div class="detail-section">
              <h4>Connected sessions</h4>
              <SessionsPanel mockId={m.id} protocol="tcp" canSend={true} />
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
                <p class="sub">No hits recorded yet — connect to port {m.tcp?.port} to generate some.</p>
              {:else}
                <div class="hit-list">
                  {#each hitsByMock[m.id] as h (h.id)}
                    <div class="hit-row">
                      <button class="hit-delete" title="Delete this log entry" on:click={() => deleteHitLogEntry(m.id, h.id)}>&times;</button>
                      <code class="hit-req">{h.requestBody}</code>
                      <div class="hit-line2">
                        <span class="arrow">→</span>
                        <code class="hit-resp">{h.responseBody || '(no response)'}</code>
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
  .delim-field { flex: 0 0 160px; }
  .max-attempts-field { flex: 0 0 110px; }
  .format-hint { margin: -4px 0 0; font-size: 12px; text-transform: none; letter-spacing: normal; font-weight: 500; }
  .format-hint.invalid { color: var(--error); }
  label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: .3px; flex: 1; }
  .label-text { display: inline-flex; align-items: center; }
  input, select, textarea {
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 8px 10px; font-size: 13px; font-family: inherit; outline: none;
    transition: border .2s;
  }
  textarea { font-family: 'SFMono-Regular', Consolas, monospace; resize: vertical; }
  input:focus, select:focus, textarea:focus { border-color: var(--primary); }

  .interactions { border-top: 1px solid var(--border); padding-top: 12px; display: flex; flex-direction: column; gap: 10px; }
  .interactions h3 { margin: 0; font-size: 12px; text-transform: uppercase; letter-spacing: .3px; color: var(--muted); }
  .interaction-row { align-items: flex-end; }
  .match-field { flex: 1; }
  .type-field { flex: 0 0 110px; }
  .response-field { flex: 2; }
  .close-field { flex: 0 0 90px; flex-direction: row; align-items: center; gap: 6px; text-transform: none; font-weight: 600; }
  .close-field input { width: 15px; height: 15px; }

  .async-toggle-row { margin-top: -4px; }
  .rule-required {
    flex-direction: row; align-items: center; gap: 6px; text-transform: none; font-weight: 600; font-size: 12px; color: var(--text);
  }
  .rule-required input { width: 15px; height: 15px; flex-shrink: 0; }
  .async-panel {
    background: var(--surface2, var(--hover)); border-radius: 8px; padding: 12px;
    display: flex; flex-direction: column; gap: 10px;
  }
  .mode-field { flex: 0 0 160px; }
  .days-field { flex: 0 0 120px; }
  .sub-hint { text-transform: none; font-weight: 400; letter-spacing: 0; opacity: .8; }

  .advanced-block { border-top: 1px solid var(--border); padding-top: 12px; display: flex; flex-direction: column; gap: 10px; }
  .advanced-block h3 { margin: 0; font-size: 12px; text-transform: uppercase; letter-spacing: .3px; color: var(--muted); }
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
  .expand-arrow.small { font-size: 10px; }

  .mock-detail { padding: 16px 18px 18px 46px; border-top: 1px solid var(--border); display: flex; flex-direction: column; gap: 18px; }
  .detail-section h4 { margin: 0 0 8px; font-size: 12px; text-transform: uppercase; letter-spacing: .3px; color: var(--muted); }
  .detail-row { display: flex; align-items: baseline; gap: 10px; font-size: 13px; margin-bottom: 6px; }
  .detail-row:last-child { margin-bottom: 0; }
  .detail-label { flex: 0 0 180px; color: var(--muted); font-size: 12px; font-weight: 600; }
  .detail-row code, .detail-row pre {
    font-family: 'SFMono-Regular', Consolas, monospace; font-size: 12px;
    background: var(--surface2, var(--hover)); border-radius: 4px; padding: 2px 6px;
    white-space: pre-wrap; word-break: break-word; margin: 0;
  }

  .interaction-card { border: 1px solid var(--border); border-radius: 8px; margin-bottom: 8px; overflow: hidden; }
  .interaction-card:last-child { margin-bottom: 0; }
  .interaction-summary { display: flex; align-items: center; gap: 10px; padding: 9px 12px; cursor: pointer; font-size: 13px; }
  .match-preview, .response-preview {
    font-family: 'SFMono-Regular', Consolas, monospace; font-size: 12px; max-width: 220px;
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .response-preview { flex: 1; color: var(--muted); }
  .arrow { color: var(--muted); flex-shrink: 0; }
  .interaction-full { padding: 10px 12px; border-top: 1px solid var(--border); background: var(--surface2, var(--hover)); }
  .interaction-full pre { white-space: pre-wrap; word-break: break-word; }

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
    font-family: 'SFMono-Regular', Consolas, monospace; overflow: hidden; text-overflow: ellipsis;
    white-space: nowrap;
  }
  /* Response (and, after it, the timestamp) default to their own line
     below the request, rather than being squeezed onto one row. */
  .hit-line2 { display: flex; align-items: center; gap: 6px; }
  .hit-resp {
    font-family: 'SFMono-Regular', Consolas, monospace; color: var(--muted);
    overflow: hidden; text-overflow: ellipsis; white-space: nowrap;
  }
  .hit-time { color: var(--muted); }
</style>
