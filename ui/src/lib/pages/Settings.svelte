<script>
  import { onMount } from 'svelte';
  import { api } from '../api.js';
  import { showToast } from '../toast.js';
  import InfoTooltip from '../InfoTooltip.svelte';
  import { density, setDensity, confirmBeforeDelete, setConfirmBeforeDelete } from '../uiPrefs.js';
  import { resetNavOrder } from '../navOrder.js';
  import { authState, setPassword, clearPassword } from '../auth.js';
  import { setInactivityTimeoutMinutes } from '../inactivityWatcher.js';

  const DEFAULT_REDACTED_HEADERS = ['Authorization', 'Cookie', 'Set-Cookie', 'X-Api-Key', 'X-Auth-Token'];
  // Mirrors settings.Default* on the Go side (internal/settings/store.go) —
  // duplicated here rather than fetched from the backend, same as
  // DEFAULT_REDACTED_HEADERS above already was before this button existed.
  const DEFAULTS = {
    hitLogMaxAgeDays: 30,
    hitLogMaxRowsPerMock: 10000,
    maxVersionsPerMock: 20,
    redactedHeaders: DEFAULT_REDACTED_HEADERS,
    maxCapturedBodyBytes: 65536,
    defaultResponseDelayMs: 0,
    defaultFailureRatePercent: 0,
    sessionTimeoutMinutes: 24 * 60,
    inactivityLockMinutes: 0,
    loadTestRunMaxAgeDays: 30,
    loadTestRunMaxRowsPerItem: 50,
    cors: { enabled: true, allowOrigin: '*', allowMethods: 'GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS', allowHeaders: '*', allowCredentials: false, maxAgeSecs: 600 },
  };

  let loading = true;
  let saving = false;
  let hitLogMaxAgeDays = 30;
  let hitLogMaxRowsPerMock = 10000;
  let maxVersionsPerMock = 20;
  let redactedHeaders = [...DEFAULT_REDACTED_HEADERS];
  let newHeaderName = '';
  let defaultResponseDelayMs = 0;
  let defaultFailureRatePercent = 0;
  let maxCapturedBodyBytes = 65536;
  let loadTestRunMaxAgeDays = 30;
  let loadTestRunMaxRowsPerItem = 50;
  let cors = { ...DEFAULTS.cors };
  // The backend stores/validates a single sessionTimeoutMinutes value —
  // sessionTimeoutValue/sessionTimeoutUnit are purely this form's own
  // display convenience, letting "24 hours" be entered as 24 (not 1440)
  // while still supporting a short, minutes-granularity value (e.g. 5
  // minutes) for anyone who wants a strict timeout.
  let sessionTimeoutValue = 24;
  let sessionTimeoutUnit = 'hours';

  function applySessionTimeoutMinutes(minutes) {
    if (minutes >= 60 && minutes % 60 === 0) {
      sessionTimeoutUnit = 'hours';
      sessionTimeoutValue = minutes / 60;
    } else {
      sessionTimeoutUnit = 'minutes';
      sessionTimeoutValue = minutes;
    }
  }

  // Auto-lock after inactivity is a SEPARATE setting from Session timeout
  // above (see inactivityWatcher.js) — off by default (inactivityLockEnabled
  // false / 0 minutes), since unlike the session timeout it's a purely
  // opt-in feature, not something every login needs a value for.
  let inactivityLockEnabled = false;
  let inactivityLockValue = 5;
  let inactivityLockUnit = 'minutes';

  function applyInactivityLockMinutes(minutes) {
    if (!minutes || minutes <= 0) {
      inactivityLockEnabled = false;
      inactivityLockValue = 5;
      inactivityLockUnit = 'minutes';
      return;
    }
    inactivityLockEnabled = true;
    if (minutes >= 60 && minutes % 60 === 0) {
      inactivityLockUnit = 'hours';
      inactivityLockValue = minutes / 60;
    } else {
      inactivityLockUnit = 'minutes';
      inactivityLockValue = minutes;
    }
  }

  let runtimeConfig = null;

  async function load() {
    loading = true;
    try {
      const s = await api.getSettings();
      hitLogMaxAgeDays = s.hitLogMaxAgeDays;
      hitLogMaxRowsPerMock = s.hitLogMaxRowsPerMock;
      maxVersionsPerMock = s.maxVersionsPerMock;
      redactedHeaders = s.redactedHeaders ?? [];
      defaultResponseDelayMs = s.defaultResponseDelayMs ?? 0;
      defaultFailureRatePercent = s.defaultFailureRatePercent ?? 0;
      maxCapturedBodyBytes = s.maxCapturedBodyBytes ?? 65536;
      loadTestRunMaxAgeDays = s.loadTestRunMaxAgeDays;
      loadTestRunMaxRowsPerItem = s.loadTestRunMaxRowsPerItem;
      cors = { ...DEFAULTS.cors, ...(s.cors ?? {}) };
      applySessionTimeoutMinutes(s.sessionTimeoutMinutes ?? 24 * 60);
      applyInactivityLockMinutes(s.inactivityLockMinutes ?? 0);
      setInactivityTimeoutMinutes(s.inactivityLockMinutes ?? 0);
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      loading = false;
    }
  }

  async function loadRuntimeConfig() {
    try {
      runtimeConfig = await api.getConfig();
    } catch {
      // non-fatal: the runtime-info card just doesn't render
    }
  }

  onMount(() => {
    load();
    loadRuntimeConfig();
  });

  function addHeader() {
    const name = newHeaderName.trim();
    if (!name) return;
    if (redactedHeaders.some((h) => h.toLowerCase() === name.toLowerCase())) {
      newHeaderName = '';
      return;
    }
    redactedHeaders = [...redactedHeaders, name];
    newHeaderName = '';
  }
  function removeHeader(name) {
    redactedHeaders = redactedHeaders.filter((h) => h !== name);
  }
  function resetHeadersToDefault() {
    redactedHeaders = [...DEFAULT_REDACTED_HEADERS];
  }

  // Enabling login (the very first time) needs no existing password to
  // check the new one against — the backend's RequireAuth is a no-op while
  // auth is disabled, same tradeoff a first-run setup page would always
  // have. Changing an already-set password reuses the same form/action;
  // the backend now requires a valid session for that case instead.
  let showSetPasswordForm = false;
  let newCredentialType = 'password';
  let newPassword = '';
  let confirmPassword = '';
  let setPasswordError = '';
  let settingPassword = false;

  function openSetPasswordForm() {
    newCredentialType = 'password';
    newPassword = '';
    confirmPassword = '';
    setPasswordError = '';
    showSetPasswordForm = true;
  }
  function cancelSetPasswordForm() {
    showSetPasswordForm = false;
  }
  async function saveNewPassword() {
    setPasswordError = '';
    if (newCredentialType === 'pin') {
      if (!/^\d{4,6}$/.test(newPassword)) {
        setPasswordError = 'PIN must be 4-6 digits';
        return;
      }
    } else if (newPassword.length < 4) {
      setPasswordError = 'Password must be at least 4 characters';
      return;
    }
    if (newPassword !== confirmPassword) {
      setPasswordError = `${newCredentialType === 'pin' ? 'PINs' : 'Passwords'} don't match`;
      return;
    }
    settingPassword = true;
    try {
      await setPassword(newCredentialType, newPassword);
      showSetPasswordForm = false;
      showToast('Admin login enabled', 'ok');
    } catch (e) {
      setPasswordError = e.message;
    } finally {
      settingPassword = false;
    }
  }
  async function turnOffLogin() {
    if (!confirm('Turn off admin login? The admin UI/API will be reachable by anyone with network access to this server.')) return;
    try {
      await clearPassword();
      showToast('Admin login turned off', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Resets every instance-wide field on this page back to its shipped
  // default — in memory only, same as resetHeadersToDefault above: nothing
  // is actually persisted until "Save changes" is clicked afterward, so
  // resetting and then navigating away without saving leaves the real
  // config untouched. Doesn't touch the separate Preferences card (UI
  // density / confirm-before-delete) — those are per-browser, not part of
  // "instance-wide configuration" this page's own header describes.
  function resetAllToDefaults() {
    if (!confirm('Reset all instance-wide settings on this page back to their defaults? Click "Save changes" afterward to actually apply it.')) return;
    hitLogMaxAgeDays = DEFAULTS.hitLogMaxAgeDays;
    hitLogMaxRowsPerMock = DEFAULTS.hitLogMaxRowsPerMock;
    maxVersionsPerMock = DEFAULTS.maxVersionsPerMock;
    redactedHeaders = [...DEFAULTS.redactedHeaders];
    maxCapturedBodyBytes = DEFAULTS.maxCapturedBodyBytes;
    defaultResponseDelayMs = DEFAULTS.defaultResponseDelayMs;
    defaultFailureRatePercent = DEFAULTS.defaultFailureRatePercent;
    loadTestRunMaxAgeDays = DEFAULTS.loadTestRunMaxAgeDays;
    loadTestRunMaxRowsPerItem = DEFAULTS.loadTestRunMaxRowsPerItem;
    cors = { ...DEFAULTS.cors };
    applySessionTimeoutMinutes(DEFAULTS.sessionTimeoutMinutes);
    applyInactivityLockMinutes(DEFAULTS.inactivityLockMinutes);
    showToast('Reset to defaults — click "Save changes" to apply', 'ok');
  }

  let importFileEl;
  let importing = false;
  let importResult = null;

  function triggerImportPicker() {
    importFileEl?.click();
  }

  // Uploads the picked file's raw bytes as-is (not re-encoded through
  // api.js's JSON-body helper) so the exported file round-trips byte for
  // byte — merges everything it contains into this instance under new ids,
  // suffixing any name that collides with what's already here rather than
  // overwriting or skipping it (see internal/web/api/backup.go's doImport).
  async function onImportFileChosen(e) {
    const file = e.target.files?.[0];
    e.target.value = ''; // let re-choosing the exact same file re-fire change
    if (!file) return;
    importing = true;
    importResult = null;
    try {
      const text = await file.text();
      const res = await fetch('/api/backup/import', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: text });
      const data = await res.json();
      if (!res.ok) throw new Error(data.error || `import failed (${res.status})`);
      importResult = data;
      const totalImported = Object.values(data).reduce((sum, s) => sum + (s.imported?.length ?? 0), 0);
      const totalSkipped = Object.values(data).reduce((sum, s) => sum + (s.skipped?.length ?? 0), 0);
      showToast(`Imported ${totalImported} item${totalImported === 1 ? '' : 's'}${totalSkipped ? `, ${totalSkipped} skipped` : ''}`, totalSkipped ? 'err' : 'ok');
    } catch (err) {
      showToast(err.message, 'err');
    } finally {
      importing = false;
    }
  }

  const BACKUP_CATEGORY_LABELS = {
    certificates: 'Certificates', certBundles: 'Certificate bundles', mockProjects: 'Mock projects',
    mocks: 'Mocks', emailTemplates: 'Email templates', workspaces: 'Workspaces',
    environments: 'Environments', collections: 'Collections', scheduledEvents: 'Scheduled events',
  };

  async function save() {
    saving = true;
    try {
      const sessionTimeoutMinutes = Number(sessionTimeoutValue) * (sessionTimeoutUnit === 'hours' ? 60 : 1);
      const inactivityLockMinutes = inactivityLockEnabled
        ? Number(inactivityLockValue) * (inactivityLockUnit === 'hours' ? 60 : 1)
        : 0;
      const s = await api.saveSettings({
        hitLogMaxAgeDays: Number(hitLogMaxAgeDays),
        hitLogMaxRowsPerMock: Number(hitLogMaxRowsPerMock),
        maxVersionsPerMock: Number(maxVersionsPerMock),
        redactedHeaders,
        defaultResponseDelayMs: Number(defaultResponseDelayMs),
        defaultFailureRatePercent: Number(defaultFailureRatePercent),
        maxCapturedBodyBytes: Number(maxCapturedBodyBytes),
        loadTestRunMaxAgeDays: Number(loadTestRunMaxAgeDays),
        loadTestRunMaxRowsPerItem: Number(loadTestRunMaxRowsPerItem),
        cors: { ...cors, maxAgeSecs: Number(cors.maxAgeSecs) || 0 },
        sessionTimeoutMinutes,
        inactivityLockMinutes,
      });
      hitLogMaxAgeDays = s.hitLogMaxAgeDays;
      hitLogMaxRowsPerMock = s.hitLogMaxRowsPerMock;
      maxVersionsPerMock = s.maxVersionsPerMock;
      redactedHeaders = s.redactedHeaders ?? [];
      defaultResponseDelayMs = s.defaultResponseDelayMs ?? 0;
      defaultFailureRatePercent = s.defaultFailureRatePercent ?? 0;
      maxCapturedBodyBytes = s.maxCapturedBodyBytes ?? 65536;
      loadTestRunMaxAgeDays = s.loadTestRunMaxAgeDays;
      loadTestRunMaxRowsPerItem = s.loadTestRunMaxRowsPerItem;
      cors = { ...DEFAULTS.cors, ...(s.cors ?? {}) };
      applySessionTimeoutMinutes(s.sessionTimeoutMinutes ?? 24 * 60);
      applyInactivityLockMinutes(s.inactivityLockMinutes ?? 0);
      setInactivityTimeoutMinutes(s.inactivityLockMinutes ?? 0);
      showToast('Settings saved', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      saving = false;
    }
  }
</script>

<div class="head-row">
  <div>
    <h1>Settings</h1>
    <p class="sub">Instance-wide configuration that applies across every mock and project.</p>
  </div>
</div>

{#if loading}
  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading…</p>
{:else}
  <div class="card form-card">
    <h3 class="form-title">Preferences</h3>
    <p class="sub">Per-browser display preferences — not shared across other browsers/devices, saved instantly.</p>
    <div class="field-row">
      <label>
        UI density
        <select value={$density} on:change={(e) => setDensity(e.target.value)}>
          <option value="comfortable">Comfortable</option>
          <option value="compact">Compact</option>
        </select>
      </label>
    </div>
    <label class="checkbox-row">
      <input type="checkbox" checked={$confirmBeforeDelete} on:change={(e) => setConfirmBeforeDelete(e.target.checked)} />
      <span class="label-text">Confirm before delete<InfoTooltip text="Applies to every confirmation dialog across the app — deleting a mock/collection/project, restoring a version, bulk actions — not only literal deletes. Turning this off skips all of them without asking." /></span>
    </label>
    <div class="sidebar-order-row">
      <span class="label-text">Sidebar order<InfoTooltip text="Drag any item in the sidebar to reorder it — remembered in this browser. This button restores the original order without needing to drag every item back by hand." /></span>
      <button class="btn btn-ghost small" on:click={resetNavOrder}>Reset sidebar order</button>
    </div>
  </div>

  <div class="card form-card">
    <h3 class="form-title">Security</h3>
    <p class="sub">
      Admin login gates the admin UI/API to whoever knows the password. The mock gateway itself (what API clients
      call) is never gated by this.
    </p>

    {#if !$authState?.authRequired}
      <div class="sidebar-order-row">
        <span class="label-text">Admin login<InfoTooltip text="Once enabled, the admin UI/API requires this password — useful for an instance deployed somewhere reachable by more than just this one trusted machine. Nothing is gated until you set a password here." /></span>
        <button class="btn btn-ghost small" on:click={openSetPasswordForm}>Enable login</button>
      </div>
    {:else}
      <div class="sidebar-order-row">
        <span class="label-text">Admin login</span>
        <div class="security-actions">
          <span class="auth-status-badge auth-on">Enabled ({$authState?.credentialType === 'pin' ? 'PIN' : 'Password'})</span>
          <button class="btn btn-ghost small" on:click={openSetPasswordForm}>Change {$authState?.credentialType === 'pin' ? 'PIN' : 'password'}</button>
          <button class="btn btn-ghost small" on:click={turnOffLogin}>Turn off</button>
        </div>
      </div>
      <div class="field-row">
        <label>
          <span class="label-text">Session timeout<InfoTooltip text="How long a login stays valid — rolling, extended on every authenticated request (any backend API call), not a fixed expiry. A restart of the AirMock service always logs everyone out regardless of this value." /></span>
          <div class="session-timeout-input">
            <input type="number" min="1" bind:value={sessionTimeoutValue} />
            <select bind:value={sessionTimeoutUnit}>
              <option value="minutes">Minutes</option>
              <option value="hours">Hours</option>
            </select>
          </div>
        </label>
      </div>

      <label class="checkbox-row">
        <input type="checkbox" bind:checked={inactivityLockEnabled} />
        <span class="label-text">Auto-lock after inactivity<InfoTooltip text="A separate, stricter check from Session timeout above: tracks real mouse/keyboard/scroll activity in the browser and logs out for real after this long with none at all — unlike the session timeout, which only resets on backend API calls and so can stay alive even while you've genuinely stepped away, if the page happens to poll in the background." /></span>
      </label>
      {#if inactivityLockEnabled}
        <div class="field-row">
          <label>
            Lock after
            <div class="session-timeout-input">
              <input type="number" min="1" bind:value={inactivityLockValue} />
              <select bind:value={inactivityLockUnit}>
                <option value="minutes">Minutes</option>
                <option value="hours">Hours</option>
              </select>
            </div>
          </label>
        </div>
      {/if}
    {/if}

    {#if showSetPasswordForm}
      <div class="security-setup-form">
        <div class="field-row">
          <label>
            Type
            <select bind:value={newCredentialType}>
              <option value="password">Password</option>
              <option value="pin">PIN (4–6 digits)</option>
            </select>
          </label>
        </div>
        <div class="field-row">
          <label>
            {newCredentialType === 'pin' ? 'New PIN' : 'New password'}
            <input
              type={newCredentialType === 'pin' ? 'text' : 'password'}
              inputmode={newCredentialType === 'pin' ? 'numeric' : undefined}
              bind:value={newPassword}
            />
          </label>
          <label>
            Confirm
            <input
              type={newCredentialType === 'pin' ? 'text' : 'password'}
              inputmode={newCredentialType === 'pin' ? 'numeric' : undefined}
              bind:value={confirmPassword}
            />
          </label>
        </div>
        {#if setPasswordError}<p class="security-setup-error">{setPasswordError}</p>{/if}
        <div class="save-row">
          <button class="btn btn-primary small" disabled={settingPassword} on:click={saveNewPassword}>Save</button>
          <button class="btn btn-ghost small" on:click={cancelSetPasswordForm}>Cancel</button>
        </div>
      </div>
    {/if}
  </div>

  <div class="card form-card">
    <h3 class="form-title">Hit log retention</h3>
    <p class="sub">
      Controls how much hit-log history is kept before it's automatically purged. Changes apply on the
      next retention pass (runs hourly) — no restart needed.
    </p>
    <div class="field-row">
      <label>
        Max age (days)
        <input type="number" min="1" bind:value={hitLogMaxAgeDays} />
      </label>
      <label>
        Max rows per mock
        <input type="number" min="1" bind:value={hitLogMaxRowsPerMock} />
      </label>
    </div>
    <div class="field-row">
      <label>
        <span class="label-text">Max captured response body (bytes)<InfoTooltip text="Caps how much of a REST/SOAP/GraphQL mock's response body is kept for the hit log — a large mock response is still sent to the client in full either way, only what gets logged is capped. Applies live, no restart needed." /></span>
        <input type="number" min="1" bind:value={maxCapturedBodyBytes} />
      </label>
    </div>
    <div class="field-row">
      <label>
        <span class="label-text">Load test history: max age (days)<InfoTooltip text="Controls how long past load-test runs (and their charts) are kept before being automatically purged. Applies on the next retention pass (runs hourly) — no restart needed." /></span>
        <input type="number" min="1" bind:value={loadTestRunMaxAgeDays} />
      </label>
      <label>
        Load test history: max runs per request
        <input type="number" min="1" bind:value={loadTestRunMaxRowsPerItem} />
      </label>
    </div>
  </div>

  <div class="card form-card">
    <h3 class="form-title">
      <span class="label-text">Browser access (CORS)<InfoTooltip text="Lets a web page served from another origin call your mocks. When on, the gateway answers preflight OPTIONS requests itself and adds Access-Control-Allow-Origin to responses for requests that carry an Origin header. A preflight is answered before routing, so turn this off if you want an OPTIONS mock of your own to answer it. Applies to every REST/SOAP/GraphQL/WS mock, including project ports." /></span>
    </h3>
    <label class="checkbox-row"><input type="checkbox" bind:checked={cors.enabled} /> Add CORS headers and answer preflight requests</label>
    {#if cors.enabled}
      <div class="field-row">
        <label>
          Allowed origins
          <input type="text" bind:value={cors.allowOrigin} placeholder="* or https://app.example, https://other.example" />
        </label>
        <label>
          Allowed methods
          <input type="text" bind:value={cors.allowMethods} />
        </label>
      </div>
      <div class="field-row">
        <label>
          Allowed headers
          <input type="text" bind:value={cors.allowHeaders} placeholder="* reflects what the browser asks for" />
        </label>
        <label>
          Preflight cache (seconds)
          <input type="number" min="0" bind:value={cors.maxAgeSecs} />
        </label>
      </div>
      <label class="checkbox-row"><input type="checkbox" bind:checked={cors.allowCredentials} /> Allow credentials (cookies, Authorization). The request's own origin is echoed instead of *</label>
    {/if}
  </div>

  <div class="card form-card">
    <h3 class="form-title">
      <span class="label-text">New mock defaults<InfoTooltip text="Pre-fills a brand-new REST/SOAP mock's own response delay and fault-injection error rate in the '+ New mock' form — a starting point, not something enforced on mocks that already exist. Each mock's own fields stay independently editable/removable afterward, same as always." /></span>
    </h3>
    <p class="sub">Applies only to mocks created after this is saved — every existing mock keeps whatever it already has.</p>
    <div class="field-row">
      <label>
        Response delay (ms)
        <input type="number" min="0" bind:value={defaultResponseDelayMs} />
      </label>
      <label>
        Failure rate (%)
        <input type="number" min="0" max="100" step="0.1" bind:value={defaultFailureRatePercent} />
      </label>
    </div>
  </div>

  <div class="card form-card">
    <h3 class="form-title">
      <span class="label-text">Version history<InfoTooltip text="Every edit to a mock snapshots its previous state so it can be restored later — this caps how many of those snapshots are kept per mock before the oldest are pruned. Applies instance-wide, across every protocol page." /></span>
    </h3>
    <p class="sub">
      Controls how many past versions are kept per mock for the Restore feature. Changes apply on the
      next edit to any mock — no restart needed.
    </p>
    <div class="field-row">
      <label>
        Max versions per mock
        <input type="number" min="1" bind:value={maxVersionsPerMock} />
      </label>
    </div>
  </div>

  <div class="card form-card">
    <h3 class="form-title">
      <span class="label-text">Redacted headers<InfoTooltip text="These header names are shown as ***REDACTED*** in the Log History instead of their real value, for both requests and responses — matched case-insensitively. Applies to REST/SOAP/GraphQL/WS mocks." /></span>
    </h3>
    <p class="sub">Prevents secrets passed through the gateway (tokens, cookies, API keys) from being written to the hit log in plain text.</p>
    <div class="header-chips">
      {#if redactedHeaders.length === 0}
        <span class="sub">No headers are redacted — everything is logged in plain text.</span>
      {/if}
      {#each redactedHeaders as h}
        <span class="chip header-chip">
          {h}
          <button type="button" class="chip-remove" on:click={() => removeHeader(h)} aria-label="Remove {h}">&times;</button>
        </span>
      {/each}
    </div>
    <div class="field-row add-header-row">
      <input aria-label="Header name to redact"
        type="text"
        placeholder="X-My-Secret-Header"
        bind:value={newHeaderName}
        on:keydown={(e) => e.key === 'Enter' && addHeader()}
      />
      <button type="button" class="btn btn-ghost small" on:click={addHeader}>+ Add</button>
      <button type="button" class="btn btn-ghost small" on:click={resetHeadersToDefault}>Reset to default</button>
    </div>
  </div>

  <div class="save-row">
    <button class="btn btn-primary" on:click={save} disabled={saving}>{saving ? 'Saving…' : 'Save changes'}</button>
    <button class="btn btn-ghost" on:click={resetAllToDefaults} disabled={saving}>Reset to defaults</button>
  </div>

  <div class="card form-card">
    <h3 class="form-title">
      <span class="label-text">Backup — export / import everything<InfoTooltip text="Certificates and bundles, mock projects and mocks, email templates, and the API client's workspaces/environments/collections, plus scheduled events — everything that makes up a 'setup'. NOT included: the shared gateway TLS/SMTP relay settings above, since blindly overwriting a working instance-wide config on import isn't what a restore should do." /></span>
    </h3>
    <p class="sub">
      Import merges the file's contents into what's already here under brand-new ids — nothing existing is
      overwritten or removed. A name that collides with something already configured gets an "(imported)"
      suffix rather than being skipped, so the content still lands.
    </p>
    <div class="field-row backup-actions">
      <a class="btn btn-ghost" href="/api/backup/export" download="airmock-backup.json">Export everything</a>
      <button type="button" class="btn btn-ghost" on:click={triggerImportPicker} disabled={importing}>
        {importing ? 'Importing…' : 'Import from file'}
      </button>
      <input aria-label="Choose a file" type="file" accept="application/json" bind:this={importFileEl} on:change={onImportFileChosen} hidden />
    </div>
    {#if importResult}
      <div class="import-summary">
        {#each Object.entries(importResult) as [key, s]}
          <div class="import-row">
            <span class="import-label">{BACKUP_CATEGORY_LABELS[key] ?? key}</span>
            <span class="chip badge-ok">{s.imported.length} imported</span>
            {#if s.skipped.length}<span class="chip badge-err">{s.skipped.length} skipped</span>{/if}
          </div>
          {#each s.skipped as sk}
            <p class="skip-reason">— {sk.name}: {sk.reason}</p>
          {/each}
        {/each}
      </div>
    {/if}
  </div>

  {#if runtimeConfig}
    <div class="card form-card runtime-card">
      <h3 class="form-title">About</h3>
      <p class="sub">A lightweight, self-contained, cross-protocol API mocking tool.</p>
      <div class="detail-row"><span class="detail-label">Version</span><code>{runtimeConfig.version || 'dev'}</code></div>
      {#if runtimeConfig.commit}
        <div class="detail-row"><span class="detail-label">Build</span><code>{runtimeConfig.commit}{runtimeConfig.dirty ? ' (local changes)' : ''}{runtimeConfig.buildDate ? ' · ' + runtimeConfig.buildDate : ''}</code></div>
      {/if}
      <div class="detail-row"><span class="detail-label">Go runtime</span><code>{runtimeConfig.goVersion}</code></div>
      <div class="detail-row"><span class="detail-label">OS / Arch</span><code>{runtimeConfig.os} / {runtimeConfig.arch}</code></div>
    </div>

    <div class="card form-card runtime-card">
      <h3 class="form-title">Runtime info</h3>
      <p class="sub">Read-only — set via CLI flags or environment variables at startup, not editable here.</p>
      <div class="detail-row"><span class="detail-label">Admin UI address</span><code>{runtimeConfig.adminHost || '0.0.0.0'}:{runtimeConfig.adminPort}</code></div>
      <div class="detail-row"><span class="detail-label">Gateway port</span><code>{runtimeConfig.gatewayPort}</code></div>
      <div class="detail-row"><span class="detail-label">Gateway TLS port</span><code>{runtimeConfig.gatewayTlsPort}</code></div>
    </div>
  {/if}
{/if}

<style>
  .head-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
  .sub { color: var(--muted); font-size: 13px; margin-top: 4px; }
  .form-card { margin-top: 16px; display: flex; flex-direction: column; gap: 12px; max-width: 560px; }
  /* The button is a bare sibling between form-cards, not a card itself, so
     without its own margin it sits flush against the Redacted headers card
     above (no gap the eye reads as "belonging to" it) while the next card's
     own margin-top opens a full 16px gap below — same 16px on both sides
     keeps it visually anchored under Redacted headers instead of looking
     like it drifted into the gap before About. */
  .save-row { margin-top: 16px; max-width: 560px; display: flex; gap: 8px; }
  .form-title { margin: 0; font-size: 14px; }
  .label-text { display: inline-flex; align-items: center; }
  .checkbox-row { display: flex; align-items: center; gap: 8px; font-size: 13px; cursor: pointer; }
  .sidebar-order-row { display: flex; align-items: center; justify-content: space-between; gap: 12px; }
  .auth-status-badge { font-size: 11px; font-weight: 700; letter-spacing: .3px; text-transform: uppercase; padding: 3px 8px; border-radius: 4px; }
  .auth-status-badge.auth-on { color: var(--success); background: rgba(22, 163, 74, .12); }
  .security-actions { display: flex; align-items: center; gap: 8px; flex-shrink: 0; }
  .session-timeout-input { display: flex; gap: 8px; }
  .session-timeout-input input { width: 90px; }
  .security-setup-form { margin-top: 12px; padding-top: 12px; border-top: 1px solid var(--border); }
  .security-setup-error { color: var(--error); font-size: 12px; margin: 8px 0 0; }
  .field-row { display: flex; gap: 12px; align-items: flex-end; }
  /* This row has a bare <input> (no <label> above it, unlike every other
     field-row) sitting next to .btn.small buttons. flex-end (correct for
     label+input columns elsewhere) bottom-aligns mismatched heights, so
     center them instead — and pin BOTH the input and the buttons to the
     exact same explicit height/line-height/box-sizing rather than relying
     on one side's padding happening to compute to the other's min-height,
     which is sensitive to browser/font-metric differences in how padding
     and line-height interact. Matching both sides to one shared explicit
     value is deterministic regardless of that. */
  .add-header-row { align-items: center; }
  .add-header-row input,
  .add-header-row button {
    height: 32px;
    box-sizing: border-box;
    line-height: 1;
  }
  .add-header-row input { padding: 0 10px; }
  label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: .3px; flex: 1; }
  input {
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 8px 10px; font-size: 13px; font-family: inherit; outline: none;
    transition: border .2s; flex: 1;
  }
  input:focus { border-color: var(--primary); }
  button[disabled] { opacity: .6; cursor: default; }

  .header-chips { display: flex; flex-wrap: wrap; gap: 8px; }
  .header-chip {
    display: inline-flex; align-items: center; gap: 6px; font-family: 'SFMono-Regular', Consolas, monospace;
  }
  .chip-remove {
    background: none; border: none; color: inherit; cursor: pointer; font-size: 14px; line-height: 1;
    padding: 0; opacity: .7;
  }
  .chip-remove:hover { opacity: 1; }

  .backup-actions { align-items: center; }
  .import-summary { display: flex; flex-direction: column; gap: 6px; margin-top: 4px; }
  .import-row { display: flex; align-items: center; gap: 8px; font-size: 13px; }
  .import-label { flex: 0 0 160px; color: var(--muted); font-weight: 600; }
  .skip-reason { margin: 0 0 0 168px; font-size: 12px; color: var(--muted); }

  .runtime-card .detail-row { display: flex; align-items: baseline; gap: 10px; font-size: 13px; margin-bottom: 6px; }
  .runtime-card .detail-row:last-child { margin-bottom: 0; }
  .runtime-card .detail-label { flex: 0 0 140px; color: var(--muted); font-size: 12px; font-weight: 600; }
  .runtime-card code {
    font-family: 'SFMono-Regular', Consolas, monospace; font-size: 12px;
    background: var(--surface2, var(--hover)); border-radius: 4px; padding: 2px 6px;
  }
</style>
