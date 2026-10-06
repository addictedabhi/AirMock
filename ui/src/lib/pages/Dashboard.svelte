<script>
  import { onMount } from 'svelte';
  import { api } from '../api.js';
  import { showToast } from '../toast.js';
  import { navigate, navigateToItem } from '../router.js';

  // Maps a mock's protocolType to the router page id that actually renders
  // it — REST/SOAP/GraphQL all share the plain "mocks" page, every other
  // protocol has its own dedicated page (mirrors the OTHER_PAGES set
  // Mocks.svelte's own load() filters against).
  const MOCK_PAGE_BY_PROTOCOL = {
    rest: 'mocks', soap: 'mocks', graphql: 'mocks',
    tcp: 'tcpmocks', smtp: 'smtpmocks', ws: 'wsmocks', mqtt: 'mqttmocks', ftp: 'ftpmocks',
    kafka: 'kafkamocks', smpp: 'smppmocks', diameter: 'diametermocks', jms: 'jmsmocks',
  };
  function goToMock(m) {
    navigateToItem(MOCK_PAGE_BY_PROTOCOL[m.protocolType] ?? 'mocks', m.id);
  }
  function goToCert(c) {
    navigateToItem('certificates', c.id);
  }

  // Only these 8 protocols track live connections at all (see each
  // engine's own ListSessions) — REST/SOAP/GraphQL/FTP have no notion of a
  // "connected session," so there's no point asking them for one.
  const SESSION_CAPABLE_PROTOCOLS = new Set(['tcp', 'ws', 'mqtt', 'smtp', 'kafka', 'smpp', 'diameter', 'jms']);

  // Which stat card's click-to-see-more breakdown is open, if any — only
  // one at a time, and toggled shut by clicking its own trigger again.
  let mockBreakdownOpen = ''; // '' | 'enabled' | 'disabled'
  function toggleMockBreakdown(kind) {
    mockBreakdownOpen = mockBreakdownOpen === kind ? '' : kind;
  }
  let certBreakdownOpen = ''; // '' | 'expired' | 'expiring'
  function toggleCertBreakdown(kind) {
    certBreakdownOpen = certBreakdownOpen === kind ? '' : kind;
  }
  let sessionsBreakdownOpen = false;
  function toggleSessionsBreakdown() {
    sessionsBreakdownOpen = !sessionsBreakdownOpen;
  }

  let mocks = [];
  // mockId -> currently-connected session count, populated once after
  // `mocks` loads (see onMount) — a point-in-time snapshot like every other
  // stat on this page, not live-polled the way SessionsPanel is on a mock's
  // own detail page.
  let sessionCountsByMock = {};
  let hits = [];
  let allTimeHits = []; // separate from `hits` — see the "Never hit" insight below for why
  let certificates = [];
  let collections = [];
  let loading = true;

  // How many trailing minute-buckets the sparkline shows — enough to read
  // a shape without the bars getting too thin to see on a narrow screen.
  const SPARKLINE_BUCKETS = 30;
  const HIT_SAMPLE_LIMIT = 500;

  // Every stat on this page derives from `hits` — scoping the fetch itself
  // to "since local midnight" (rather than just "most recent N ever") makes
  // the whole dashboard read as "today at a glance," which is what "Hits
  // sampled" is actually meant to answer, instead of an arbitrary trailing
  // window that could silently span into yesterday on a quiet instance.
  function startOfToday() {
    const d = new Date();
    d.setHours(0, 0, 0, 0);
    return d.toISOString();
  }

  onMount(async () => {
    try {
      [mocks, hits, allTimeHits, certificates, collections] = await Promise.all([
        api.listMocks(),
        api.listHitLog({ since: startOfToday(), limit: HIT_SAMPLE_LIMIT }),
        // Deliberately NOT scoped to today — "never hit" is a claim about
        // all time, and reusing the today-scoped `hits` here previously
        // meant a mock hammered all day yesterday showed up mislabeled
        // "never hit" the moment the clock passed midnight.
        api.listHitLog({ limit: HIT_SAMPLE_LIMIT }),
        api.listCertificates().catch(() => []),
        api.listCollections().catch(() => []),
      ]);
      mocks = mocks ?? [];
      hits = hits ?? [];
      allTimeHits = allTimeHits ?? [];
      certificates = certificates ?? [];
      collections = collections ?? [];
    } catch (e) {
      showToast(e.message, 'err');
    } finally {
      loading = false;
    }
    loadSessionCounts();
  });

  // Fired-and-forgotten after the main load — a slow/failed session count
  // for one mock (an engine hiccup, a stale mock) shouldn't hold up or
  // fail the rest of the dashboard, so each mock's own listSessions call is
  // caught individually rather than via one shared try/catch.
  async function loadSessionCounts() {
    const sessionMocks = mocks.filter((m) => SESSION_CAPABLE_PROTOCOLS.has(m.protocolType));
    const counts = await Promise.all(
      sessionMocks.map((m) => api.listSessions(m.id).then((s) => [m.id, (s ?? []).length]).catch(() => [m.id, 0]))
    );
    sessionCountsByMock = Object.fromEntries(counts);
  }

  $: restMocks = mocks.filter((m) => m.protocolType !== 'tcp');
  $: tcpMocks = mocks.filter((m) => m.protocolType === 'tcp');
  $: enabledMocksList = mocks.filter((m) => m.enabled);
  $: disabledMocksList = mocks.filter((m) => !m.enabled);
  $: enabledCount = enabledMocksList.length;
  $: disabledCount = disabledMocksList.length;
  $: mocksWithSessions = mocks.filter((m) => (sessionCountsByMock[m.id] ?? 0) > 0);
  $: totalSessions = Object.values(sessionCountsByMock).reduce((sum, n) => sum + n, 0);
  $: errorCount = hits.filter((h) => h.responseStatus >= 400).length;
  $: errorRate = hits.length ? Math.round((errorCount / hits.length) * 100) : 0;
  $: latencies = hits.map((h) => h.latencyMs || 0).sort((a, b) => a - b);
  $: avgLatency = latencies.length ? Math.round(latencies.reduce((s, v) => s + v, 0) / latencies.length) : 0;
  $: p95Latency = latencies.length ? latencies[Math.min(latencies.length - 1, Math.floor(latencies.length * 0.95))] : 0;

  $: sampleCapped = hits.length >= HIT_SAMPLE_LIMIT;

  $: protocolCounts = countBy(hits, (h) => h.protocolType || 'unknown');
  $: directionCounts = countBy(hits, (h) => h.direction || 'inbound');

  $: expiringSoonCertList = certificates.filter((c) => daysUntil(c.notAfter) <= 30 && daysUntil(c.notAfter) >= 0);
  $: expiredCertList = certificates.filter((c) => daysUntil(c.notAfter) < 0);
  $: expiringSoon = expiringSoonCertList.length;
  $: expiredCerts = expiredCertList.length;

  $: topMocks = rankMocksByHits(mocks, hits).slice(0, 5);

  // Collection hits: outbound calls sent from the API client (Collections
  // page — a single "Send", or a Collection Runner pass) tagged by
  // Collections.svelte with which collection/request triggered them. A
  // call from an unsaved/draft tab (no collection at all) still counts
  // under its own "Unsaved requests" bucket rather than being dropped, so
  // the total here always reconciles with the "API client calls" count in
  // the by-direction breakdown below.
  $: collectionHits = hits.filter((h) => h.direction === 'outbound-client-call');
  $: topCollections = rankCollectionHits(collectionHits).slice(0, 5);

  function rankCollectionHits(hitList) {
    const counts = {};
    const names = {};
    for (const h of hitList) {
      const key = h.collectionId || '';
      counts[key] = (counts[key] || 0) + 1;
      names[key] = h.collectionName || 'Unsaved requests';
    }
    return Object.entries(counts)
      .map(([id, hitCount]) => ({ id, name: names[id], hitCount }))
      .sort((a, b) => b.hitCount - a.hitCount);
  }

  // Insights — proactive callouts from data already loaded above rather
  // than a new endpoint. "Never hit" and "high error rate" deliberately use
  // DIFFERENT samples: never-hit is a claim about all time (backed by
  // allTimeHits, uncapped by date), while error rate is about CURRENT
  // problems worth acting on today (backed by the today-scoped `hits` — a
  // mock that errored yesterday but is fine now shouldn't keep nagging).
  $: allTimeMockHitCounts = countBy(allTimeHits, (h) => h.mockId || '');
  $: mockHitCounts = countBy(hits, (h) => h.mockId || '');
  $: mockErrorCounts = (() => {
      const out = {};
      for (const h of hits) {
        if (h.mockId && h.responseStatus >= 400) out[h.mockId] = (out[h.mockId] || 0) + 1;
      }
      return out;
    })();
  // "Unused" only applies to protocols the hit log can actually observe —
  // TCP/SMTP/MQTT/FTP mocks log hits under their own protocolType same as
  // REST, so no exclusion needed there; every enabled mock is fair game.
  // Capped and sorted so one noisy mock list can't push the panel very tall.
  $: unusedMocks = mocks.filter((m) => m.enabled && !allTimeMockHitCounts[m.id]).slice(0, 8);
  // Needs a handful of hits before "error rate" means anything — one failed
  // request out of one total would otherwise flag every fresh mock at 100%.
  $: errorProneMocks = mocks
    .map((m) => ({ ...m, hitCount: mockHitCounts[m.id] || 0, errCount: mockErrorCounts[m.id] || 0 }))
    .filter((m) => m.hitCount >= 5 && m.errCount / m.hitCount >= 0.5)
    .sort((a, b) => b.errCount / b.hitCount - a.errCount / a.hitCount)
    .slice(0, 8);
  $: expiringCertList = certificates
    .map((c) => ({ ...c, daysLeft: daysUntil(c.notAfter) }))
    .filter((c) => c.daysLeft <= 14)
    .sort((a, b) => a.daysLeft - b.daysLeft)
    .slice(0, 8);
  $: hasInsights = unusedMocks.length > 0 || errorProneMocks.length > 0 || expiringCertList.length > 0;

  $: recentHits = [...hits]
    .sort((a, b) => new Date(b.createdAt) - new Date(a.createdAt))
    .slice(0, 8);

  $: sparkline = bucketHitsByMinute(hits, SPARKLINE_BUCKETS);
  $: sparkMax = Math.max(1, ...sparkline.map((b) => b.count));

  $: latencySparkline = bucketLatencyByMinute(hits, SPARKLINE_BUCKETS);
  $: latencySparkMax = Math.max(1, ...latencySparkline.map((b) => b.avgMs));

  function countBy(list, keyFn) {
    const out = {};
    for (const item of list) {
      const k = keyFn(item);
      out[k] = (out[k] || 0) + 1;
    }
    return out;
  }

  function daysUntil(iso) {
    if (!iso) return Infinity;
    return Math.floor((new Date(iso) - Date.now()) / 86400000);
  }

  function rankMocksByHits(mockList, hitList) {
    const counts = countBy(hitList, (h) => h.mockId || '');
    return mockList
      .map((m) => ({ ...m, hitCount: counts[m.id] || 0 }))
      .filter((m) => m.hitCount > 0)
      .sort((a, b) => b.hitCount - a.hitCount);
  }

  function bucketHitsByMinute(hitList, bucketCount) {
    const now = Date.now();
    const buckets = Array.from({ length: bucketCount }, (_, i) => ({
      minutesAgo: bucketCount - 1 - i,
      count: 0,
    }));
    for (const h of hitList) {
      const ageMin = Math.floor((now - new Date(h.createdAt)) / 60000);
      const idx = bucketCount - 1 - ageMin;
      if (idx >= 0 && idx < bucketCount) buckets[idx].count++;
    }
    return buckets;
  }

  // Average latency per minute bucket — a snapshot avg/p95 stat (see the
  // stat card above) hides whether latency has been climbing, spiking, or
  // steady; this shows the shape over time instead. A bucket with no hits
  // has avgMs=0 but count=0 too, so the render below can tell "genuinely
  // instant" apart from "no data this minute" rather than drawing a
  // misleading full-height or zero-height bar for an empty bucket.
  function bucketLatencyByMinute(hitList, bucketCount) {
    const now = Date.now();
    const sums = Array.from({ length: bucketCount }, () => ({ total: 0, count: 0 }));
    for (const h of hitList) {
      const ageMin = Math.floor((now - new Date(h.createdAt)) / 60000);
      const idx = bucketCount - 1 - ageMin;
      if (idx >= 0 && idx < bucketCount) {
        sums[idx].total += h.latencyMs || 0;
        sums[idx].count++;
      }
    }
    return sums.map((s, i) => ({
      minutesAgo: bucketCount - 1 - i,
      avgMs: s.count ? Math.round(s.total / s.count) : 0,
      count: s.count,
    }));
  }

  function relativeTime(iso) {
    const diffMs = Date.now() - new Date(iso);
    const mins = Math.floor(diffMs / 60000);
    if (mins < 1) return 'just now';
    if (mins < 60) return `${mins}m ago`;
    const hrs = Math.floor(mins / 60);
    if (hrs < 24) return `${hrs}h ago`;
    return `${Math.floor(hrs / 24)}d ago`;
  }

  function methodClass(method) {
    return method === 'GET' ? 'badge-info' : method === 'DELETE' ? 'badge-err' : method === 'POST' ? 'badge-ok' : 'badge-warn';
  }

  function statusClass(status) {
    if (!status) return 'badge-warn';
    return status >= 500 ? 'badge-err' : status >= 400 ? 'badge-warn' : 'badge-ok';
  }

  const PROTOCOL_LABELS = { rest: 'REST', soap: 'SOAP', graphql: 'GraphQL', tcp: 'TCP', unknown: 'Other' };
  const DIRECTION_LABELS = {
    inbound: 'Mock hits',
    'proxy-capture': 'Proxy captures',
    'outbound-client-call': 'API client calls',
    callback: 'Async callbacks',
  };
</script>

<h1>Dashboard</h1>
<p class="sub">Overview of mocks, certificates, and recent traffic across this instance.</p>

{#if loading}
  <p class="sub"><span class="loader-spin"></span>&nbsp; Loading…</p>
{:else}
  <div class="grid">
    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
    <div class="card clickable" on:click={() => navigate('mocks')} title="Go to Mocks">
      <div class="stat-label"><button type="button" class="stat-label-btn" on:click|stopPropagation={() => navigate('mocks')}>Mocks configured</button></div>
      <div class="stat-val">{mocks.length}</div>
      <div class="stat-row">
        <span class="dot-alive"></span>&nbsp;
        <button class="stat-link" on:click|stopPropagation={() => toggleMockBreakdown('enabled')}>{enabledCount} enabled</button>
        &nbsp;·&nbsp;
        <button class="stat-link" on:click|stopPropagation={() => toggleMockBreakdown('disabled')}>{disabledCount} disabled</button>
      </div>
      {#if mockBreakdownOpen}
        <div class="breakdown-popover" role="none" on:click|stopPropagation>
          {#each (mockBreakdownOpen === 'enabled' ? enabledMocksList : disabledMocksList) as m (m.id)}
            <button class="breakdown-item" on:click={() => goToMock(m)}>
              <span class="chip {m.protocolType === 'tcp' ? 'chip-tls' : 'badge-info'}">{PROTOCOL_LABELS[m.protocolType] ?? m.protocolType}</span>
              <span class="breakdown-item-name">{m.name || m.pathPattern}</span>
            </button>
          {:else}
            <p class="sub empty">None.</p>
          {/each}
        </div>
      {/if}
    </div>
    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
    <div
      class="card clickable"
      on:click={toggleSessionsBreakdown}
      title="Show currently connected clients"
    >
      <div class="stat-label"><button type="button" class="stat-label-btn" aria-expanded={!!sessionsBreakdownOpen} on:click|stopPropagation={toggleSessionsBreakdown}>Connected sessions</button></div>
      <div class="stat-val">{totalSessions}</div>
      <div class="stat-row">across {mocksWithSessions.length} mock{mocksWithSessions.length === 1 ? '' : 's'}</div>
      {#if sessionsBreakdownOpen}
        <div class="breakdown-popover" role="none" on:click|stopPropagation>
          {#each mocksWithSessions as m (m.id)}
            <button class="breakdown-item" on:click={() => goToMock(m)}>
              <span class="chip {m.protocolType === 'tcp' ? 'chip-tls' : 'badge-info'}">{PROTOCOL_LABELS[m.protocolType] ?? m.protocolType}</span>
              <span class="breakdown-item-name">{m.name || m.pathPattern}</span>
              <span class="chip chip-stat">{sessionCountsByMock[m.id]}</span>
            </button>
          {:else}
            <p class="sub empty">No clients currently connected.</p>
          {/each}
        </div>
      {/if}
    </div>
    <div class="card clickable" role="button" tabindex="0" on:click={() => navigate('logs')} on:keydown={(e) => e.key === 'Enter' && navigate('logs')} title="Go to Log History">
      <div class="stat-label">Hits today</div>
      <div class="stat-val">{hits.length}{sampleCapped ? '+' : ''}</div>
      <div class="stat-row">since midnight{sampleCapped ? `, capped at ${HIT_SAMPLE_LIMIT} (more exist)` : ''}</div>
    </div>
    <div class="card clickable" role="button" tabindex="0" on:click={() => navigate('logs')} on:keydown={(e) => e.key === 'Enter' && navigate('logs')} title="Go to Log History">
      <div class="stat-label">Error rate</div>
      <div class="stat-val">{errorRate}%</div>
      <div class="stat-row">{errorCount} of {hits.length} responses &ge; 400</div>
    </div>
    <div class="card clickable" role="button" tabindex="0" on:click={() => navigate('logs')} on:keydown={(e) => e.key === 'Enter' && navigate('logs')} title="Go to Log History">
      <div class="stat-label">Latency (avg / p95)</div>
      <div class="stat-val">{avgLatency}<span class="stat-unit">ms</span></div>
      <div class="stat-row">p95 {p95Latency}ms across sampled hits</div>
    </div>
    <div class="card clickable" role="button" tabindex="0" on:click={() => navigate('collections')} on:keydown={(e) => e.key === 'Enter' && navigate('collections')} title="Go to Collections">
      <div class="stat-label">Collections</div>
      <div class="stat-val">{collections.length}</div>
      <div class="stat-row">{collections.reduce((n, c) => n + (c.items?.length ?? 0), 0)} top-level items · {collectionHits.length} call{collectionHits.length === 1 ? '' : 's'} today</div>
    </div>
    <!-- svelte-ignore a11y_click_events_have_key_events a11y_no_static_element_interactions -->
    <div class="card clickable" on:click={() => navigate('certificates')} title="Go to Certificates">
      <div class="stat-label"><button type="button" class="stat-label-btn" on:click|stopPropagation={() => navigate('certificates')}>Certificates</button></div>
      <div class="stat-val">{certificates.length}</div>
      <div class="stat-row">
        {#if expiredCerts > 0}<button class="chip badge-err" on:click|stopPropagation={() => toggleCertBreakdown('expired')}>{expiredCerts} expired</button>{/if}
        {#if expiringSoon > 0}<button class="chip badge-warn" on:click|stopPropagation={() => toggleCertBreakdown('expiring')}>{expiringSoon} expiring &lt;30d</button>{/if}
        {#if expiredCerts === 0 && expiringSoon === 0}all healthy{/if}
      </div>
      {#if certBreakdownOpen}
        <div class="breakdown-popover" role="none" on:click|stopPropagation>
          {#each (certBreakdownOpen === 'expired' ? expiredCertList : expiringSoonCertList) as c (c.id)}
            <button class="breakdown-item" on:click={() => goToCert(c)}>
              <span class="breakdown-item-name">{c.commonName || c.name}</span>
            </button>
          {:else}
            <p class="sub empty">None.</p>
          {/each}
        </div>
      {/if}
    </div>
  </div>

  <div class="panels">
    <div class="card panel">
      <h3>Traffic over last {SPARKLINE_BUCKETS} min</h3>
      {#if hits.length === 0}
        <p class="sub empty">No hits recorded yet — traffic will appear here once a mock or the API client is used.</p>
      {:else}
        <div class="sparkline" role="img" aria-label="Hit count per minute over the last {SPARKLINE_BUCKETS} minutes">
          {#each sparkline as b}
            <div class="spark-bar" style="height: {Math.max(3, Math.round((b.count / sparkMax) * 100))}%" title="{b.minutesAgo}m ago: {b.count} hit{b.count === 1 ? '' : 's'}"></div>
          {/each}
        </div>
      {/if}
    </div>

    <div class="card panel">
      <h3>Avg latency over last {SPARKLINE_BUCKETS} min</h3>
      {#if hits.length === 0}
        <p class="sub empty">No hits recorded yet — traffic will appear here once a mock or the API client is used.</p>
      {:else}
        <div class="sparkline" role="img" aria-label="Average latency per minute over the last {SPARKLINE_BUCKETS} minutes">
          {#each latencySparkline as b}
            {#if b.count > 0}
              <div
                class="spark-bar latency-bar"
                style="height: {Math.max(3, Math.round((b.avgMs / latencySparkMax) * 100))}%"
                title="{b.minutesAgo}m ago: {b.avgMs}ms avg ({b.count} hit{b.count === 1 ? '' : 's'})"
              ></div>
            {:else}
              <div class="spark-bar spark-bar-empty" title="{b.minutesAgo}m ago: no hits"></div>
            {/if}
          {/each}
        </div>
      {/if}
    </div>

    <div class="card panel">
      <h3>By protocol <span class="heading-scope">today</span></h3>
      {#if hits.length === 0}
        <p class="sub empty">No traffic sampled yet.</p>
      {:else}
        <div class="breakdown">
          {#each Object.entries(protocolCounts).sort((a, b) => b[1] - a[1]) as [proto, count]}
            <div class="breakdown-row">
              <span class="chip {proto === 'tcp' ? 'chip-tls' : 'badge-info'}">{PROTOCOL_LABELS[proto] ?? proto}</span>
              <div class="bar-track"><div class="bar-fill" style="width: {Math.round((count / hits.length) * 100)}%"></div></div>
              <span class="bar-count">{count}</span>
            </div>
          {/each}
        </div>
        <h3 class="sub-heading">By direction</h3>
        <div class="breakdown">
          {#each Object.entries(directionCounts).sort((a, b) => b[1] - a[1]) as [dir, count]}
            <div class="breakdown-row">
              <span class="chip chip-stat">{DIRECTION_LABELS[dir] ?? dir}</span>
              <div class="bar-track"><div class="bar-fill" style="width: {Math.round((count / hits.length) * 100)}%"></div></div>
              <span class="bar-count">{count}</span>
            </div>
          {/each}
        </div>
      {/if}
    </div>

    <div class="card panel">
      <h3>Top mocks by traffic <span class="heading-scope">today</span></h3>
      {#if topMocks.length === 0}
        <p class="sub empty">No mock has been hit yet.</p>
      {:else}
        <div class="top-mocks">
          {#each topMocks as m}
            <button class="top-mock-row clickable-row" on:click={() => goToMock(m)}>
              <span class="badge {methodClass(m.method)}">{m.method ?? 'TCP'}</span>
              <span class="top-mock-name" title={m.pathPattern ?? m.name}>{m.name || m.pathPattern}</span>
              <span class="top-mock-count">{m.hitCount}</span>
            </button>
          {/each}
        </div>
      {/if}
    </div>

    <div class="card panel">
      <h3>Top collections by traffic <span class="heading-scope">today</span></h3>
      {#if topCollections.length === 0}
        <p class="sub empty">No API-client call has been sent yet today.</p>
      {:else}
        <div class="top-mocks">
          {#each topCollections as c (c.id)}
            <button
              class="top-mock-row clickable-row"
              disabled={!c.id}
              title={c.id ? '' : "Sent from an unsaved/draft tab — nothing to jump to"}
              on:click={() => c.id && navigateToItem('collections', c.id)}
            >
              <span class="chip chip-stat">API</span>
              <span class="top-mock-name" title={c.name}>{c.name}</span>
              <span class="top-mock-count">{c.hitCount}</span>
            </button>
          {/each}
        </div>
      {/if}
    </div>

    <div class="card panel panel-wide">
      <h3>Recent activity</h3>
      {#if recentHits.length === 0}
        <p class="sub empty">Nothing recorded yet.</p>
      {:else}
        <div class="activity-list">
          {#each recentHits as h (h.id)}
            {#if h.mockId}
              <div
                class="activity-row row-enter clickable-row"
                role="button"
                tabindex="0"
                on:click={() => goToMock({ id: h.mockId, protocolType: h.protocolType })}
                on:keydown={(e) => (e.key === 'Enter' || e.key === ' ') && goToMock({ id: h.mockId, protocolType: h.protocolType })}
              >
                <span class="badge {methodClass(h.method)}">{h.method || '—'}</span>
                <span class="activity-path" title={h.path}>{h.path || h.targetUrl || '(no path)'}</span>
                <span class="chip {h.protocolType === 'tcp' ? 'chip-tls' : 'badge-info'}">{PROTOCOL_LABELS[h.protocolType] ?? h.protocolType}</span>
                <span class="badge {statusClass(h.responseStatus)}">{h.responseStatus || '—'}</span>
                <span class="activity-latency">{h.latencyMs ?? 0}ms</span>
                <span class="activity-time">{relativeTime(h.createdAt)}</span>
              </div>
            {:else}
              <div class="activity-row row-enter">
                <span class="badge {methodClass(h.method)}">{h.method || '—'}</span>
                <span class="activity-path" title={h.path}>{h.path || h.targetUrl || '(no path)'}</span>
                <span class="chip {h.protocolType === 'tcp' ? 'chip-tls' : 'badge-info'}">{PROTOCOL_LABELS[h.protocolType] ?? h.protocolType}</span>
                <span class="badge {statusClass(h.responseStatus)}">{h.responseStatus || '—'}</span>
                <span class="activity-latency">{h.latencyMs ?? 0}ms</span>
                <span class="activity-time">{relativeTime(h.createdAt)}</span>
              </div>
            {/if}
          {/each}
        </div>
      {/if}
    </div>

    <div class="card panel panel-wide">
      <h3>Insights</h3>
      {#if !hasInsights}
        <p class="sub empty">Nothing needs your attention — no unused mocks, no error-prone mocks, and no certificates expiring soon.</p>
      {:else}
        <div class="insights-grid">
          {#if unusedMocks.length > 0}
            <div class="insight-group">
              <h4>Never hit <span class="sub">— enabled, but no hits in the most recent {HIT_SAMPLE_LIMIT} ever recorded</span></h4>
              <div class="insight-list">
                {#each unusedMocks as m (m.id)}
                  <button class="insight-row clickable-row" on:click={() => goToMock(m)}>
                    <span class="chip {m.protocolType === 'tcp' ? 'chip-tls' : 'badge-info'}">{PROTOCOL_LABELS[m.protocolType] ?? m.protocolType}</span>
                    <span class="insight-name" title={m.pathPattern ?? m.name}>{m.name || m.pathPattern}</span>
                  </button>
                {/each}
              </div>
            </div>
          {/if}
          {#if errorProneMocks.length > 0}
            <div class="insight-group">
              <h4>High error rate today <span class="sub">— &ge;5 hits today, &ge;50% responses &ge;400</span></h4>
              <div class="insight-list">
                {#each errorProneMocks as m (m.id)}
                  <button class="insight-row clickable-row" on:click={() => goToMock(m)}>
                    <span class="badge badge-err">{Math.round((m.errCount / m.hitCount) * 100)}%</span>
                    <span class="insight-name" title={m.pathPattern ?? m.name}>{m.name || m.pathPattern}</span>
                    <span class="insight-meta">{m.errCount}/{m.hitCount} hits</span>
                  </button>
                {/each}
              </div>
            </div>
          {/if}
          {#if expiringCertList.length > 0}
            <div class="insight-group">
              <h4>Certificates expiring soon <span class="sub">— within 14 days</span></h4>
              <div class="insight-list">
                {#each expiringCertList as c (c.id)}
                  <button class="insight-row clickable-row" on:click={() => goToCert(c)}>
                    <span class="badge {c.daysLeft < 0 ? 'badge-err' : 'badge-warn'}">{c.daysLeft < 0 ? 'expired' : c.daysLeft + 'd left'}</span>
                    <span class="insight-name" title={c.commonName}>{c.commonName || c.name}</span>
                  </button>
                {/each}
              </div>
            </div>
          {/if}
        </div>
      {/if}
    </div>
  </div>
{/if}

<style>
  /* The card label is the keyboard-reachable control for cards that also contain
     their own buttons (a clickable container cannot itself be a button then). */
  .stat-label-btn { all: unset; cursor: pointer; border-radius: 3px; }
  .stat-label-btn:focus-visible { outline: 2px solid var(--primary, currentColor); outline-offset: 2px; }
  .sub { color: var(--muted); font-size: 13px; margin-top: -8px; }
  .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(200px, 1fr)); gap: 16px; margin-top: 20px; }
  .stat-label { font-size: 12px; color: var(--muted); font-weight: 600; text-transform: uppercase; letter-spacing: .3px; }
  .stat-val { font-size: 26px; font-weight: 800; margin-top: 6px; }
  .stat-unit { font-size: 14px; font-weight: 600; color: var(--muted); margin-left: 2px; }
  .stat-row { margin-top: 6px; font-size: 13px; color: var(--muted); display: flex; align-items: center; gap: 6px; flex-wrap: wrap; }

  .panels { display: grid; grid-template-columns: repeat(auto-fit, minmax(280px, 1fr)); gap: 16px; margin-top: 16px; }
  /* Every .panel in a row is grid-stretched to match its tallest sibling
     (e.g. "By protocol" with two stacked breakdown lists) — a sparkline
     card's fixed-height chart would otherwise sit pinned under its h3 with
     a dead gap below. flex column + margin-top:auto on the chart/empty-
     state pins it to the card's bottom edge (a normal bar-chart baseline)
     regardless of how much extra height the row stretch adds; when there's
     no stretch, auto margin has nothing to consume so nothing moves. */
  .panel { display: flex; flex-direction: column; }
  .panel h3 { margin: 0 0 12px; font-size: 13px; text-transform: uppercase; letter-spacing: .3px; color: var(--muted); }
  .heading-scope { font-weight: 400; text-transform: none; letter-spacing: normal; opacity: .7; }
  .panel .sub-heading { margin: 16px 0 12px; }
  .panel-wide { grid-column: 1 / -1; }
  .empty { margin: 0; }
  .panel > .empty, .panel > .sparkline { margin-top: auto; }

  .sparkline { display: flex; align-items: flex-end; gap: 3px; height: 90px; }
  .spark-bar { flex: 1; background: var(--primary); border-radius: 2px 2px 0 0; min-height: 3px; opacity: .85; transition: opacity .15s; }
  .spark-bar:hover { opacity: 1; }
  .latency-bar { background: var(--warn); }
  .spark-bar-empty { flex: 1; height: 3px; background: var(--border); border-radius: 2px; opacity: .5; }

  /* max-height + scroll: protocolCounts/directionCounts group by whatever
     distinct protocolType/direction strings actually show up in the hit
     log, not a value this page controls — defensive against that list
     someday being longer than the card can show cleanly (more protocols,
     unexpected direction tags) rather than growing the whole card and
     throwing off its row's height relative to its siblings. */
  .breakdown { display: flex; flex-direction: column; gap: 8px; max-height: 220px; overflow-y: auto; }
  .breakdown-row { display: flex; align-items: center; gap: 10px; }
  .breakdown-row .chip { flex: 0 0 auto; }
  .bar-track { flex: 1; height: 6px; border-radius: 3px; background: var(--hover); overflow: hidden; }
  .bar-fill { height: 100%; background: var(--primary); border-radius: 3px; }
  /* min-width (not a fixed flex-basis) reserves alignment space for the
     common case (small counts) without clipping/overflowing once a count
     grows past 3 digits — flex:0 0 28px would hold every row's number
     column to exactly 28px regardless of content width. */
  .bar-count { flex: 0 0 auto; min-width: 28px; text-align: right; font-size: 12px; color: var(--muted); font-weight: 600; }

  .top-mocks { display: flex; flex-direction: column; gap: 8px; }
  .top-mock-row { display: flex; align-items: center; gap: 10px; }

  /* Cards/rows that double as nav links — a plain <button>/<div role=button>
     reset to look identical to the static version they replace, then a
     hover cue on top so it's discoverable without shouting "I'm a button." */
  .card.clickable { cursor: pointer; text-align: left; }
  /* Layout (display/align-items/gap/padding) comes from the row's own
     class (.top-mock-row/.insight-row/.activity-row) — this only strips
     the native <button>'s chrome so it reads as the same row, not a
     button-shaped thing. No `all: unset` here: that would reset `display`
     too, and cascade order (this rule sits after the row classes below)
     would let it win and collapse the flex layout. */
  .clickable-row {
    background: none; border: none; font: inherit; color: inherit;
    box-sizing: border-box; cursor: pointer; width: 100%; text-align: left;
  }
  .top-mock-row.clickable-row:hover, .insight-row.clickable-row:hover { background: var(--surface2, var(--hover)); }
  .activity-row.clickable-row:hover { filter: brightness(0.97); }
  .clickable-row:disabled { cursor: default; opacity: .6; }

  .stat-link {
    background: none; border: none; padding: 0; margin: 0; font: inherit; color: inherit;
    cursor: pointer; text-decoration: underline; text-decoration-style: dotted; text-underline-offset: 2px;
  }
  .stat-link:hover { color: var(--primary); }
  .stat-row button.chip { cursor: pointer; font: inherit; }

  .breakdown-popover {
    margin-top: 10px; padding-top: 10px; border-top: 1px solid var(--border);
    display: flex; flex-direction: column; gap: 4px; max-height: 220px; overflow-y: auto;
  }
  .breakdown-item {
    all: unset; box-sizing: border-box; display: flex; align-items: center; gap: 8px;
    padding: 6px 8px; border-radius: 6px; cursor: pointer; font-size: 12px;
  }
  .breakdown-item:hover { background: var(--surface2, var(--hover)); }
  .breakdown-item-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
  .breakdown-popover .empty { font-size: 12px; margin: 4px 8px; }
  .top-mock-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }
  .top-mock-count { font-size: 13px; font-weight: 700; color: var(--muted); }

  .activity-list { display: flex; flex-direction: column; gap: 6px; }
  .activity-row { display: flex; align-items: center; gap: 10px; padding: 8px 10px; border-radius: 8px; background: var(--hover); }
  .activity-path { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; font-family: monospace; }
  .activity-latency { font-size: 12px; color: var(--muted); flex: 0 0 50px; text-align: right; }
  .activity-time { font-size: 12px; color: var(--muted); flex: 0 0 60px; text-align: right; }

  /* auto-fit rather than a fixed column count: with one insight group
     (the common case) it fills the wide card as one column; with all
     three present it lays out side by side without needing a breakpoint. */
  .insights-grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(220px, 1fr)); gap: 20px; }
  .insight-group h4 { margin: 0 0 8px; font-size: 12px; text-transform: uppercase; letter-spacing: .3px; color: var(--text); }
  .insight-group h4 .sub { display: block; font-size: 11px; text-transform: none; letter-spacing: normal; margin-top: 2px; }
  .insight-list { display: flex; flex-direction: column; gap: 6px; }
  .insight-row { display: flex; align-items: center; gap: 8px; padding: 6px 8px; border-radius: 8px; background: var(--hover); }
  .insight-name { flex: 1; min-width: 0; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; font-size: 13px; }
  .insight-meta { font-size: 11px; color: var(--muted); flex: 0 0 auto; }

  /* Deliberately last: on a narrow viewport the row's other five fixed-
     width children (method badge, protocol chip, status badge, latency,
     time) alone already add up to nearly the full row width, so
     .activity-path's flex:1/flex-basis:0% gets 0px left over and the URL
     text renders with zero width — present in the DOM, invisible on
     screen. Giving it order:-1 + flex-basis:100% moves it first and forces
     it onto its own full-width line; the other five (still order:0, and
     comfortably narrower than the row without path competing for space)
     then wrap together onto a second line below it. */
  @media (max-width: 640px) {
    .activity-row { flex-wrap: wrap; row-gap: 4px; }
    .activity-path {
      order: -1; flex: 1 1 100%;
      white-space: normal; word-break: break-all; overflow: visible; text-overflow: clip;
    }
  }
</style>
