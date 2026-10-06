<script>
  import { api } from './api.js';
  import { showToast } from './toast.js';
  import InfoTooltip from './InfoTooltip.svelte';

  // ownerPath is `/api/mocks/${id}` or `/api/scheduled-events/${id}` — both
  // expose the identical csv-source routes (see api.js's setCsvSource/
  // getCsvSource/deleteCsvSource). Re-fetches its own metadata whenever
  // ownerPath changes (e.g. switching which mock's edit form is open).
  export let ownerPath;

  let meta = null;
  let mode = 'round_robin';
  let loading = false;
  let fileInput;

  async function load() {
    if (!ownerPath) return;
    meta = await api.getCsvSource(ownerPath).catch(() => null);
    if (meta) mode = meta.mode;
  }

  // Runs once on creation and again whenever ownerPath changes (the reactive
  // statement covers both; a separate onMount(load) made every Edit click send
  // the request twice).
  $: ownerPath, load();

  async function handleFileChange(e) {
    const file = e.target.files?.[0];
    if (!file) return;
    loading = true;
    try {
      meta = await api.setCsvSource(ownerPath, mode, file);
      showToast(`CSV attached — ${meta.rowCount} row${meta.rowCount === 1 ? '' : 's'}`, 'ok');
    } catch (err) {
      showToast(err.message, 'err');
    } finally {
      loading = false;
      if (fileInput) fileInput.value = '';
    }
  }

  async function removeSource() {
    if (!confirm('Remove the attached CSV? {{csv "..."}} calls in this template will then error until a new one is attached.')) return;
    loading = true;
    try {
      await api.deleteCsvSource(ownerPath);
      meta = null;
      showToast('CSV removed', 'ok');
    } catch (err) {
      showToast(err.message, 'err');
    } finally {
      loading = false;
    }
  }
</script>

<div class="csv-source-field">
  <label class="csv-source-label">
    CSV data source (optional)
    <InfoTooltip
      text={'Attach a CSV to back the {{csv "column"}} template function — each render pulls one row (round-robin cycles through rows in order; random picks one each time) and {{csv "column"}} returns that row\'s value for the named column. Multiple {{csv "..."}} calls in the same response use the same row.'}
    />
  </label>
  {#if meta}
    <div class="csv-source-status">
      <span class="chip chip-stat">{meta.rowCount} row{meta.rowCount === 1 ? '' : 's'}</span>
      <span class="chip chip-stat">{meta.mode === 'random' ? 'random' : 'round-robin'}</span>
      <span class="csv-columns">columns: {meta.columns.join(', ')}</span>
      <button type="button" class="btn btn-ghost small remove" disabled={loading} on:click={removeSource}>Remove</button>
    </div>
  {/if}
  <div class="csv-source-controls">
    <select aria-label="How rows are chosen" bind:value={mode} disabled={loading}>
      <option value="round_robin">Round-robin</option>
      <option value="random">Random</option>
    </select>
    <input aria-label="Choose a file" type="file" accept=".csv,text/csv" disabled={loading} bind:this={fileInput} on:change={handleFileChange} />
  </div>
</div>

<style>
  .csv-source-field { display: flex; flex-direction: column; gap: 8px; margin: 10px 0; }
  .csv-source-label { display: flex; align-items: center; gap: 6px; font-size: 12px; font-weight: 600; color: var(--muted); }
  .csv-source-status { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; font-size: 12px; }
  .csv-columns { color: var(--muted); overflow-wrap: anywhere; }
  .csv-source-controls { display: flex; align-items: center; gap: 8px; flex-wrap: wrap; }
  .csv-source-controls select {
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 6px 8px; font-size: 12px; font-weight: 500; font-family: inherit; outline: none;
  }
  .csv-source-controls input[type='file'] { font-size: 12px; max-width: 100%; }
</style>
