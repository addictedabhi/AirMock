<script>
  // Rendered as a stacked bar of status classes with a legend with plain HTML/CSS (no charting library).

  // statuses: StatusCounts — { count2xx, count3xx, count4xx, count5xx, countError }
  export let statuses;

  const LABELS = ['2xx', '3xx', '4xx', '5xx', 'Network error'];
  const COLORS = ['#22c55e', '#3b82f6', '#f59e0b', '#ef4444', '#991b1b'];

  $: counts = [statuses.count2xx, statuses.count3xx, statuses.count4xx, statuses.count5xx, statuses.countError];
  $: total = counts.reduce((a, b) => a + b, 0);
</script>

<div class="chart-wrap">
  {#if total === 0}
    <p class="empty">No requests.</p>
  {:else}
    <div class="bar-total">
      {#each counts as c, i}
        {#if c > 0}
          <div class="segment" style="width: {(c / total) * 100}%; background: {COLORS[i]};" title="{LABELS[i]}: {c}"></div>
        {/if}
      {/each}
    </div>
    <div class="legend">
      {#each LABELS as label, i}
        <span class="item"><span class="swatch" style="background: {COLORS[i]}"></span>{label}: {counts[i]}</span>
      {/each}
    </div>
  {/if}
</div>

<style>
  .chart-wrap { position: relative; min-height: 220px; max-width: 100%; padding: 0.5rem 0; }
  .empty { color: var(--text-muted, #666); font-size: 0.9rem; }
  .bar-total { display: flex; height: 24px; border-radius: 4px; overflow: hidden; margin-bottom: 0.75rem; }
  .legend { display: flex; flex-wrap: wrap; gap: 0.75rem; font-size: 0.8rem; }
  .item { display: inline-flex; align-items: center; gap: 0.35rem; }
  .swatch { width: 10px; height: 10px; border-radius: 2px; display: inline-block; }
</style>
