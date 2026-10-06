<script>
  import { diffObjects, formatDiffValue } from './diff.js';

  // versions: Version[] (most-recent-first, from api.listMockVersions).
  // current: the mock's live Definition — each version diffs against this,
  // so expanding a row answers "what would Restore actually change" rather
  // than just "what changed at that point in editing history".
  export let versions = [];
  export let current = null;
  export let restoringVersionId = '';
  export let onRestore = (versionId) => {};

  let expandedVersionId = '';
  function toggleExpand(id) {
    expandedVersionId = expandedVersionId === id ? '' : id;
  }
</script>

{#if !versions?.length}
  <p class="sub">No past versions yet — every edit after this one will be saved here.</p>
{:else}
  <div class="version-list">
    {#each versions as v (v.id)}
      {@const changes = diffObjects(v.definition, current)}
      <div class="version-row-wrap">
        <div class="version-row">
          <button type="button" class="version-time-btn" on:click|stopPropagation={() => toggleExpand(v.id)}>
            <span class="expand-arrow" class:expanded={expandedVersionId === v.id}>▸</span>
            <span class="version-time">{new Date(v.createdAt).toLocaleString()}</span>
            <span class="chip chip-stat">{changes.length} change{changes.length === 1 ? '' : 's'}</span>
          </button>
          <button
            class="btn btn-ghost small"
            on:click|stopPropagation={() => onRestore(v.id)}
            disabled={restoringVersionId === v.id}
          >{restoringVersionId === v.id ? 'Restoring…' : 'Restore'}</button>
        </div>
        {#if expandedVersionId === v.id}
          <div class="version-changes">
            {#if changes.length === 0}
              <p class="sub">No differences from the current state.</p>
            {:else}
              {#each changes as c}
                <div class="change-row">
                  <code class="change-path">{c.path}</code>
                  <code class="change-old">{formatDiffValue(c.oldValue)}</code>
                  <span class="arrow">→</span>
                  <code class="change-new">{formatDiffValue(c.newValue)}</code>
                </div>
              {/each}
            {/if}
          </div>
        {/if}
      </div>
    {/each}
  </div>
{/if}

<style>
  .version-list { display: flex; flex-direction: column; gap: 6px; max-height: 280px; overflow-y: auto; padding-right: 4px; }
  .version-row-wrap { background: var(--surface2, var(--hover)); border-radius: 6px; }
  .version-row { display: flex; align-items: center; justify-content: space-between; gap: 10px; padding: 6px 10px; font-size: 12px; }
  .version-time-btn {
    display: flex; align-items: center; gap: 8px; flex: 1; min-width: 0;
    background: none; border: none; cursor: pointer; padding: 0; font: inherit; color: inherit; text-align: left;
  }
  .expand-arrow { display: inline-block; transition: transform .15s; color: var(--muted); flex-shrink: 0; }
  .expand-arrow.expanded { transform: rotate(90deg); }
  .version-time { color: var(--muted); }
  .version-changes {
    display: flex; flex-direction: column; gap: 4px;
    padding: 4px 10px 10px 30px; font-size: 12px;
  }
  .change-row { display: flex; align-items: baseline; gap: 8px; flex-wrap: wrap; }
  .change-path { color: var(--muted); font-weight: 600; flex-shrink: 0; }
  .change-old { color: var(--error, #dc2626); text-decoration: line-through; word-break: break-all; }
  .change-new { color: var(--success, #16a34a); word-break: break-all; }
  .arrow { color: var(--muted); flex-shrink: 0; }
</style>
