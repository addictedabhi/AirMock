// buildLoadTestReportHtml renders a load-test result into a single,
// self-contained HTML file: stat tiles plus three charts drawn onto
// off-screen <canvas> elements and embedded as PNG data URIs, so the
// downloaded file opens and looks right in any browser with no network
// access, no AirMock instance, and no chart library — real images, not a
// dependency on chart.js (unavailable in this build, see the chart
// components' own "TEMPORARY STOPGAP" comments) or anything else external.

const STATUS_LABELS = ['2xx', '3xx', '4xx', '5xx', 'Network error'];
const STATUS_COLORS = ['#22c55e', '#3b82f6', '#f59e0b', '#ef4444', '#991b1b'];

function pointColor(s) {
  if (s.error) return '#ef4444';
  if (s.statusCode >= 500) return '#ef4444';
  if (s.statusCode >= 400) return '#f59e0b';
  if (s.statusCode >= 300) return '#3b82f6';
  return '#22c55e';
}

function makeCanvas(w, h) {
  const c = document.createElement('canvas');
  c.width = w;
  c.height = h;
  return c;
}

function drawDonut(ctx, w, h, counts, colors, labels) {
  const total = counts.reduce((a, b) => a + b, 0);
  ctx.clearRect(0, 0, w, h);
  ctx.font = '12px sans-serif';
  if (total === 0) {
    ctx.fillStyle = '#888';
    ctx.textAlign = 'center';
    ctx.fillText('No requests', w / 2, h / 2);
    return;
  }
  const cx = w * 0.35;
  const cy = h / 2;
  const r = Math.min(w * 0.35, h / 2) - 8;
  let start = -Math.PI / 2;
  counts.forEach((c, i) => {
    if (!c) return;
    const angle = (c / total) * Math.PI * 2;
    ctx.beginPath();
    ctx.moveTo(cx, cy);
    ctx.arc(cx, cy, r, start, start + angle);
    ctx.closePath();
    ctx.fillStyle = colors[i];
    ctx.fill();
    start += angle;
  });
  ctx.globalCompositeOperation = 'destination-out';
  ctx.beginPath();
  ctx.arc(cx, cy, r * 0.55, 0, Math.PI * 2);
  ctx.fill();
  ctx.globalCompositeOperation = 'source-over';

  // legend
  let ly = cy - (labels.length * 18) / 2 + 9;
  const lx = w * 0.62;
  labels.forEach((label, i) => {
    if (!counts[i]) return;
    ctx.fillStyle = colors[i];
    ctx.fillRect(lx, ly - 8, 10, 10);
    ctx.fillStyle = '#333';
    ctx.textAlign = 'left';
    ctx.fillText(`${label}: ${counts[i]}`, lx + 16, ly + 1);
    ly += 18;
  });
}

function drawBarChart(ctx, w, h, labels, values, color) {
  ctx.clearRect(0, 0, w, h);
  ctx.font = '9px sans-serif';
  if (values.length === 0) {
    ctx.fillStyle = '#888';
    ctx.textAlign = 'center';
    ctx.fillText('No samples', w / 2, h / 2);
    return;
  }
  const max = Math.max(1, ...values);
  const padLeft = 34;
  const padBottom = 36;
  const padTop = 10;
  const padRight = 10;
  const chartW = w - padLeft - padRight;
  const chartH = h - padTop - padBottom;
  const n = values.length;
  const slot = chartW / n;
  const barW = slot * 0.7;

  // y-axis gridlines + labels
  ctx.strokeStyle = '#ddd';
  ctx.fillStyle = '#666';
  ctx.textAlign = 'right';
  for (let i = 0; i <= 4; i++) {
    const y = padTop + chartH - (i / 4) * chartH;
    ctx.beginPath();
    ctx.moveTo(padLeft, y);
    ctx.lineTo(w - padRight, y);
    ctx.stroke();
    ctx.fillText(String(Math.round((max * i) / 4)), padLeft - 4, y + 3);
  }

  values.forEach((v, i) => {
    const barH = (v / max) * chartH;
    const x = padLeft + i * slot + (slot - barW) / 2;
    const y = padTop + chartH - barH;
    ctx.fillStyle = color;
    ctx.fillRect(x, y, barW, barH);
    ctx.save();
    ctx.fillStyle = '#444';
    ctx.textAlign = 'right';
    ctx.translate(x + barW / 2, h - padBottom + 12);
    ctx.rotate(-Math.PI / 5);
    ctx.fillText(labels[i], 0, 0);
    ctx.restore();
  });
}

function drawScatter(ctx, w, h, points) {
  ctx.clearRect(0, 0, w, h);
  ctx.font = '9px sans-serif';
  if (points.length === 0) {
    ctx.fillStyle = '#888';
    ctx.textAlign = 'center';
    ctx.fillText('No samples', w / 2, h / 2);
    return;
  }
  const padLeft = 40;
  const padBottom = 24;
  const padTop = 10;
  const padRight = 10;
  const chartW = w - padLeft - padRight;
  const chartH = h - padTop - padBottom;
  const maxX = Math.max(1, ...points.map((p) => p.x));
  const maxY = Math.max(1, ...points.map((p) => p.y));

  ctx.strokeStyle = '#ddd';
  ctx.fillStyle = '#666';
  ctx.textAlign = 'right';
  for (let i = 0; i <= 4; i++) {
    const y = padTop + chartH - (i / 4) * chartH;
    ctx.beginPath();
    ctx.moveTo(padLeft, y);
    ctx.lineTo(w - padRight, y);
    ctx.stroke();
    ctx.fillText(String(Math.round((maxY * i) / 4)), padLeft - 4, y + 3);
  }

  for (const p of points) {
    const x = padLeft + (p.x / maxX) * chartW;
    const y = padTop + chartH - (p.y / maxY) * chartH;
    ctx.beginPath();
    ctx.arc(x, y, 2, 0, Math.PI * 2);
    ctx.fillStyle = pointColor(p);
    ctx.fill();
  }

  ctx.strokeStyle = '#999';
  ctx.beginPath();
  ctx.moveTo(padLeft, padTop);
  ctx.lineTo(padLeft, padTop + chartH);
  ctx.lineTo(padLeft + chartW, padTop + chartH);
  ctx.stroke();
  ctx.fillStyle = '#666';
  ctx.textAlign = 'center';
  ctx.fillText('Elapsed (ms) →', padLeft + chartW / 2, h - 4);
}

// Same bucketing as LatencyHistogram.svelte's buildBuckets — duplicated
// rather than imported since that file is a Svelte component (can't be
// imported from plain JS) and this is the only other place it's needed.
function buildLatencyBuckets(samples, bucketCount = 10) {
  if (samples.length === 0) return { labels: [], counts: [] };
  const latencies = samples.map((s) => s.latencyMs);
  let min = latencies[0];
  let max = latencies[0];
  for (const v of latencies) {
    if (v < min) min = v;
    if (v > max) max = v;
  }
  const width = Math.max(1, Math.ceil((max - min + 1) / bucketCount));
  const counts = new Array(bucketCount).fill(0);
  for (const ms of latencies) {
    const idx = Math.min(bucketCount - 1, Math.floor((ms - min) / width));
    counts[idx]++;
  }
  const labels = counts.map((_, i) => `${min + i * width}-${min + (i + 1) * width}`);
  return { labels, counts };
}

function escapeHtml(s) {
  return String(s).replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
}

// buildLoadTestReportHtml(result, meta) -> HTML string.
// meta: { method, url, generatedAt (Date) }
export function buildLoadTestReportHtml(result, meta) {
  const statuses = result.statuses;
  const counts = [statuses.count2xx, statuses.count3xx, statuses.count4xx, statuses.count5xx, statuses.countError];
  const samples = result.samples ?? [];

  const donutCanvas = makeCanvas(420, 220);
  drawDonut(donutCanvas.getContext('2d'), 420, 220, counts, STATUS_COLORS, STATUS_LABELS);

  const { labels, counts: bucketCounts } = buildLatencyBuckets(samples);
  const histCanvas = makeCanvas(420, 220);
  drawBarChart(histCanvas.getContext('2d'), 420, 220, labels.map((l) => l + 'ms'), bucketCounts, '#3b82f6');

  const scatterCanvas = makeCanvas(860, 240);
  drawScatter(
    scatterCanvas.getContext('2d'),
    860,
    240,
    samples.map((s) => ({ x: s.elapsedMs, y: s.latencyMs, statusCode: s.statusCode, error: s.error }))
  );

  const donutUrl = donutCanvas.toDataURL('image/png');
  const histUrl = histCanvas.toDataURL('image/png');
  const scatterUrl = samples.length ? scatterCanvas.toDataURL('image/png') : null;

  const stat = (val, label) => `<div class="stat"><span class="v">${escapeHtml(val)}</span><span class="l">${escapeHtml(label)}</span></div>`;

  return `<!doctype html>
<html>
<head>
<meta charset="utf-8">
<title>AirMock load test report</title>
<style>
  body { font-family: -apple-system, Segoe UI, Roboto, sans-serif; margin: 0; padding: 32px; color: #1a1a1a; background: #fafafa; }
  h1 { font-size: 20px; margin: 0 0 4px; }
  .meta { color: #666; font-size: 13px; margin-bottom: 24px; word-break: break-all; }
  .stats { display: flex; flex-wrap: wrap; gap: 20px; margin-bottom: 24px; }
  .stat { display: flex; flex-direction: column; }
  .stat .v { font-size: 20px; font-weight: 800; }
  .stat .l { font-size: 11px; color: #777; text-transform: uppercase; letter-spacing: .3px; }
  .charts { display: flex; flex-wrap: wrap; gap: 20px; margin-bottom: 24px; }
  .chart-box { background: #fff; border: 1px solid #e2e2e2; border-radius: 8px; padding: 12px; }
  .chart-box h3 { font-size: 13px; margin: 0 0 8px; color: #444; }
  img { display: block; max-width: 100%; height: auto; }
  footer { color: #999; font-size: 11px; margin-top: 16px; }
</style>
</head>
<body>
  <h1>Load test report</h1>
  <div class="meta">${escapeHtml(meta.method ?? '')} ${escapeHtml(meta.url ?? '')} — generated ${escapeHtml(meta.generatedAt.toLocaleString())}</div>
  <div class="stats">
    ${stat(result.totalRequests, 'requests')}
    ${stat(result.requestsPerSec.toFixed(1), 'req/s (TPS)')}
    ${stat(result.durationMs + 'ms', 'duration')}
    ${stat(result.minMs + 'ms', 'min')}
    ${stat(result.avgMs + 'ms', 'avg')}
    ${stat(result.p50Ms + 'ms', 'p50')}
    ${stat(result.p90Ms + 'ms', 'p90')}
    ${stat(result.p95Ms + 'ms', 'p95')}
    ${stat(result.p99Ms + 'ms', 'p99')}
    ${stat(result.maxMs + 'ms', 'max')}
  </div>
  <div class="charts">
    <div class="chart-box"><h3>Status breakdown</h3><img src="${donutUrl}" width="420" height="220" alt="Status breakdown"></div>
    <div class="chart-box"><h3>Latency distribution</h3><img src="${histUrl}" width="420" height="220" alt="Latency histogram"></div>
    ${scatterUrl ? `<div class="chart-box" style="flex-basis:100%"><h3>Latency over time</h3><img src="${scatterUrl}" width="860" height="240" alt="Latency over time"></div>` : ''}
  </div>
  <footer>Generated by AirMock${result.runId ? ` — run ${escapeHtml(result.runId)}` : ''}</footer>
</body>
</html>`;
}
