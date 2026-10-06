<script>
  // Rendered as a latency summary plus a strip of the first 200 samples, coloured by status with plain HTML/CSS (no charting library).

  // samples: LoadTestSample[] — { index, elapsedMs, latencyMs, statusCode, error }
  export let samples = [];

  function pointColor(s) {
    if (s.error) return '#ef4444';
    if (s.statusCode >= 500) return '#ef4444';
    if (s.statusCode >= 400) return '#f59e0b';
    if (s.statusCode >= 300) return '#3b82f6';
    return '#22c55e';
  }

  $: stats = (() => {
    if (samples.length === 0) return null;
    let min = samples[0].latencyMs;
    let max = samples[0].latencyMs;
    let sum = 0;
    for (const s of samples) {
      if (s.latencyMs < min) min = s.latencyMs;
      if (s.latencyMs > max) max = s.latencyMs;
      sum += s.latencyMs;
    }
    return { count: samples.length, min, max, avg: Math.round(sum / samples.length) };
  })();

  // A lightweight sparkline-style strip of the first ~200 samples so this
  // isn't just a number — colored by status, height by relative latency.
  $: strip = samples.slice(0, 200);
  $: stripMax = strip.reduce((m, s) => Math.max(m, s.latencyMs), 1);
</script>

<div class="chart-wrap">
  {#if !stats}
    <p class="empty">No samples.</p>
  {:else}
    <div class="stats">
      <span><strong>{stats.count}</strong> samples</span>
      <span>min <strong>{stats.min}ms</strong></span>
      <span>avg <strong>{stats.avg}ms</strong></span>
      <span>max <strong>{stats.max}ms</strong></span>
    </div>
    <div class="strip">
      {#each strip as s}
        <div
          class="bar"
          style="height: {Math.max(2, Math.round((s.latencyMs / stripMax) * 100))}%; background: {pointColor(s)};"
          title="elapsed {s.elapsedMs}ms — {s.latencyMs}ms"
        ></div>
      {/each}
    </div>
  {/if}
</div>

<style>
  .chart-wrap { position: relative; min-height: 220px; max-width: 100%; padding: 0.5rem 0; }
  .empty { color: var(--text-muted, #666); font-size: 0.9rem; }
  .stats { display: flex; gap: 1rem; font-size: 0.85rem; margin-bottom: 0.75rem; }
  .strip { display: flex; align-items: flex-end; gap: 1px; height: 120px; }
  .bar { flex: 1 1 0; min-width: 1px; border-radius: 1px 1px 0 0; }
</style>
