<script>
  import { api } from './api.js';
  import { showToast } from './toast.js';

  // Two-way bound: the parent's import handler reads this once the user
  // has it populated via whichever source they picked.
  export let content = '';
  export let placeholder = '';
  export let rows = 8;

  let mode = 'paste'; // 'paste' | 'file' | 'url'
  let urlInput = '';
  let urlLoading = false;
  let fileName = '';
  let dragging = false;
  let fileInputEl;

  async function loadFile(file) {
    if (!file) return;
    try {
      content = await file.text();
      fileName = file.name;
      mode = 'file';
      showToast(`Loaded ${file.name} (${file.size.toLocaleString()} bytes)`, 'ok');
    } catch (err) {
      showToast(`Could not read ${file.name}: ${err.message}`, 'err');
    }
  }

  function onFileChange(e) {
    loadFile(e.target.files?.[0]);
  }

  function onDrop(e) {
    e.preventDefault();
    dragging = false;
    loadFile(e.dataTransfer.files?.[0]);
  }

  function onDragOver(e) {
    e.preventDefault();
    dragging = true;
  }

  async function loadFromUrl() {
    const url = urlInput.trim();
    if (!url) return;
    urlLoading = true;
    try {
      const { content: fetched } = await api.fetchImportUrl(url);
      content = fetched;
      showToast(`Fetched ${fetched.length.toLocaleString()} bytes from ${url}`, 'ok');
    } catch (err) {
      showToast(err.message, 'err');
    } finally {
      urlLoading = false;
    }
  }
</script>

<div class="import-source">
  <div class="mode-tabs">
    <button type="button" class:active={mode === 'paste'} on:click={() => (mode = 'paste')}>Paste</button>
    <button type="button" class:active={mode === 'file'} on:click={() => (mode = 'file')}>Upload file</button>
    <button type="button" class:active={mode === 'url'} on:click={() => (mode = 'url')}>From URL</button>
  </div>

  {#if mode === 'file'}
    <div
      class="dropzone"
      class:dragging
      on:dragover={onDragOver}
      on:dragleave={() => (dragging = false)}
      on:drop={onDrop}
      on:click={() => fileInputEl.click()}
      on:keydown={(e) => (e.key === 'Enter' || e.key === ' ') && fileInputEl.click()}
      role="button"
      tabindex="0"
    >
      <input aria-label="Choose a file" bind:this={fileInputEl} type="file" on:change={onFileChange} hidden />
      <span class="dropzone-icon">⇪</span>
      {#if fileName}
        <span class="dropzone-text"><strong>{fileName}</strong> loaded ({content.length.toLocaleString()} bytes)</span>
        <span class="dropzone-hint">Click or drop another file to replace it</span>
      {:else}
        <span class="dropzone-text">Drag &amp; drop a file here, or click to browse</span>
      {/if}
    </div>
  {:else if mode === 'url'}
    <div class="url-row">
      <input aria-label="URL to fetch the file from" type="text" bind:value={urlInput} placeholder="https://example.com/spec.json" on:keydown={(e) => e.key === 'Enter' && loadFromUrl()} />
      <button type="button" class="btn btn-ghost" on:click={loadFromUrl} disabled={urlLoading}>
        {urlLoading ? 'Fetching…' : 'Fetch'}
      </button>
    </div>
  {/if}

  <textarea aria-label={placeholder} {rows} {placeholder} bind:value={content}></textarea>
</div>

<style>
  .import-source { display: flex; flex-direction: column; gap: 8px; }
  .mode-tabs { display: flex; gap: 4px; }
  .mode-tabs button {
    background: none; border: 1px solid var(--border); color: var(--muted);
    border-radius: 6px; padding: 5px 12px; font-size: 12px; font-weight: 600;
    cursor: pointer; transition: all .15s;
  }
  .mode-tabs button.active { background: var(--primary); border-color: var(--primary); color: #fff; }
  .mode-tabs button:not(.active):hover { border-color: var(--primary); color: var(--primary); }

  .dropzone {
    display: flex; flex-direction: column; align-items: center; justify-content: center;
    gap: 4px; padding: 22px 16px; border: 2px dashed var(--border); border-radius: 10px;
    background: var(--surface2, var(--hover)); cursor: pointer; text-align: center;
    transition: border-color .18s, background .18s, transform .12s;
  }
  .dropzone:hover { border-color: var(--primary); }
  .dropzone.dragging { border-color: var(--primary); background: var(--surface-hover, var(--hover)); transform: scale(1.01); }
  .dropzone-icon { font-size: 20px; color: var(--primary); }
  .dropzone-text { font-size: 13px; color: var(--text); }
  .dropzone-hint { font-size: 11px; color: var(--muted); }

  .url-row { display: flex; align-items: center; gap: 10px; }
  .url-row input {
    flex: 1; background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 8px 10px; font-size: 13px; font-family: inherit; outline: none;
  }
  .url-row input:focus { border-color: var(--primary); }

  textarea {
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 8px 10px; font-size: 13px; font-family: 'SFMono-Regular', Consolas, monospace;
    outline: none; resize: vertical; transition: border .2s;
  }
  textarea:focus { border-color: var(--primary); }
</style>
