<script>
  import Modal from './Modal.svelte';
  import { showToast } from './toast.js';
  import { api } from './api.js';

  // workspace is the full {id, name, locked, lockCredentialType} object.
  export let workspace;
  export let onDone = () => {}; // called after a successful lock/change/remove — caller should re-fetch workspaces
  export let onCancel = () => {};

  // Changing or removing an already-set lock needs the CURRENT password
  // first (the user's own requirement) — "change" is the default action
  // once locked, since that's the more common of the two.
  let mode = workspace?.locked ? 'change' : 'set';
  let credentialType = 'password';
  let currentPassword = '';
  let newPassword = '';
  let loading = false;
  let error = '';

  async function submitSetOrChange() {
    loading = true;
    error = '';
    try {
      await api.lockWorkspace(workspace.id, {
        credentialType,
        newPassword,
        currentPassword: workspace.locked ? currentPassword : undefined,
      });
      showToast(workspace.locked ? 'Workspace lock changed' : 'Workspace locked', 'ok');
      onDone();
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
  }

  async function submitRemove() {
    loading = true;
    error = '';
    try {
      await api.removeWorkspaceLock(workspace.id, currentPassword);
      showToast('Workspace lock removed', 'ok');
      onDone();
    } catch (e) {
      error = e.message;
    } finally {
      loading = false;
    }
  }
</script>

<Modal title={workspace?.locked ? 'Manage workspace lock' : 'Lock workspace'} onClose={onCancel} width="420px">
  <p class="sub">
    {#if workspace?.locked}
      <strong>{workspace.name}</strong> is currently locked ({workspace.lockCredentialType === 'pin' ? 'PIN' : 'password'}).
    {:else}
      Set a PIN or password on <strong>{workspace?.name}</strong> — editing or deleting a mock mapped to it will then require this.
    {/if}
  </p>

  {#if workspace?.locked}
    <div class="mode-tabs">
      <button type="button" class:active={mode === 'change'} on:click={() => (mode = 'change')}>Change lock</button>
      <button type="button" class:active={mode === 'remove'} on:click={() => (mode = 'remove')}>Remove lock</button>
    </div>
  {/if}

  {#if mode === 'remove'}
    <form on:submit|preventDefault={submitRemove}>
      <label>
        Current password
        <input type="password" bind:value={currentPassword} autocomplete="off" />
      </label>
      <button type="submit" class="btn btn-primary full-width" disabled={!currentPassword || loading}>Remove lock</button>
    </form>
  {:else}
    <form on:submit|preventDefault={submitSetOrChange}>
      {#if workspace?.locked}
        <label>
          Current password
          <input type="password" bind:value={currentPassword} autocomplete="off" />
        </label>
      {/if}
      <label>
        New credential type
        <select bind:value={credentialType}>
          <option value="password">Password</option>
          <option value="pin">PIN (4-6 digits)</option>
        </select>
      </label>
      <label>
        New {credentialType === 'pin' ? 'PIN' : 'password'}
        <input
          type={credentialType === 'pin' ? 'text' : 'password'}
          inputmode={credentialType === 'pin' ? 'numeric' : undefined}
          bind:value={newPassword}
          autocomplete="off"
        />
      </label>
      <button
        type="submit"
        class="btn btn-primary full-width"
        disabled={!newPassword || (workspace?.locked && !currentPassword) || loading}
      >
        {workspace?.locked ? 'Change lock' : 'Set lock'}
      </button>
    </form>
  {/if}

  {#if error}<p class="lock-error">{error}</p>{/if}
  <button type="button" class="btn btn-ghost full-width cancel-btn" on:click={onCancel}>Cancel</button>
</Modal>

<style>
  .sub { margin: 0 0 14px; font-size: 13px; color: var(--muted); }
  form { display: flex; flex-direction: column; gap: 12px; }
  label { display: flex; flex-direction: column; gap: 4px; font-size: 12px; font-weight: 600; color: var(--muted); }
  input, select {
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 7px 10px; font-size: 13px; font-weight: 500; font-family: inherit; outline: none;
  }
  .full-width { width: 100%; }
  .cancel-btn { margin-top: 10px; }
  .lock-error { color: var(--error); font-size: 12px; margin: 10px 0 0; }
  .mode-tabs { display: flex; gap: 4px; margin-bottom: 14px; border-bottom: 1px solid var(--border); }
  .mode-tabs button {
    background: none; border: none; padding: 8px 12px; font-size: 12px; font-weight: 700;
    color: var(--muted); cursor: pointer; border-bottom: 2px solid transparent; margin-bottom: -1px;
  }
  .mode-tabs button.active { color: var(--primary); border-bottom-color: var(--primary); }
</style>
