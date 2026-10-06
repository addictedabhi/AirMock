<script>
  import { onMount } from 'svelte';
  import { installConfirmOverride } from './lib/uiPrefs.js';
  import { authState, checkAuthStatus, logout, installSessionExpiryPoller } from './lib/auth.js';
  import { installInactivityWatcher } from './lib/inactivityWatcher.js';
  import { toggleMobileSidebar, closeMobileSidebar } from './lib/sidebarMobile.js';
  import Sidebar from './lib/Sidebar.svelte';
  import Toast from './lib/Toast.svelte';
  import CommandPalette from './lib/CommandPalette.svelte';
  import Login from './lib/Login.svelte';
  import Dashboard from './lib/pages/Dashboard.svelte';
  import Mocks from './lib/pages/Mocks.svelte';
  import TCPMocks from './lib/pages/TCPMocks.svelte';
  import SMTPMocks from './lib/pages/SMTPMocks.svelte';
  import WSMocks from './lib/pages/WSMocks.svelte';
  import MQTTMocks from './lib/pages/MQTTMocks.svelte';
  import FTPMocks from './lib/pages/FTPMocks.svelte';
  import KafkaMocks from './lib/pages/KafkaMocks.svelte';
  import SMPPMocks from './lib/pages/SMPPMocks.svelte';
  import DiameterMocks from './lib/pages/DiameterMocks.svelte';
  import JMSMocks from './lib/pages/JMSMocks.svelte';
  import ScheduledEvents from './lib/pages/ScheduledEvents.svelte';
  import Collections from './lib/pages/Collections.svelte';
  import Certificates from './lib/pages/Certificates.svelte';
  import Smtp from './lib/pages/Smtp.svelte';
  import Logs from './lib/pages/Logs.svelte';
  import Settings from './lib/pages/Settings.svelte';
  import { currentPage, pages } from './lib/router.js';

  const pageComponents = {
    dashboard: Dashboard,
    mocks: Mocks,
    tcpmocks: TCPMocks,
    smtpmocks: SMTPMocks,
    wsmocks: WSMocks,
    mqttmocks: MQTTMocks,
    ftpmocks: FTPMocks,
    kafkamocks: KafkaMocks,
    smppmocks: SMPPMocks,
    diametermocks: DiameterMocks,
    jmsmocks: JMSMocks,
    scheduledevents: ScheduledEvents,
    collections: Collections,
    certificates: Certificates,
    smtp: Smtp,
    logs: Logs,
    settings: Settings,
  };

  $: activeLabel = pages.find((p) => p.id === $currentPage)?.label ?? '';
  // Closes the mobile sidebar overlay on ANY page change, not just a tap
  // on a Sidebar nav item itself (Sidebar.svelte's onNavItemClick already
  // covers that one directly) — the command palette's Ctrl/Cmd+K jump is
  // the other real way to navigate, and shouldn't leave the overlay open
  // behind it. Harmless/idempotent if it's already closed.
  $: if ($currentPage) closeMobileSidebar();

  // A deliberate deterrent, not a real security boundary — no page can
  // actually stop a determined user from opening DevTools (the browser's
  // own ⋮ → More tools → Developer tools menu is never reachable from page
  // JS, and every keyboard shortcut here can be reassigned or just ignored
  // by the browser depending on OS/build). This only removes the two most
  // casual paths in: right-click → Inspect, and the common keyboard
  // shortcuts. A genuine context menu (see ContextMenu.svelte, used on the
  // Mocks pages) calls stopPropagation on its own trigger so this global
  // handler never fights it.
  function blockContextMenu(e) {
    e.preventDefault();
  }
  function blockDevToolsShortcuts(e) {
    const key = e.key.toUpperCase();
    if (key === 'F12') {
      e.preventDefault();
      return;
    }
    if ((e.ctrlKey || e.metaKey) && e.shiftKey && (key === 'I' || key === 'J' || key === 'C')) {
      e.preventDefault();
    }
  }
  onMount(() => {
    installConfirmOverride();
    installInactivityWatcher();
    installSessionExpiryPoller();
    checkAuthStatus();
    document.addEventListener('contextmenu', blockContextMenu);
    document.addEventListener('keydown', blockDevToolsShortcuts);
    return () => {
      document.removeEventListener('contextmenu', blockContextMenu);
      document.removeEventListener('keydown', blockDevToolsShortcuts);
    };
  });
</script>

{#if $authState === null}
  <!-- Brief window before the first /api/auth/status check resolves —
       showing nothing here (rather than the real app, or Login) avoids a
       flash of the wrong view either way. -->
{:else if $authState.authRequired && !$authState.authenticated}
  <Login />
{:else}
  <div class="shell">
    <Sidebar />
    <main class="main">
      <header class="topbar">
        <div class="topbar-left">
          <button class="hamburger-btn" on:click={toggleMobileSidebar} aria-label="Toggle navigation menu">
            <span></span><span></span><span></span>
          </button>
          <span class="crumb">{activeLabel}</span>
        </div>
        <div class="topbar-right">
          <span class="cmdk-hint">Ctrl/Cmd+K to jump to any mock, request, or page</span>
          {#if $authState.authRequired}
            <button class="btn btn-ghost small" on:click={logout}>Log out</button>
          {/if}
        </div>
      </header>
      <section class="page-content">
        {#key $currentPage}
          <div class="page-enter">
            <svelte:component this={pageComponents[$currentPage]} />
          </div>
        {/key}
      </section>
    </main>
  </div>
  <Toast />
  <CommandPalette />
{/if}

<style>
  :global(html, body, #app) { height: 100%; }
  .shell { display: flex; height: 100vh; overflow: hidden; }
  .main { flex: 1; display: flex; flex-direction: column; overflow: hidden; background: var(--bg); }
  .topbar {
    padding: 14px 24px; border-bottom: 1px solid var(--border);
    background: var(--surface); display: flex; align-items: center; justify-content: space-between;
  }
  .crumb {
    font-size: 13px; font-weight: 700; color: var(--muted); text-transform: uppercase; letter-spacing: .4px;
    white-space: nowrap; overflow: hidden; text-overflow: ellipsis; min-width: 0;
  }
  .topbar-left { display: flex; align-items: center; gap: 12px; min-width: 0; }
  .topbar-right { display: flex; align-items: center; gap: 16px; }
  .cmdk-hint { font-size: 12px; color: var(--muted); }
  .page-content { flex: 1; overflow: auto; padding: 24px; }

  /* Hidden entirely above the breakpoint — Sidebar.svelte's own
     off-canvas/backdrop CSS only activates under the same 768px
     condition, so there's no way to open an overlay that has nowhere to
     be triggered from on a wide screen, and no need for the button either. */
  .hamburger-btn {
    display: none; flex-direction: column; justify-content: center; gap: 4px;
    width: 32px; height: 32px; padding: 0; border: none; background: none; cursor: pointer;
    flex-shrink: 0;
  }
  .hamburger-btn span { display: block; width: 18px; height: 2px; background: var(--text); border-radius: 1px; }

  @media (max-width: 768px) {
    .hamburger-btn { display: flex; }
    .topbar { padding: 12px 16px; }
    .page-content { padding: 14px; }
    /* The keyboard-shortcut hint is dead space on a device with no
       physical keyboard, and crowds the Log out button at this width. */
    .cmdk-hint { display: none; }
  }
</style>
