<script>
  import { onMount, tick } from 'svelte';
  import { showToast } from '../toast.js';
  import { copyText } from '../clipboard.js';
  import { api } from '../api.js';
  import { pendingFocus, consumePendingFocus } from '../router.js';
  import ContextMenu from '../ContextMenu.svelte';

  let certs = [];
  let bundles = [];
  let gateway = { enabled: false, certId: '', bundleId: '', clientCertMode: '', clientCaId: '', port: '' };
  let loading = true;

  let genForm = emptyGenForm();
  function emptyGenForm() {
    return { name: '', kind: 'ca', commonName: '', sans: '', keyAlgorithm: 'ecdsa', validDays: 365, issuerId: '' };
  }
  let showGenForm = false;

  let bundleForm = emptyBundleForm();
  function emptyBundleForm() {
    return { baseName: '', commonName: '', sans: '', keyAlgorithm: 'ecdsa', validDays: 365, withClientCert: true };
  }
  let showBundleForm = false;
  let bundleBusy = false;

  // "Assemble from existing certs" — a second way to create a bundle,
  // alongside generating three brand-new certs above: pick a CA (required)
  // and optionally a server/client cert already in the store.
  let assembleForm = emptyAssembleForm();
  function emptyAssembleForm() {
    return { name: '', caId: '', serverCertId: '', clientCertId: '' };
  }
  let showAssembleForm = false;

  // Import an existing, externally-issued certificate — as opposed to every
  // form above, which all mint brand-new material. Two input modes share
  // one endpoint: pick files (cert/key/PKCS#12), or paste PEM text directly
  // — whichever the material at hand is already in.
  let importForm = emptyImportForm();
  function emptyImportForm() {
    return {
      name: '', kind: 'server', mode: 'files',
      certFile: null, keyFile: null, p12File: null,
      certPem: '', keyPem: '', passphrase: '',
    };
  }
  let showImportForm = false;
  let importBusy = false;

  function certById(id) {
    return certs.find((c) => c.id === id) ?? null;
  }
  function certName(id) {
    return certById(id)?.name ?? '(deleted)';
  }

  // Certs are shown grouped by bundle rather than in a separate flat list —
  // this is what's left over: anything not referenced as any bundle's own
  // CA/server/client cert (a lone cert generated outside the bundle flow,
  // or one left behind after its bundle was deleted).
  $: ungroupedCerts = certs.filter(
    (c) => !bundles.some((b) => b.caId === c.id || b.serverCertId === c.id || b.clientCertId === c.id)
  );

  let tlsTestTarget = '';
  let tlsTestResult = null;
  let tlsTestError = '';

  let renewingId = '';

  $: cas = certs.filter((c) => c.kind === 'ca');
  $: serverCerts = certs.filter((c) => c.kind === 'server');
  $: clientCerts = certs.filter((c) => c.kind === 'client');

  function daysUntil(iso) {
    return Math.floor((new Date(iso) - Date.now()) / 86400000);
  }
  function expiryStatus(c) {
    const days = daysUntil(c.notAfter);
    if (days < 0) return 'expired';
    if (days <= 30) return 'soon';
    return 'ok';
  }

  async function jreq(method, path, body) {
    const res = await fetch(path, {
      method,
      headers: body ? { 'Content-Type': 'application/json' } : undefined,
      body: body ? JSON.stringify(body) : undefined,
    });
    if (!res.ok) {
      const data = await res.json().catch(() => ({}));
      throw new Error(data.error || `${method} ${path} failed (${res.status})`);
    }
    return res.status === 204 ? null : res.json();
  }

  async function load() {
    loading = true;
    try {
      const [certList, bundleList, gw] = await Promise.all([
        jreq('GET', '/api/certificates'),
        jreq('GET', '/api/cert-bundles'),
        jreq('GET', '/api/gateway/tls'),
      ]);
      certs = certList ?? [];
      bundles = bundleList ?? [];
      gateway = gw;
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      loading = false;
    }
  }

  onMount(load);

  // Jump target from the command palette or a Dashboard link — highlights
  // and scrolls to one specific cert, whether it's grouped in a bundle or
  // sitting in Ungrouped. Same pendingFocus mechanism every other page
  // already consumes (see Mocks.svelte); gated on certs.length so it also
  // fires once load() has actually populated the list.
  let focusedCertId = '';
  $: if ($pendingFocus?.pageId === 'certificates' && certs.length) {
    const focus = consumePendingFocus('certificates');
    if (focus) {
      focusedCertId = focus.itemId;
      tick().then(() => {
        document.getElementById(`cert-${focusedCertId}`)?.scrollIntoView({ behavior: 'smooth', block: 'center' });
      });
    }
  }

  async function generate() {
    try {
      await jreq('POST', '/api/certificates', {
        name: genForm.name,
        kind: genForm.kind,
        commonName: genForm.commonName,
        sans: genForm.sans.split(',').map((s) => s.trim()).filter(Boolean),
        keyAlgorithm: genForm.keyAlgorithm,
        validDays: Number(genForm.validDays) || 365,
        issuerId: genForm.kind === 'ca' ? '' : genForm.issuerId,
      });
      showToast('Certificate generated', 'ok');
      genForm = emptyGenForm();
      showGenForm = false;
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // Generates a CA, a server cert signed by it, and (optionally) a client
  // cert signed by it, all in one action — grouped as a named, persisted
  // Bundle server-side, so applying it to a mock/project/the gateway later
  // is one pick instead of choosing the server cert and CA separately.
  async function generateBundle() {
    if (!bundleForm.baseName.trim()) {
      showToast('Base name is required', 'err');
      return;
    }
    bundleBusy = true;
    try {
      await jreq('POST', '/api/cert-bundles/generate', {
        name: bundleForm.baseName.trim(),
        keyAlgorithm: bundleForm.keyAlgorithm,
        validDays: Number(bundleForm.validDays) || 365,
        commonName: bundleForm.commonName.trim(),
        sans: bundleForm.sans.split(',').map((s) => s.trim()).filter(Boolean),
        withClientCert: bundleForm.withClientCert,
      });
      showToast(`Bundle "${bundleForm.baseName.trim()}" generated`, 'ok');
      bundleForm = emptyBundleForm();
      showBundleForm = false;
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      bundleBusy = false;
    }
  }

  async function assembleBundle() {
    if (!assembleForm.name.trim() || !assembleForm.caId) {
      showToast('Name and a CA are required', 'err');
      return;
    }
    try {
      await jreq('POST', '/api/cert-bundles', {
        name: assembleForm.name.trim(),
        caId: assembleForm.caId,
        serverCertId: assembleForm.serverCertId,
        clientCertId: assembleForm.clientCertId,
      });
      showToast(`Bundle "${assembleForm.name.trim()}" created`, 'ok');
      assembleForm = emptyAssembleForm();
      showAssembleForm = false;
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function importCertFile() {
    if (!importForm.name.trim()) {
      showToast('Name is required', 'err');
      return;
    }
    const isP12 = importForm.mode === 'p12';
    if (isP12 && !importForm.p12File) {
      showToast('A PKCS#12 (.p12/.pfx) file is required', 'err');
      return;
    }
    if (!isP12 && importForm.mode === 'files' && !importForm.certFile) {
      showToast('A certificate file is required', 'err');
      return;
    }
    if (!isP12 && importForm.mode === 'paste' && !importForm.certPem.trim()) {
      showToast('Certificate PEM text is required', 'err');
      return;
    }

    const fd = new FormData();
    fd.set('name', importForm.name.trim());
    fd.set('kind', importForm.kind);
    if (importForm.passphrase) fd.set('passphrase', importForm.passphrase);
    if (isP12) {
      fd.set('p12File', importForm.p12File);
    } else if (importForm.mode === 'files') {
      fd.set('certFile', importForm.certFile);
      if (importForm.keyFile) fd.set('keyFile', importForm.keyFile);
    } else {
      fd.set('certPem', importForm.certPem);
      if (importForm.keyPem.trim()) fd.set('keyPem', importForm.keyPem);
    }

    importBusy = true;
    try {
      await api.importCertificate(fd);
      showToast('Certificate imported', 'ok');
      importForm = emptyImportForm();
      showImportForm = false;
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      importBusy = false;
    }
  }

  async function removeBundle(b) {
    if (!confirm(`Delete bundle "${b.name}"? The certificates it groups are NOT deleted — only the grouping itself.`)) return;
    try {
      await jreq('DELETE', `/api/cert-bundles/${b.id}`);
      showToast('Bundle deleted', 'ok');
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function removeCert(id) {
    const c = certs.find((x) => x.id === id);
    let warning = '';
    try {
      const usage = await jreq('GET', `/api/certificates/${id}/usage`);
      const refs = [];
      if (usage.gatewayServer) refs.push('the gateway HTTPS server certificate');
      if (usage.gatewayClientCa) refs.push('the gateway mTLS trusted client CA');
      if (usage.tcpMocks?.length) refs.push(`${usage.tcpMocks.length} TCP mock(s): ${usage.tcpMocks.map((m) => m.name).join(', ')}`);
      if (usage.smtpMocks?.length) refs.push(`${usage.smtpMocks.length} SMTP mock(s): ${usage.smtpMocks.map((m) => m.name).join(', ')}`);
      if (usage.projects?.length) refs.push(`${usage.projects.length} project(s): ${usage.projects.map((m) => m.name).join(', ')}`);
      if (usage.bundles?.length) refs.push(`${usage.bundles.length} bundle(s): ${usage.bundles.map((m) => m.name).join(', ')}`);
      if (usage.issuedCerts?.length) refs.push(`${usage.issuedCerts.length} certificate(s) it issued: ${usage.issuedCerts.map((m) => m.name).join(', ')}`);
      if (refs.length) {
        warning = `\n\nThis will break:\n- ${refs.join('\n- ')}`;
      }
    } catch {
      // non-fatal: fall back to the generic confirm below
    }
    if (!confirm(`Delete certificate "${c?.name ?? id}"?${warning}\n\nThis cannot be undone.`)) return;
    try {
      await jreq('DELETE', `/api/certificates/${id}`);
      showToast('Certificate deleted', 'ok');
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function renewCert(c) {
    if (!confirm(`Renew "${c.name}"? This regenerates its key with a fresh 365-day validity window, keeping the same id so anything bound to it keeps working.`)) return;
    renewingId = c.id;
    try {
      await jreq('POST', `/api/certificates/${c.id}/renew`, { validDays: 365 });
      showToast('Certificate renewed', 'ok');
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      renewingId = '';
    }
  }

  async function downloadCert(c) {
    try {
      const data = await jreq('GET', `/api/certificates/${c.id}/download`);
      for (const [ext, pem] of [['cert.pem', data.certPem], ['key.pem', data.keyPem]]) {
        const blob = new Blob([pem], { type: 'application/x-pem-file' });
        const a = document.createElement('a');
        a.href = URL.createObjectURL(blob);
        a.download = `${c.name}.${ext}`;
        a.click();
        URL.revokeObjectURL(a.href);
      }
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  // A single portaled ContextMenu is reused for both cert rows and bundle
  // cards — `kind` tells contextMenuItems which action set/item shape to
  // build, since a bundle's only action is "delete the grouping" while a
  // cert's are renew/download/copy/delete.
  let contextMenu = null;
  function openCertContextMenu(e, c) {
    e.preventDefault();
    contextMenu = { x: e.clientX, y: e.clientY, kind: 'cert', item: c };
  }
  function openBundleContextMenu(e, b) {
    e.preventDefault();
    contextMenu = { x: e.clientX, y: e.clientY, kind: 'bundle', item: b };
  }
  function contextMenuItems(menu) {
    if (menu.kind === 'bundle') {
      return [{ label: 'Delete bundle', danger: true, onClick: () => removeBundle(menu.item) }];
    }
    const c = menu.item;
    return [
      { label: renewingId === c.id ? 'Renewing…' : 'Renew', disabled: renewingId === c.id, onClick: () => renewCert(c) },
      { label: 'Download', onClick: () => downloadCert(c) },
      { label: 'Copy common name', onClick: () => copyText(c.commonName ?? '').then(() => showToast('Common name copied', 'ok')) },
      { divider: true },
      { label: 'Delete', danger: true, onClick: () => removeCert(c.id) },
    ];
  }

  async function saveGateway() {
    try {
      gateway = await jreq('PUT', '/api/gateway/tls', {
        enabled: gateway.enabled,
        certId: gateway.certId,
        bundleId: gateway.bundleId,
        clientCertMode: gateway.clientCertMode,
        clientCaId: gateway.clientCaId,
      });
      showToast('Gateway TLS settings saved', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function runTlsTest() {
    tlsTestError = '';
    tlsTestResult = null;
    try {
      tlsTestResult = await jreq('POST', '/api/tools/tls-test', { target: tlsTestTarget });
    } catch (e) {
      tlsTestError = e.message;
    }
  }

  function kindBadge(kind) {
    return kind === 'ca' ? 'badge-info' : kind === 'client' ? 'badge-warn' : 'badge-ok';
  }

  $: expiredCount = certs.filter((c) => expiryStatus(c) === 'expired').length;
  $: expiringSoonCount = certs.filter((c) => expiryStatus(c) === 'soon').length;
</script>

<div class="head-row">
  <div>
    <h1>Certificates</h1>
    <p class="sub">Generate CAs and certs, bind one to the gateway for HTTPS/mTLS, or inspect any TLS endpoint.</p>
  </div>
  <div class="head-actions">
    <button class="btn btn-ghost" on:click={() => (showImportForm = !showImportForm)}>
      {showImportForm ? 'Cancel' : '+ Import certificate'}
    </button>
    <button class="btn btn-ghost" on:click={() => (showAssembleForm = !showAssembleForm)}>
      {showAssembleForm ? 'Cancel' : '+ Bundle from existing certs'}
    </button>
    <button class="btn btn-ghost" on:click={() => (showBundleForm = !showBundleForm)}>
      {showBundleForm ? 'Cancel' : '+ Generate all at once'}
    </button>
    <button class="btn btn-primary" on:click={() => (showGenForm = !showGenForm)}>
      {showGenForm ? 'Cancel' : '+ Generate certificate'}
    </button>
  </div>
</div>

{#if expiredCount > 0 || expiringSoonCount > 0}
  <div class="card expiry-banner">
    {#if expiredCount > 0}<span class="chip chip-stop">{expiredCount} expired</span>{/if}
    {#if expiringSoonCount > 0}<span class="chip badge-warn">{expiringSoonCount} expiring within 30 days</span>{/if}
    <span class="sub">Renew below to extend validity without rebinding anything that references them.</span>
  </div>
{/if}

{#if showImportForm}
  <div class="card form-card import-card">
    <button class="card-minus" on:click={() => (showImportForm = false)} title="Cancel" aria-label="Cancel">−</button>
    <p class="sub">Bring in an externally-issued certificate instead of generating one — a real CA-signed cert, a corporate root, or a cert exported from another tool.</p>
    <div class="field-row">
      <label>
        Name
        <input type="text" bind:value={importForm.name} placeholder="my-imported-cert" />
      </label>
      <label class="kind-field">
        Kind
        <select bind:value={importForm.kind}>
          <option value="ca">CA</option>
          <option value="server">Server</option>
          <option value="client">Client</option>
        </select>
      </label>
    </div>

    <div class="import-mode-tabs">
      <button type="button" class="tab-btn" class:active={importForm.mode === 'files'} on:click={() => (importForm.mode = 'files')}>Cert + key files</button>
      <button type="button" class="tab-btn" class:active={importForm.mode === 'paste'} on:click={() => (importForm.mode = 'paste')}>Paste PEM</button>
      <button type="button" class="tab-btn" class:active={importForm.mode === 'p12'} on:click={() => (importForm.mode = 'p12')}>PKCS#12 / .pfx</button>
    </div>

    {#if importForm.mode === 'files'}
      <label>
        Certificate file (PEM or DER — a fullchain with intermediates is fine)
        <input type="file" accept=".pem,.crt,.cer,.der" on:change={(e) => (importForm.certFile = e.target.files[0] ?? null)} />
      </label>
      <label>
        Private key file (optional — omit to import a trust-only CA/public cert)
        <input type="file" accept=".pem,.key,.der" on:change={(e) => (importForm.keyFile = e.target.files[0] ?? null)} />
      </label>
      <label>
        Passphrase (only if the key file is encrypted)
        <input type="password" bind:value={importForm.passphrase} placeholder="leave blank if not encrypted" />
      </label>
    {:else if importForm.mode === 'paste'}
      <label>
        Certificate PEM (one or more concatenated CERTIFICATE blocks)
        <textarea rows="6" bind:value={importForm.certPem} placeholder="-----BEGIN CERTIFICATE-----&#10;...&#10;-----END CERTIFICATE-----"></textarea>
      </label>
      <label>
        Private key PEM (optional)
        <textarea rows="6" bind:value={importForm.keyPem} placeholder="-----BEGIN PRIVATE KEY-----&#10;...&#10;-----END PRIVATE KEY-----"></textarea>
      </label>
      <label>
        Passphrase (only if the key is encrypted)
        <input type="password" bind:value={importForm.passphrase} placeholder="leave blank if not encrypted" />
      </label>
    {:else}
      <label>
        PKCS#12 / .pfx file
        <input type="file" accept=".p12,.pfx" on:change={(e) => (importForm.p12File = e.target.files[0] ?? null)} />
      </label>
      <label>
        Passphrase
        <input type="password" bind:value={importForm.passphrase} placeholder="the file's export password" />
      </label>
    {/if}

    <button class="btn btn-primary" on:click={importCertFile} disabled={importBusy}>
      {importBusy ? 'Importing…' : 'Import'}
    </button>
  </div>
{/if}

{#if showBundleForm}
  <div class="card form-card import-card">
    <button class="card-minus" on:click={() => (showBundleForm = false)} title="Cancel" aria-label="Cancel">−</button>
    <p class="sub">Creates a CA plus a server and client certificate signed by it, in one go — the common starting set for HTTPS + mTLS testing.</p>
    <div class="field-row">
      <label>
        Base name
        <input type="text" bind:value={bundleForm.baseName} placeholder="mock" />
      </label>
      <label class="algo-field">
        Key
        <select bind:value={bundleForm.keyAlgorithm}>
          <option value="ecdsa">ECDSA</option>
          <option value="rsa">RSA</option>
        </select>
      </label>
      <label class="days-field">
        Valid days
        <input type="number" bind:value={bundleForm.validDays} />
      </label>
    </div>
    <label>
      Server common name
      <input type="text" bind:value={bundleForm.commonName} placeholder="mock.local" />
    </label>
    <label>
      Server SANs (comma-separated, DNS names or IPs)
      <input type="text" bind:value={bundleForm.sans} placeholder="mock.local, 127.0.0.1" />
    </label>
    <label class="checkbox-row">
      <input type="checkbox" bind:checked={bundleForm.withClientCert} />
      Also generate a client certificate (for mTLS testing)
    </label>
    <button class="btn btn-primary" on:click={generateBundle} disabled={bundleBusy}>
      {bundleBusy ? 'Generating…' : bundleForm.withClientCert ? 'Generate CA + server + client' : 'Generate CA + server'}
    </button>
  </div>
{/if}

{#if showAssembleForm}
  <div class="card form-card import-card">
    <button class="card-minus" on:click={() => (showAssembleForm = false)} title="Cancel" aria-label="Cancel">−</button>
    <p class="sub">Groups certificates already in the store into a named bundle — pick a CA (required) and, optionally, a server and/or client cert it issued.</p>
    <label>
      Bundle name
      <input type="text" bind:value={assembleForm.name} placeholder="my-bundle" />
    </label>
    <label>
      CA
      <select bind:value={assembleForm.caId}>
        <option value="">Select a CA…</option>
        {#each cas as ca}<option value={ca.id}>{ca.name}</option>{/each}
      </select>
    </label>
    <label>
      Server certificate (optional)
      <select bind:value={assembleForm.serverCertId}>
        <option value="">None</option>
        {#each serverCerts as c}<option value={c.id}>{c.name}</option>{/each}
      </select>
    </label>
    <label>
      Client certificate (optional)
      <select bind:value={assembleForm.clientCertId}>
        <option value="">None</option>
        {#each clientCerts as c}<option value={c.id}>{c.name}</option>{/each}
      </select>
    </label>
    <button class="btn btn-primary" on:click={assembleBundle}>Create bundle</button>
  </div>
{/if}

{#if showGenForm}
  <div class="card form-card import-card">
    <button class="card-minus" on:click={() => (showGenForm = false)} title="Cancel" aria-label="Cancel">−</button>
    <div class="field-row">
      <label>
        Name
        <input type="text" bind:value={genForm.name} placeholder="my-ca" />
      </label>
      <label class="kind-field">
        Kind
        <select bind:value={genForm.kind}>
          <option value="ca">CA</option>
          <option value="server">Server</option>
          <option value="client">Client</option>
        </select>
      </label>
      <label class="algo-field">
        Key
        <select bind:value={genForm.keyAlgorithm}>
          <option value="ecdsa">ECDSA</option>
          <option value="rsa">RSA</option>
        </select>
      </label>
      <label class="days-field">
        Valid days
        <input type="number" bind:value={genForm.validDays} />
      </label>
    </div>
    {#if genForm.kind !== 'ca'}
      <label>
        Issuer CA
        <select bind:value={genForm.issuerId}>
          <option value="">Select a CA…</option>
          {#each cas as ca}<option value={ca.id}>{ca.name}</option>{/each}
        </select>
      </label>
    {/if}
    <label>
      Common name
      <input type="text" bind:value={genForm.commonName} placeholder="mock.local" />
    </label>
    <label>
      SANs (comma-separated, DNS names or IPs)
      <input type="text" bind:value={genForm.sans} placeholder="mock.local, 127.0.0.1" />
    </label>
    <button class="btn btn-primary" on:click={generate}>Generate</button>
  </div>
{/if}

{#if loading}
  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading…</p>
{:else}
  <h2 class="section-title">Bundles</h2>
  {#if bundles.length === 0}
    <div class="card empty">
      <span class="chip chip-tls">no bundles</span>
      <p>No bundles yet — generate one above, or group existing certs into one.</p>
    </div>
  {:else}
    <div class="cert-list">
      {#each bundles as b (b.id)}
        <div class="card bundle-card row-enter">
          <!-- svelte-ignore a11y_no_static_element_interactions -->
          <div class="bundle-head" on:contextmenu={(e) => openBundleContextMenu(e, b)}>
            <span class="cn">{b.name}</span>
            <span class="expiry">created {new Date(b.createdAt).toLocaleDateString()}</span>
            <button class="btn btn-ghost small remove" on:click={() => removeBundle(b)}>Delete bundle</button>
          </div>
          <div class="bundle-certs">
            {#each [b.caId, b.serverCertId, b.clientCertId] as certId}
              {#if certId && certById(certId)}
                <!-- svelte-ignore a11y_no_static_element_interactions -->
                <div class="cert-subrow" id="cert-{certId}" class:focused={certId === focusedCertId} on:contextmenu={(e) => openCertContextMenu(e, certById(certId))}>
                  {@render bundleCertRow(certById(certId))}
                </div>
              {/if}
            {/each}
          </div>
        </div>
      {/each}
    </div>
  {/if}

  <h2 class="section-title">Ungrouped certificates</h2>
  <p class="sub">Certificates not part of any bundle — generated on their own, or left behind after their bundle was deleted.</p>
  {#if ungroupedCerts.length === 0}
    <div class="card empty">
      <span class="chip chip-tls">none</span>
      <p>Every certificate is part of a bundle.</p>
    </div>
  {:else}
    <div class="cert-list">
      {#each ungroupedCerts as c (c.id)}
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div class="card cert-row row-enter" id="cert-{c.id}" class:focused={c.id === focusedCertId} on:contextmenu={(e) => openCertContextMenu(e, c)}>
          {@render bundleCertRow(c)}
        </div>
      {/each}
    </div>
  {/if}

  <div class="card settings-card">
    <h2>Gateway HTTPS</h2>
    <p class="sub">Port {gateway.port} — only active while enabled below.</p>
    <label class="checkbox-row">
      <input type="checkbox" bind:checked={gateway.enabled} />
      Enable HTTPS on the gateway
    </label>
    <label>
      Bundle (optional — supplies the server cert + CA together)
      <select bind:value={gateway.bundleId} disabled={!gateway.enabled}>
        <option value="">None — pick individually below</option>
        {#each bundles as b}<option value={b.id}>{b.name}</option>{/each}
      </select>
    </label>
    <label>
      Server certificate
      <select bind:value={gateway.certId} disabled={!gateway.enabled || !!gateway.bundleId}>
        <option value="">Select a server cert…</option>
        {#each serverCerts as c}<option value={c.id}>{c.name} ({c.commonName})</option>{/each}
      </select>
    </label>
    <label>
      Client certificate
      <select bind:value={gateway.clientCertMode} disabled={!gateway.enabled}>
        <option value="">None required</option>
        <option value="optional">Optional — verified if presented</option>
        <option value="required">Required — reject connections without one</option>
      </select>
    </label>
    {#if gateway.clientCertMode === 'optional' || gateway.clientCertMode === 'required'}
      <label>
        Trusted client CA
        <select bind:value={gateway.clientCaId} disabled={!gateway.enabled || !!gateway.bundleId}>
          <option value="">Select a CA…</option>
          {#each cas as ca}<option value={ca.id}>{ca.name}</option>{/each}
        </select>
      </label>
    {/if}
    <button class="btn btn-primary" on:click={saveGateway}>Save</button>
  </div>

  <div class="card settings-card">
    <h2>Test TLS / Certificate</h2>
    <p class="sub">Dial any host:port and inspect the certificate chain it presents.</p>
    <div class="field-row">
      <input aria-label="Host and port to inspect" type="text" bind:value={tlsTestTarget} placeholder="localhost:8443" />
      <button class="btn btn-primary" on:click={runTlsTest}>Test</button>
    </div>
    {#if tlsTestError}
      <p class="test-error">{tlsTestError}</p>
    {/if}
    {#if tlsTestResult}
      <div class="test-result">
        {#if tlsTestResult.expired}<span class="chip chip-stop">expired</span>{/if}
        {#if tlsTestResult.expiresSoon}<span class="chip badge-warn">expires soon</span>{/if}
        {#if tlsTestResult.hostnameMismatch}<span class="chip chip-stop">hostname mismatch</span>{/if}
        {#if tlsTestResult.verificationError}<span class="chip badge-warn">{tlsTestResult.verificationError}</span>{/if}
        {#each tlsTestResult.chain as entry, i}
          <div class="chain-entry">
            <strong>{i === 0 ? 'Leaf' : 'Chain #' + i}</strong>
            <div>Subject: {entry.subject}</div>
            <div>Issuer: {entry.issuer}</div>
            <div>Valid: {new Date(entry.notBefore).toLocaleDateString()} – {new Date(entry.notAfter).toLocaleDateString()}</div>
          </div>
        {/each}
      </div>
    {/if}
  </div>
{/if}

{#snippet bundleCertRow(c)}
  <span class="badge {kindBadge(c.kind)}">{c.kind}</span>
  <span class="cn">{c.commonName}</span>
  <span class="chip chip-stat">{c.keyAlgorithm}</span>
  {#if expiryStatus(c) === 'expired'}
    <span class="chip chip-stop">expired {Math.abs(daysUntil(c.notAfter))}d ago</span>
  {:else if expiryStatus(c) === 'soon'}
    <span class="chip badge-warn">expires in {daysUntil(c.notAfter)}d</span>
  {:else}
    <span class="expiry">expires {new Date(c.notAfter).toLocaleDateString()}</span>
  {/if}
  <button class="btn btn-ghost small" on:click={() => renewCert(c)} disabled={renewingId === c.id}>
    {renewingId === c.id ? 'Renewing…' : 'Renew'}
  </button>
  <button class="btn btn-ghost small" on:click={() => downloadCert(c)}>Download</button>
  <button class="btn btn-ghost small remove" on:click={() => removeCert(c.id)}>Delete</button>
{/snippet}

{#if contextMenu}
  <ContextMenu x={contextMenu.x} y={contextMenu.y} items={contextMenuItems(contextMenu)} onClose={() => (contextMenu = null)} />
{/if}

<style>
  .head-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; flex-wrap: wrap; }
  .head-actions { display: flex; flex-wrap: wrap; justify-content: flex-end; gap: 10px; min-width: 0; }
  .expiry-banner { margin-top: 16px; display: flex; align-items: center; gap: 10px; flex-wrap: wrap; }
  .sub { color: var(--muted); font-size: 13px; margin-top: 4px; }
  .form-card, .settings-card { margin-top: 16px; display: flex; flex-direction: column; gap: 12px; }
  .import-card { position: relative; padding-right: 40px; }
  .card-minus {
    position: absolute; top: 10px; right: 10px;
    width: 22px; height: 22px; display: flex; align-items: center; justify-content: center;
    background: none; border: 1px solid var(--border); border-radius: 6px; color: var(--muted);
    cursor: pointer; font-size: 15px; line-height: 1; padding: 0;
    transition: color .15s, border-color .15s, background .15s;
  }
  .card-minus:hover { color: var(--error, #dc2626); border-color: var(--error, #dc2626); background: rgba(220,38,38,.1); }
  .field-row { display: flex; gap: 12px; align-items: flex-end; }
  .kind-field, .algo-field { flex: 0 0 110px; }
  .days-field { flex: 0 0 100px; }
  label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: .3px; flex: 1; }
  .checkbox-row { flex-direction: row; align-items: center; gap: 8px; text-transform: none; font-weight: 600; }
  input[type=text], input[type=number], input[type=password], input[type=file], select, textarea {
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 8px 10px; font-size: 13px; font-family: inherit; outline: none;
  }
  textarea { font-family: 'SFMono-Regular', Consolas, monospace; font-size: 12px; resize: vertical; }
  input[type=checkbox] { width: 16px; height: 16px; }

  .import-mode-tabs { display: flex; gap: 8px; }
  .tab-btn {
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--muted);
    border-radius: 6px; padding: 7px 14px; font-size: 12px; font-weight: 700; cursor: pointer;
  }
  .tab-btn.active { border-color: var(--primary); color: var(--primary); }

  .cert-list { margin-top: 20px; display: flex; flex-direction: column; gap: 10px; }
  .focused { outline: 2px solid var(--primary); outline-offset: 2px; }
  .cert-row { display: flex; align-items: center; gap: 14px; padding: 14px 18px; }
  .bundle-card { display: flex; flex-direction: column; gap: 10px; padding: 14px 18px; }
  .bundle-head { display: flex; align-items: center; gap: 14px; }
  .bundle-certs { display: flex; flex-direction: column; gap: 4px; }
  .cert-subrow {
    display: flex; align-items: center; gap: 14px; padding: 8px 10px;
    background: var(--surface2, var(--hover)); border-radius: 6px;
  }
  .cn { font-family: 'SFMono-Regular', Consolas, monospace; font-size: 13px; }
  .expiry { color: var(--muted); font-size: 12px; margin-left: auto; }
  .remove { margin-left: 4px; }
  .empty { margin-top: 20px; display: flex; flex-direction: column; gap: 10px; align-items: flex-start; }

  h2 { font-size: 15px; margin: 0; }
  .section-title { font-size: 15px; margin: 24px 0 0; }
  .test-error { color: var(--error); font-size: 13px; }
  .test-result { display: flex; flex-direction: column; gap: 8px; }
  .chain-entry { font-size: 12px; color: var(--muted); border-top: 1px solid var(--border); padding-top: 8px; }
  .chain-entry strong { color: var(--text); display: block; margin-bottom: 2px; }

  /* Deliberately last: same bug as the mock-listing pages' .mock-row (see
     theme.css) in a different file with different class names — .cert-row/
     .cert-subrow/.bundle-head are all plain unwrapped flex rows, and .cn
     (the cert/bundle name) has no flex-shrink guard, so on a narrow screen
     the Renew/Download/Delete buttons were rendering past the card's right
     edge with no way to reach them (unlike .mock-card, .card itself
     doesn't clip overflow, so here they just visibly spilled off the
     viewport rather than being invisibly clipped — same underlying defect,
     different symptom). Same fix: name gets its own full-width line,
     everything else (badges/chips/buttons) wraps onto the line(s) after. */
  @media (max-width: 640px) {
    .cert-row, .cert-subrow, .bundle-head { flex-wrap: wrap; row-gap: 8px; }
    .cn { order: -1; flex: 1 1 100%; white-space: normal; overflow-wrap: anywhere; }
    .expiry { margin-left: 0; }
  }
</style>
