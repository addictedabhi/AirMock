<script>
  import { onMount } from 'svelte';
  import Modal from './Modal.svelte';
  import { api } from './api.js';

  // workspace is the full {id, name, locked, lockCredentialType} object
  // (from api.listWorkspaces()) — not just an id, so the title/PIN-vs-
  // password choice don't need a second round-trip to resolve.
  export let workspace;
  export let onUnlock = () => {};
  export let onCancel = () => {};
  // modalTitle/subtitle/confirmLabel/verify let a caller repurpose this
  // same PIN-keypad-or-password-input shell for "confirm the CURRENT
  // password to do something more destructive than an edit" (e.g.
  // Collections.svelte deleting the whole workspace, which must demand
  // the password fresh every time rather than accepting an existing
  // session unlock) instead of only ever unlocking for the rest of the
  // session — default behavior (unlock via api.unlockWorkspace) is
  // unchanged when none of these are passed.
  export let modalTitle = 'Unlocking workspace';
  export let subtitle = null;
  export let confirmLabel = null;
  export let verify = null;

  let password = '';
  let error = '';
  let loading = false;
  let shake = false;
  let passwordEl;
  onMount(() => passwordEl?.focus());
  let shakeTimer = null;

  $: isPin = workspace?.lockCredentialType === 'pin';
  // Digits in the PIN, from the server; 0 = not known yet (older lock; one
  // correct unlock teaches it), so nothing auto-submits until then.
  $: pinLength = workspace?.lockPinLength ?? 0;
  $: maxDigits = pinLength > 0 ? pinLength : 6;
  $: minDigits = pinLength > 0 ? pinLength : 4;
  $: dotCount = pinLength > 0 ? pinLength : Math.max(4, password.length);

  // Same rAF-boundary trick as Login.svelte's own triggerShake — toggling
  // straight false->true within one tick wouldn't restart the CSS
  // animation if a previous shake were still mid-flight.
  function triggerShake() {
    shake = false;
    requestAnimationFrame(() => {
      shake = true;
      clearTimeout(shakeTimer);
      shakeTimer = setTimeout(() => (shake = false), 400);
    });
  }

  async function submit() {
    if (!password || loading) return;
    loading = true;
    error = '';
    try {
      if (verify) await verify(password);
      else await api.unlockWorkspace(workspace.id, password);
      onUnlock();
    } catch (e) {
      error = e.status === 401 ? `Incorrect ${isPin ? 'PIN' : 'password'}` : e.message;
      password = '';
      triggerShake();
    } finally {
      loading = false;
    }
  }

  function pressDigit(d) {
    if (password.length >= maxDigits) return;
    password += d;
    error = '';
    if (pinLength > 0 && password.length === pinLength) submit();
  }
  function backspace() {
    password = password.slice(0, -1);
    error = '';
  }
  function onKeydown(e) {
    if (loading) return;
    if (isPin) {
      if (e.key >= '0' && e.key <= '9') pressDigit(e.key);
      else if (e.key === 'Backspace') backspace();
      else if (e.key === 'Enter') submit();
    }
  }
</script>

<svelte:window on:keydown={onKeydown} />

<Modal title={modalTitle} onClose={onCancel}>
  <div class="unlock-card" class:shake>
    <p class="sub">
      {#if subtitle}
        {subtitle}
      {:else}
        <strong>{workspace?.name ?? 'This workspace'}</strong> is locked — enter its {isPin ? 'PIN' : 'password'} to continue.
      {/if}
    </p>

    {#if isPin}
      <div class="pin-dots">
        {#each Array(dotCount) as _, i}
          <span class="pin-dot" class:filled={i < password.length}></span>
        {/each}
      </div>
      <div class="keypad">
        {#each ['1', '2', '3', '4', '5', '6', '7', '8', '9'] as d}
          <button type="button" class="keypad-btn" disabled={loading} on:click={() => pressDigit(d)}>{d}</button>
        {/each}
        <span></span>
        <button type="button" class="keypad-btn" disabled={loading} on:click={() => pressDigit('0')}>0</button>
        <button type="button" class="keypad-btn keypad-backspace" disabled={loading} on:click={backspace} aria-label="Backspace">⌫</button>
      </div>
      <button type="button" class="btn btn-primary unlock-btn" disabled={password.length < minDigits || loading} on:click={submit}>
        {confirmLabel ?? 'Unlock'}
      </button>
    {:else}
      <form on:submit|preventDefault={submit}>
        <input aria-label="Password" type="password" class="password-input" placeholder="Password" bind:value={password} bind:this={passwordEl} />
        <button type="submit" class="btn btn-primary unlock-btn" disabled={!password || loading}>{confirmLabel ?? 'Unlock'}</button>
      </form>
    {/if}

    {#if error}<p class="unlock-error">{error}</p>{/if}

    <button type="button" class="btn btn-ghost cancel-btn" on:click={onCancel}>Cancel</button>
  </div>
</Modal>

<style>
  .unlock-card { text-align: center; }
  .sub { margin: 0 0 18px; font-size: 13px; color: var(--muted); }
  .password-input {
    width: 100%; box-sizing: border-box; padding: 8px 10px; margin-bottom: 12px;
    border: 1px solid var(--border); border-radius: 6px; background: var(--card); color: var(--text); font-size: 14px;
  }
  .unlock-btn { width: 100%; }
  .unlock-error { color: var(--error); font-size: 12px; margin: 10px 0 0; }
  .cancel-btn { width: 100%; margin-top: 10px; }

  .unlock-card.shake { animation: unlock-shake 0.4s cubic-bezier(.36,.07,.19,.97) both; }
  @keyframes unlock-shake {
    10%, 90% { transform: translateX(-1px); }
    20%, 80% { transform: translateX(2px); }
    30%, 50%, 70% { transform: translateX(-4px); }
    40%, 60% { transform: translateX(4px); }
  }
  @media (prefers-reduced-motion: reduce) {
    .unlock-card.shake { animation: none; }
  }

  .pin-dots { display: flex; justify-content: center; gap: 10px; margin-bottom: 18px; }
  .pin-dot {
    width: 12px; height: 12px; border-radius: 50%; border: 1px solid var(--border);
    background: transparent; transition: background 0.1s ease;
  }
  .pin-dot.filled { background: var(--primary); border-color: var(--primary); }

  .keypad {
    display: grid; grid-template-columns: repeat(3, 56px); justify-content: center; gap: 10px; margin-bottom: 16px;
  }
  .keypad-btn {
    width: 56px; height: 56px; border-radius: 50%; border: 1px solid var(--border);
    background: var(--surface, var(--card)); color: var(--text); font-size: 18px; font-weight: 600;
    cursor: pointer; transition: background 0.1s ease, transform 0.1s ease;
    -webkit-user-select: none; user-select: none; touch-action: manipulation;
    -webkit-tap-highlight-color: transparent;
  }
  .keypad-btn:hover { background: var(--hover, var(--surface2)); }
  .keypad-btn:active { background: var(--hover, var(--surface2)); transform: scale(0.94); }
  .keypad-backspace { font-size: 16px; }
</style>
