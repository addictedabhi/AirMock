<script>
  import { onMount, onDestroy } from 'svelte';
  import { api } from './api.js';
  import { showToast } from './toast.js';

  // mockId/protocol identify which mock's sessions to poll; canSend hides
  // the per-row "Send" control entirely for the 3 protocols with no
  // server-push verb (SMTP/Kafka/Diameter) rather than showing a button
  // that would just fail every time it's clicked.
  export let mockId;
  export let protocol;
  export let canSend = false;

  let sessions = [];
  let loading = true;
  let pollTimer = null;
  let closingId = '';
  let sendOpenId = '';
  let sendPayload = '';
  let sendTopic = '';
  let sendSourceAddr = '';
  let sendDestAddr = '';
  let sendAddress = '';
  let sending = false;

  async function load() {
    try {
      sessions = (await api.listSessions(mockId)) ?? [];
    } catch {
      // non-fatal transient poll failure — keep showing the last good list
      // rather than flashing an error toast every 3 seconds
    } finally {
      loading = false;
    }
  }

  onMount(() => {
    load();
    pollTimer = setInterval(load, 3000);
  });
  onDestroy(() => clearInterval(pollTimer));

  function relativeTime(iso) {
    const seconds = Math.max(0, Math.round((Date.now() - new Date(iso).getTime()) / 1000));
    if (seconds < 60) return `${seconds}s ago`;
    const minutes = Math.round(seconds / 60);
    if (minutes < 60) return `${minutes}m ago`;
    const hours = Math.round(minutes / 60);
    if (hours < 24) return `${hours}h ago`;
    return `${Math.round(hours / 24)}d ago`;
  }

  async function disconnect(id) {
    closingId = id;
    try {
      await api.closeSession(mockId, id);
      sessions = sessions.filter((s) => s.id !== id);
      showToast('Session disconnected', 'ok');
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      closingId = '';
    }
  }

  function toggleSendForm(id) {
    sendOpenId = sendOpenId === id ? '' : id;
    sendPayload = '';
    sendTopic = '';
    sendSourceAddr = '';
    sendDestAddr = '';
    sendAddress = '';
  }

  // extraFieldsFor builds SendToSession's protocol-specific `extra` map —
  // each protocol's own wire primitive needs a different addressing detail
  // beyond the plain payload (MQTT: which topic to publish on; SMPP: the
  // source/destination addresses on the deliver_sm; JMS: which attached
  // consumer link's address to deliver to). TCP/WS need none at all.
  function extraFieldsFor() {
    const extra = {};
    if (protocol === 'mqtt' && sendTopic.trim()) extra.topic = sendTopic.trim();
    if (protocol === 'smpp') {
      if (sendSourceAddr.trim()) extra.sourceAddr = sendSourceAddr.trim();
      if (sendDestAddr.trim()) extra.destAddr = sendDestAddr.trim();
    }
    if (protocol === 'jms' && sendAddress.trim()) extra.address = sendAddress.trim();
    return extra;
  }

  async function submitSend(id) {
    if (!sendPayload.trim()) return;
    sending = true;
    try {
      await api.sendToSession(mockId, id, sendPayload, extraFieldsFor());
      showToast('Message sent', 'ok');
      sendOpenId = '';
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      sending = false;
    }
  }
</script>

{#if loading}
  <p class="hint">Loading…</p>
{:else if sessions.length === 0}
  <p class="hint">No clients currently connected.</p>
{:else}
  <div class="sessions-list">
    {#each sessions as s (s.id)}
      <div class="session-row-wrap">
        <div class="session-row">
          <div class="session-main">
            <span class="session-addr">{s.remoteAddr}</span>
            <span class="hint">connected {relativeTime(s.connectedAt)} · active {relativeTime(s.lastActiveAt)}</span>
            <span class="chip chip-stat">in {s.messagesIn} / out {s.messagesOut}</span>
            {#if s.meta}
              {#each Object.entries(s.meta) as [k, v] (k)}
                {#if v}<span class="chip chip-stat" title={k}>{v}</span>{/if}
              {/each}
            {/if}
          </div>
          <div class="session-actions">
            {#if canSend}
              <button class="btn btn-ghost small" on:click={() => toggleSendForm(s.id)}>Send</button>
            {/if}
            <button class="btn btn-ghost small" on:click={() => disconnect(s.id)} disabled={closingId === s.id}>
              {closingId === s.id ? 'Disconnecting…' : 'Disconnect'}
            </button>
          </div>
        </div>
        {#if sendOpenId === s.id}
          <div class="session-send-form">
            <textarea aria-label="Message payload" rows="2" placeholder="Message payload" bind:value={sendPayload}></textarea>
            {#if protocol === 'mqtt'}
              <input aria-label="Topic" type="text" placeholder="Topic" bind:value={sendTopic} />
            {:else if protocol === 'smpp'}
              <div class="field-row">
                <input aria-label="Source address" type="text" placeholder="Source address" bind:value={sendSourceAddr} />
                <input aria-label="Destination address" type="text" placeholder="Destination address" bind:value={sendDestAddr} />
              </div>
            {:else if protocol === 'jms'}
              <input aria-label="Address" type="text" placeholder="Address" bind:value={sendAddress} />
            {/if}
            <div class="session-send-actions">
              <button
                class="btn btn-primary small"
                on:click={() => submitSend(s.id)}
                disabled={sending || !sendPayload.trim()}
              >{sending ? 'Sending…' : 'Send'}</button>
              <button class="btn btn-ghost small" on:click={() => toggleSendForm(s.id)}>Cancel</button>
            </div>
          </div>
        {/if}
      </div>
    {/each}
  </div>
{/if}

<style>
  .hint { color: var(--muted); font-size: 13px; }
  .sessions-list { display: flex; flex-direction: column; gap: 6px; max-height: 320px; overflow-y: auto; padding-right: 4px; }
  .session-row-wrap { background: var(--surface2, var(--hover)); border-radius: 6px; }
  .session-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 8px 10px; flex-wrap: wrap; }
  .session-main { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; flex: 1; min-width: 0; }
  .session-addr { font-weight: 600; font-family: monospace; font-size: 13px; }
  .session-actions { display: flex; gap: 6px; flex-shrink: 0; }
  .session-send-form { display: flex; flex-direction: column; gap: 6px; padding: 0 10px 10px; }
  .session-send-form textarea, .session-send-form input {
    background: var(--card); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 8px 10px; font-size: 13px; font-family: inherit; resize: vertical;
  }
  .session-send-form .field-row { display: flex; gap: 6px; }
  .session-send-form .field-row input { flex: 1; min-width: 0; }
  .session-send-actions { display: flex; gap: 6px; }
</style>
