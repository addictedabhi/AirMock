<script>
  import { onMount } from 'svelte';
  import { api } from '../api.js';
  import { showToast } from '../toast.js';
  import { copyText } from '../clipboard.js';
  import ContextMenu from '../ContextMenu.svelte';

  let settings = { host: '', port: 587, username: '', passwordSet: false, fromName: '', fromAddress: '', useTls: true };
  let password = ''; // separate from settings.passwordSet — see saveSettings
  let templates = [];
  let loading = true;
  let savingSettings = false;

  let testTo = '';
  let testing = false;

  let showTemplateForm = false;
  let editingTemplateId = '';
  let templateForm = emptyTemplateForm();
  function emptyTemplateForm() {
    return { name: '', subject: '', htmlBody: '<p>Hello,</p>\n<p>{{.Request.Body}}</p>' };
  }

  async function load() {
    loading = true;
    try {
      [settings, templates] = await Promise.all([api.getSmtpSettings(), api.listEmailTemplates()]);
      templates = templates ?? [];
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      loading = false;
    }
  }

  onMount(load);

  async function saveSettings() {
    savingSettings = true;
    try {
      settings = await api.saveSmtpSettings({ ...settings, password });
      password = '';
      showToast('SMTP settings saved', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      savingSettings = false;
    }
  }

  async function runTestSend() {
    if (!testTo.trim()) {
      showToast('Enter a recipient address first', 'err');
      return;
    }
    testing = true;
    try {
      await api.testSmtpSend(testTo.trim());
      showToast('Test email sent', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      testing = false;
    }
  }

  function openNewTemplate() {
    editingTemplateId = '';
    templateForm = emptyTemplateForm();
    showTemplateForm = true;
  }

  function openEditTemplate(t) {
    editingTemplateId = t.id;
    templateForm = { name: t.name, subject: t.subject, htmlBody: t.htmlBody };
    showTemplateForm = true;
  }

  async function saveTemplate() {
    if (!templateForm.name.trim()) {
      showToast('Name is required', 'err');
      return;
    }
    try {
      if (editingTemplateId) {
        await api.updateEmailTemplate(editingTemplateId, templateForm);
      } else {
        await api.createEmailTemplate(templateForm);
      }
      showTemplateForm = false;
      showToast('Template saved', 'ok');
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  async function removeTemplate(t) {
    if (!confirm(`Delete email template "${t.name}"? Any mock referencing it will fall back to its own inline subject/body.`)) return;
    try {
      await api.deleteEmailTemplate(t.id);
      showToast('Template deleted', 'ok');
      await load();
    } catch (e) {
      showToast(e.message, 'err');
    }
  }

  let contextMenu = null;
  function openContextMenu(e, t) {
    e.preventDefault();
    contextMenu = { x: e.clientX, y: e.clientY, template: t };
  }
  function contextMenuItems(t) {
    return [
      { label: 'Edit', onClick: () => openEditTemplate(t) },
      { label: 'Copy subject', onClick: () => copyText(t.subject ?? '').then(() => showToast('Subject copied', 'ok')) },
      { divider: true },
      { label: 'Delete', danger: true, onClick: () => removeTemplate(t) },
    ];
  }
</script>

<div class="head-row">
  <div>
    <h1>SMTP</h1>
    <p class="sub">Configure the outgoing relay an async mock's "email" callback channel sends through, and reusable HTML email templates.</p>
  </div>
</div>

{#if loading}
  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading…</p>
{:else}
  <div class="card settings-card">
    <h2>Relay settings</h2>
    <div class="field-row">
      <label class="flex-2">
        Host
        <input type="text" bind:value={settings.host} placeholder="smtp.example.com" />
      </label>
      <label class="port-field">
        Port
        <input type="number" bind:value={settings.port} />
      </label>
    </div>
    <div class="field-row">
      <label>
        Username
        <input type="text" bind:value={settings.username} placeholder="optional" />
      </label>
      <label>
        Password {settings.passwordSet && !password ? '(already set — leave blank to keep it)' : ''}
        <input type="password" bind:value={password} placeholder={settings.passwordSet ? '••••••••' : ''} />
      </label>
    </div>
    <div class="field-row">
      <label>
        From name <span class="optional">(personal name shown to recipients, optional)</span>
        <input type="text" bind:value={settings.fromName} placeholder="AirMock Notifications" />
      </label>
      <label>
        From address
        <input type="text" bind:value={settings.fromAddress} placeholder="mock@example.com" />
      </label>
      <label class="checkbox-row">
        <input type="checkbox" bind:checked={settings.useTls} />
        Use STARTTLS
      </label>
    </div>
    <button class="btn btn-primary" on:click={saveSettings} disabled={savingSettings}>
      {savingSettings ? 'Saving…' : 'Save'}
    </button>
  </div>

  <div class="card settings-card">
    <h2>Test send</h2>
    <p class="sub">Sends a real email through the settings above — verify the relay works before any mock depends on it.</p>
    <div class="field-row">
      <input aria-label="Test email recipient" type="text" bind:value={testTo} placeholder="you@example.com" />
      <button class="btn btn-primary" on:click={runTestSend} disabled={testing}>{testing ? 'Sending…' : 'Send test email'}</button>
    </div>
  </div>

  <div class="head-row templates-head">
    <h2>Email templates</h2>
    <button class="btn btn-primary" on:click={() => (showTemplateForm ? (showTemplateForm = false) : openNewTemplate())}>
      {showTemplateForm ? 'Cancel' : '+ New template'}
    </button>
  </div>

  {#if showTemplateForm}
    <div class="card form-card">
      <label>
        Name
        <input type="text" bind:value={templateForm.name} placeholder="Order shipped" />
      </label>
      <label>
        Subject
        <input type="text" bind:value={templateForm.subject} placeholder="Order {'{{'}.Request.Body.orderId{'}}'} shipped" />
      </label>
      <label>
        HTML body
        <textarea rows="8" bind:value={templateForm.htmlBody}></textarea>
      </label>
      <p class="sub">Subject and body both support {'{{'}placeholder{'}}'} substitution from the triggering request (e.g. {'{{'}.Request.Body.orderId{'}}'}, {'{{'}.Request.Headers.X-Foo{'}}'}) — the same templating every mock response uses.</p>
      <button class="btn btn-primary" on:click={saveTemplate}>Save template</button>
    </div>
  {/if}

  {#if templates.length === 0}
    <div class="card empty"><p>No email templates yet — create one above, or an async mock's email callback can just use its own inline subject/body.</p></div>
  {:else}
    <div class="template-list">
      {#each templates as t (t.id)}
        <!-- svelte-ignore a11y_no_static_element_interactions -->
        <div class="card template-row row-enter" on:contextmenu={(e) => openContextMenu(e, t)}>
          <span class="template-name">{t.name}</span>
          <span class="template-subject">{t.subject}</span>
          <button class="btn btn-ghost" on:click={() => openEditTemplate(t)}>Edit</button>
          <button class="btn btn-ghost remove" on:click={() => removeTemplate(t)}>Delete</button>
        </div>
      {/each}
    </div>
  {/if}
{/if}

{#if contextMenu}
  <ContextMenu x={contextMenu.x} y={contextMenu.y} items={contextMenuItems(contextMenu.template)} onClose={() => (contextMenu = null)} />
{/if}

<style>
  .head-row { display: flex; align-items: flex-start; justify-content: space-between; gap: 16px; }
  .sub { color: var(--muted); font-size: 13px; margin-top: 4px; }
  .settings-card, .form-card { margin-top: 16px; display: flex; flex-direction: column; gap: 12px; }
  .field-row { display: flex; gap: 12px; align-items: flex-end; }
  .flex-2 { flex: 2; }
  .port-field { flex: 0 0 100px; }
  label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: .3px; flex: 1; }
  .optional { text-transform: none; font-weight: 400; letter-spacing: 0; }
  .checkbox-row { flex-direction: row; align-items: center; gap: 8px; text-transform: none; font-weight: 600; flex: 0 0 auto; white-space: nowrap; }
  input[type=text], input[type=number], input[type=password], textarea {
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 8px 10px; font-size: 13px; font-family: inherit; outline: none;
  }
  textarea { font-family: 'SFMono-Regular', Consolas, monospace; resize: vertical; }
  input[type=checkbox] { width: 16px; height: 16px; }

  .templates-head { margin-top: 24px; align-items: center; }
  h2 { font-size: 15px; margin: 0; }

  .template-list { margin-top: 16px; display: flex; flex-direction: column; gap: 10px; }
  .template-row { display: flex; align-items: center; gap: 14px; padding: 14px 18px; }
  .template-name { font-weight: 700; font-size: 14px; flex: 0 0 200px; }
  .template-subject { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; color: var(--muted); font-size: 13px; }
  .empty { margin-top: 16px; }
</style>
