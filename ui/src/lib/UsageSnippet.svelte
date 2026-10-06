<script>
  import { showToast } from './toast.js';
  import { copyText } from './clipboard.js';

  // A copyable "how to test this" code block — shown in each mock type's
  // expanded detail view with a ready-to-run command built from the mock's
  // own real port/path/rule.
  export let command = '';
  export let label = 'How to test this';

  let copied = false;

  async function copy() {
    try {
      await copyText(command);
      copied = true;
      showToast('Copied to clipboard', 'ok');
      setTimeout(() => (copied = false), 1500);
    } catch (e) {
      showToast('Could not copy — select the text manually', 'err');
    }
  }
</script>

<div class="usage-snippet">
  <div class="usage-snippet-head">
    <span class="usage-snippet-label">{label}</span>
    <button type="button" class="btn btn-ghost small" on:click={copy}>{copied ? 'Copied!' : 'Copy'}</button>
  </div>
  <pre class="usage-snippet-code">{command}</pre>
</div>

<style>
  .usage-snippet {
    background: var(--surface2, var(--hover)); border: 1px solid var(--border); border-radius: 8px;
    padding: 10px 12px; display: flex; flex-direction: column; gap: 6px;
  }
  .usage-snippet-head { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
  .usage-snippet-label { font-size: 11px; font-weight: 700; text-transform: uppercase; letter-spacing: .3px; color: var(--muted); }
  .usage-snippet-code {
    margin: 0; font-family: 'SFMono-Regular', Consolas, monospace; font-size: 12px; color: var(--text);
    white-space: pre-wrap; word-break: break-word; line-height: 1.5;
  }
</style>
