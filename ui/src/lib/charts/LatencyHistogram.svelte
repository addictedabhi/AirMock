<script>
  // Rendered as a plain-CSS bar histogram of latency buckets with plain HTML/CSS (no charting library).

  // samples: LoadTestSample[] — only latencyMs is used here
  export let samples = [];
  const BUCKET_COUNT = 10;

  function buildBuckets() {
    if (samples.length === 0) return { labels: [], counts: [] };
    const latencies = samples.map((s) => s.latencyMs);
    // Explicit loop rather than Math.min/max(...latencies) — spreading a
    // large array as function arguments throws "RangeError: Maximum call
    // stack size exceeded" in V8 past roughly 100k elements, which a long
    // duration-mode load test can easily produce.
    let min = latencies[0];
    let max = latencies[0];
    for (const v of latencies) {
      if (v < min) min = v;
      if (v > max) max = v;
    }
    const width = Math.max(1, Math.ceil((max - min + 1) / BUCKET_COUNT));
    const counts = new Array(BUCKET_COUNT).fill(0);
    for (const ms of latencies) {
      const idx = Math.min(BUCKET_COUNT - 1, Math.floor((ms - min) / width));
      counts[idx]++;
    }
    const labels = counts.map((_, i) => `${min + i * width}-${min + (i + 1) * width}`);
    return { labels, counts };
  }

  $: buckets = buildBuckets();
  $: maxCount = buckets.counts.reduce((m, c) => Math.max(m, c), 1);
</script>

<div class="chart-wrap">
  {#if buckets.labels.length === 0}
    <p class="empty">No samples.</p>
  {:else}
    <div class="bars">
      {#each buckets.labels as label, i}
        <div class="row">
          <span class="label">{label}ms</span>
          <div class="track"><div class="fill" style="width: {Math.max(2, (buckets.counts[i] / maxCount) * 100)}%"></div></div>
          <span class="count">{buckets.counts[i]}</span>
        </div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .chart-wrap { position: relative; min-height: 220px; max-width: 100%; padding: 0.5rem 0; }
  .empty { color: var(--text-muted, #666); font-size: 0.9rem; }
  .bars { display: flex; flex-direction: column; gap: 0.3rem; }
  .row { display: flex; align-items: center; gap: 0.5rem; font-size: 0.8rem; }
  .label { width: 110px; flex-shrink: 0; text-align: right; color: var(--text-muted, #666); }
  .track { flex: 1; background: var(--surface-muted, #eee); border-radius: 2px; overflow: hidden; }
  .fill { height: 14px; background: #3b82f6; border-radius: 2px; }
  .count { width: 2.5rem; flex-shrink: 0; }
</style>
