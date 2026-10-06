<script>
  import { onMount, onDestroy } from 'svelte';
  import { authState, login } from './auth.js';

  let password = '';
  let error = '';
  let loading = false;
  let passwordEl;
  let shake = false;
  let shakeTimer = null;

  // Toggling shake straight from false to true within the same tick
  // wouldn't restart the CSS animation if a previous shake were still
  // mid-flight (Svelte only re-renders the DOM class when the bound value
  // actually changes) — the rAF boundary below forces a paint in between
  // so a second wrong attempt right after the first still visibly shakes.
  function triggerShake() {
    shake = false;
    requestAnimationFrame(() => {
      shake = true;
      clearTimeout(shakeTimer);
      shakeTimer = setTimeout(() => (shake = false), 400);
    });
  }

  $: isPin = $authState?.credentialType === 'pin';
  // Digits in the configured PIN, from the server; 0 means not known yet (a
  // PIN saved before the length was recorded: one successful login teaches
  // it), in which case nothing auto-submits and Enter / "Log in" is used.
  $: pinLength = $authState?.pinLength ?? 0;
  $: maxDigits = pinLength > 0 ? pinLength : 6;
  $: minDigits = pinLength > 0 ? pinLength : 4;
  $: dotCount = pinLength > 0 ? pinLength : Math.max(4, password.length);

  onMount(() => passwordEl?.focus());

  async function submit() {
    if (!password || loading) return;
    loading = true;
    error = '';
    try {
      await login(password);
    } catch (e) {
      error = e.status === 429 ? e.message : `Incorrect ${isPin ? 'PIN' : 'password'}`;
      password = '';
      triggerShake();
    } finally {
      loading = false;
    }
  }

  // PIN mode only — password mode uses a plain <input> + <form> submit.
  // Submits automatically once the PIN reaches its configured length (known
  // from the server), so a 6-digit PIN is never cut off at 4 digits. When the
  // length is not known yet, nothing auto-submits: press Enter or "Log in".
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
    if (!isPin || loading) return;
    if (e.key >= '0' && e.key <= '9') pressDigit(e.key);
    else if (e.key === 'Backspace') backspace();
    else if (e.key === 'Enter') submit();
  }
  onMount(() => window.addEventListener('keydown', onKeydown));
  onDestroy(() => {
    window.removeEventListener('keydown', onKeydown);
    clearTimeout(shakeTimer);
  });
</script>

<div class="login-shell">
  <div class="login-card" class:shake>
    <h1>AirMock</h1>
    <p class="sub">Enter the admin {isPin ? 'PIN' : 'password'} to continue.</p>

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
      <button type="button" class="btn btn-primary login-btn" disabled={password.length < minDigits || loading} on:click={submit}>
        Log in
      </button>
    {:else}
      <form on:submit|preventDefault={submit}>
        <input aria-label="Password" type="password" class="password-input" placeholder="Password" bind:value={password} bind:this={passwordEl} />
        <button type="submit" class="btn btn-primary login-btn" disabled={!password || loading}>Log in</button>
      </form>
    {/if}

    {#if error}<p class="login-error">{error}</p>{/if}
  </div>
</div>

<style>
  .login-shell {
    position: fixed; inset: 0; z-index: 10000;
    display: flex; align-items: center; justify-content: center;
    background: var(--bg);
  }
  .login-card {
    width: 320px; max-width: 90vw; text-align: center;
    background: var(--card); border: 1px solid var(--border); border-radius: 12px;
    padding: 32px 28px; box-shadow: 0 12px 32px rgba(0, 0, 0, 0.18);
  }
  h1 { margin: 0 0 4px; font-size: 20px; color: var(--text); }
  .sub { margin: 0 0 18px; font-size: 13px; color: var(--muted); }
  .password-input {
    width: 100%; box-sizing: border-box; padding: 8px 10px; margin-bottom: 12px;
    border: 1px solid var(--border); border-radius: 6px; background: var(--card); color: var(--text); font-size: 14px;
  }
  .login-btn { width: 100%; }
  .login-error { color: var(--error); font-size: 12px; margin: 10px 0 0; }

  .login-card.shake { animation: login-shake 0.4s cubic-bezier(.36,.07,.19,.97) both; }
  @keyframes login-shake {
    10%, 90% { transform: translateX(-1px); }
    20%, 80% { transform: translateX(2px); }
    30%, 50%, 70% { transform: translateX(-4px); }
    40%, 60% { transform: translateX(4px); }
  }
  @media (prefers-reduced-motion: reduce) {
    .login-card.shake { animation: none; }
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
  }
  .keypad-btn:hover { background: var(--hover, var(--surface2)); }
  .keypad-btn:active { background: var(--hover, var(--surface2)); transform: scale(0.94); }
  .keypad-backspace { font-size: 16px; }
</style>
