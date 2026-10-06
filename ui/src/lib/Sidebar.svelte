<script>
  import { currentPage, navigate } from './router.js';
  import { theme, themes, setTheme, accentsByTheme, setAccent } from './theme.js';
  import { orderedPages, reorderNav } from './navOrder.js';
  import { mobileSidebarOpen, closeMobileSidebar } from './sidebarMobile.js';
  import Icon from './Icon.svelte';

  const SIDEBAR_STORAGE_KEY = 'airmock-sidebar-collapsed';

  // Restored from localStorage rather than always starting expanded — the
  // collapse button previously had nowhere to persist to, so it silently
  // reset to expanded on every reload regardless of what the user last set.
  export let collapsed = typeof localStorage !== 'undefined' && localStorage.getItem(SIDEBAR_STORAGE_KEY) === 'true';
  let spinning = false;
  let showThemeMenu = false;
  let toggleEl;
  let menuStyle = '';

  // `collapsed` is a persisted DESKTOP icon-only preference — while the
  // sidebar is showing as a mobile off-canvas overlay it isn't competing
  // with page content for width at all, so a collapsed-on-desktop
  // preference shouldn't also hide labels there; full labels are more
  // useful in an overlay regardless of what was last set on a wide screen.
  $: effectiveCollapsed = collapsed && !$mobileSidebarOpen;

  $: activeTheme = themes.find((t) => t.id === $theme) ?? themes[0];
  // The active theme's OWN accent list (its own color family — see
  // theme.js) and whichever id (if any) is on record for THIS theme
  // specifically; a pick made under a different theme never leaks in.
  $: activeAccentId = $accentsByTheme[activeTheme.id] ?? '';
  $: activeAccentSwatch = activeTheme.accents.find((a) => a.id === activeAccentId)?.swatch ?? activeTheme.swatch;

  // The sidebar's own overflow:hidden (needed so nav labels clip cleanly
  // during the collapse-width transition) would also clip this popover if
  // it were positioned relative to a sidebar ancestor — position:fixed with
  // coordinates computed from the trigger button escapes that clipping
  // regardless of whether the sidebar is collapsed or expanded.
  function toggleMenu() {
    showThemeMenu = !showThemeMenu;
    if (showThemeMenu && toggleEl) {
      const r = toggleEl.getBoundingClientRect();
      menuStyle = `left: ${r.left}px; bottom: ${window.innerHeight - r.top + 8}px;`;
    }
  }

  function onSelectTheme(id) {
    spinning = true;
    setTheme(id);
    showThemeMenu = false;
    setTimeout(() => (spinning = false), 550);
  }

  // Accent picking stays inside the same popover as the theme list rather
  // than closing it — the accent row only ever shows the CURRENT theme's
  // own options (see activeAccentId above), so a user comparing a few of
  // them shouldn't have to reopen the menu after every click.
  function onSelectAccent(id) {
    spinning = true;
    setAccent(activeTheme.id, id);
    setTimeout(() => (spinning = false), 550);
  }

  function onWindowClick(ev) {
    if (!ev.target.closest?.('.theme-picker')) showThemeMenu = false;
  }

  function onWindowKeydown(ev) {
    if (ev.key === 'Escape' && $mobileSidebarOpen) closeMobileSidebar();
  }

  function toggleCollapsed() {
    collapsed = !collapsed;
    localStorage.setItem(SIDEBAR_STORAGE_KEY, String(collapsed));
  }

  // A plain click navigates; a click that was actually the tail end of a
  // hold-then-drag (below) must NOT also navigate — suppressClick is set
  // exactly when a drag really happened, and consumed (reset) the next
  // time a click fires, whether or not a reorder actually resulted.
  let suppressClick = false;
  function onNavItemClick(id) {
    if (suppressClick) {
      suppressClick = false;
      return;
    }
    navigate(id);
    closeMobileSidebar();
  }

  // Nav item drag-and-drop reordering (see navOrder.js for the persisted
  // order itself), built on Pointer Events rather than HTML5 native
  // drag-and-drop — native drag has no touch-event fallback at all, so it
  // only ever worked with a mouse. The whole row is draggable again (no
  // separate handle), gated behind a brief press-and-hold ON TOUCH ONLY: a
  // touch device's same gesture space is also "scroll the nav list" and
  // "tap to navigate", so a plain pointerdown->drag would fight both;
  // requiring HOLD_MS of near-stillness first is what a touch reorder
  // list conventionally uses to disambiguate all three from each other.
  //
  // A MOUSE has none of that ambiguity — there's no competing "scroll by
  // dragging the row" gesture (mice scroll via the wheel), and click-vs-
  // drag is already cleanly separated by whether real movement happened
  // before pointerup, the standard desktop drag-and-drop convention. Only
  // requiring the same touch-style hold for a mouse too (the original bug
  // here) made a mouse drag silently do nothing for anyone who reorders
  // the way desktop users actually do — press and immediately move — since
  // that movement got swallowed by the touch-only "was this a scroll?"
  // cancel path (MOVE_CANCEL_PX) instead of ever starting a drag; only a
  // mouse user who happened to hold dead still for HOLD_MS first ever saw
  // it work. onNavPointerDown/onNavPointerMove branch on e.pointerType
  // ('mouse' vs everything else) to give each input its own natural gesture.
  //
  // .nav-item has touch-action:none (see its CSS) — without it, a touch
  // move big enough to look like a scroll gets reclaimed by the browser
  // as a real scroll gesture, which cancels the whole pointer sequence
  // (a pointercancel, observed firing within single-digit ms of a
  // pointermove) regardless of calling preventDefault() on the pointer
  // event itself — that only ever stops OTHER pointer-event side effects,
  // never the browser's own default touch handling; only touch-action (or
  // preventDefault on the underlying touchmove) does that. The trade-off:
  // since the browser now never scrolls .sb-nav for us once a touch
  // starts on a row, a hold that gets cancelled by real movement (read:
  // an ordinary scroll attempt) has to drive that scroll manually from
  // here instead — see the `scrolling` branch below. None of this applies
  // to mouse input, which never goes through the scrolling branch at all.
  //
  // draggingId/dragOverId are purely local visual state — dragOverId
  // tracks whichever OTHER row the pointer is currently over so the drop
  // target gets a highlight, not just the item being dragged.
  const HOLD_MS = 450;
  const MOVE_CANCEL_PX = 10; // touch: this much movement before HOLD_MS means "scroll", not "drag"
  const MOUSE_DRAG_PX = 6; // mouse: this much movement means "drag" — no hold needed first
  let draggingId = null;
  let dragOverId = null;
  let holdTimer = null;
  let holdStart = null; // { x, y } — set only while waiting to see if this is a hold/drag start
  let scrolling = false;
  let lastScrollY = 0;
  let navEl = null;
  // The row + pointer a gesture started on, held here (rather than only
  // inside the touch path's setTimeout closure) since the mouse path also
  // needs them later, from onNavPointerMove instead of a timer callback.
  let pendingId = null;
  let pendingTargetEl = null;
  let pendingPointerId = null;
  let pendingIsMouse = false;

  function clearHoldTimer() {
    clearTimeout(holdTimer);
    holdTimer = null;
  }
  function resetGesture() {
    clearHoldTimer();
    holdStart = null;
    scrolling = false;
    draggingId = null;
    dragOverId = null;
    pendingId = null;
    pendingTargetEl = null;
    pendingPointerId = null;
    pendingIsMouse = false;
  }
  function beginDrag() {
    draggingId = pendingId;
    suppressClick = true;
    pendingTargetEl.setPointerCapture(pendingPointerId);
  }
  function onNavPointerDown(e, id) {
    if (e.button !== undefined && e.button !== 0) return; // left-click only for mouse
    // A second pointer going down (e.g. a touchscreen laptop where a mouse
    // and a finger both press) must NOT clobber a gesture already in
    // flight for a different pointer — all this state is page-level, not
    // scoped per pointerId, so without this guard the second pointer's
    // moves/release could alter or finish the FIRST pointer's drag.
    if (pendingPointerId !== null && pendingPointerId !== e.pointerId) return;
    pendingId = id;
    pendingTargetEl = e.currentTarget; // currentTarget goes stale once the event finishes dispatching
    pendingPointerId = e.pointerId;
    pendingIsMouse = e.pointerType === 'mouse';
    navEl = pendingTargetEl.closest('.sb-nav');
    holdStart = { x: e.clientX, y: e.clientY };
    scrolling = false;
    clearHoldTimer();
    if (pendingIsMouse) {
      // No timer for mouse — onNavPointerMove promotes this straight to a
      // drag on the first real movement past MOUSE_DRAG_PX instead.
      return;
    }
    holdTimer = setTimeout(() => {
      holdStart = null;
      beginDrag();
    }, HOLD_MS);
  }
  function onNavPointerMove(e) {
    if (draggingId) {
      const el = document.elementFromPoint(e.clientX, e.clientY);
      const row = el?.closest('.nav-item');
      const overId = row?.dataset.navId ?? null;
      dragOverId = overId && overId !== draggingId ? overId : null;
      return;
    }
    if (scrolling) {
      if (navEl) navEl.scrollTop -= e.clientY - lastScrollY;
      lastScrollY = e.clientY;
      return;
    }
    if (!holdStart) return;
    const dist = Math.hypot(e.clientX - holdStart.x, e.clientY - holdStart.y);
    if (pendingIsMouse) {
      if (dist > MOUSE_DRAG_PX) {
        holdStart = null;
        beginDrag();
      }
      return;
    }
    if (dist > MOVE_CANCEL_PX) {
      // Real movement before the hold fired — an ordinary scroll attempt,
      // not a hold-to-drag. Take it over manually from here (see the big
      // comment above this function for why the browser won't).
      clearHoldTimer();
      scrolling = true;
      lastScrollY = holdStart.y;
      holdStart = null;
      if (navEl) navEl.scrollTop -= e.clientY - lastScrollY;
      lastScrollY = e.clientY;
    }
  }
  function onNavPointerUp() {
    if (draggingId && dragOverId) reorderNav(draggingId, dragOverId);
    resetGesture();
  }
  function onNavPointerCancel() {
    resetGesture();
  }
</script>

<!-- A window-level fallback for the per-row pointerup/pointercancel
     listeners below: during the "pending hold" phase (before beginDrag
     calls setPointerCapture), a touch released somewhere that isn't a
     .nav-item — the ~2px gaps between rows, the sidebar footer, the
     mobile backdrop, anywhere outside the sidebar entirely — never
     reaches a .nav-item's own listener at all, since no capture is in
     effect yet to redirect it there. Without this, resetGesture() never
     runs, the still-pending holdTimer fires ~450ms later with a pointer
     that's already gone, and the row is left stuck mid-drag (opacity
     .35) with suppressClick stuck true, silently eating the next
     unrelated click anywhere in the sidebar. Calling onNavPointerUp/
     onNavPointerCancel here is a no-op whenever a per-row listener
     already handled the same event (resetGesture is idempotent) — this
     is purely a safety net for the gap, not a second active path. -->
<svelte:window on:click={onWindowClick} on:keydown={onWindowKeydown} on:pointerup={onNavPointerUp} on:pointercancel={onNavPointerCancel} />

{#if $mobileSidebarOpen}
  <!-- A full-screen dismiss-on-click scrim, not a real button — the
       window-level Escape listener below is the keyboard-driven
       equivalent, since a plain div can't receive its own keydown without
       a tabindex it has no real reason to hold otherwise. -->
  <!-- svelte-ignore a11y_no_static_element_interactions -->
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <div class="sb-backdrop" on:click={closeMobileSidebar}></div>
{/if}

<aside class="sb" class:collapsed class:mobile-open={$mobileSidebarOpen}>
  <div class="sb-logo">
    <div class="sb-logo-row">
      <!-- Inlined rather than an <img src="/favicon.svg"> so the gradient/
           glow filter renders crisply at this small size regardless of how
           a given browser rasterizes an <img>-referenced SVG — the same
           mark as the favicon and the test-email header, kept as one
           literal copy per context rather than a shared asset, since each
           needs its own container sizing anyway. -->
      <svg class="sb-logo-mark" viewBox="0 0 100 100" role="img" aria-label="AirMock">
        <defs>
          <linearGradient id="sbBracketGrad" x1="10%" y1="0%" x2="90%" y2="100%">
            <stop offset="0%" stop-color="#0052cc" />
            <stop offset="100%" stop-color="#0a78d4" />
          </linearGradient>
        </defs>
        <path d="M45,20 L20,50 L45,80" fill="none" stroke="url(#sbBracketGrad)" stroke-width="12" stroke-linecap="round" stroke-linejoin="round" />
        <path d="M55,20 L80,50 L55,80" fill="none" stroke="url(#sbBracketGrad)" stroke-width="12" stroke-linecap="round" stroke-linejoin="round" />
        <circle cx="50" cy="50" r="9" fill="#22d3ee" />
      </svg>
      {#if !effectiveCollapsed}<div class="sb-logo-name">AirMock</div>{/if}
    </div>
    {#if !effectiveCollapsed}
      <div class="sb-logo-sub">Cross-protocol mock server</div>
      <div class="sb-divider"></div>
    {/if}
  </div>

  <nav class="sb-nav">
    {#each $orderedPages as p (p.id)}
      <button
        class="nav-item"
        data-nav-id={p.id}
        class:active={$currentPage === p.id}
        class:dragging={draggingId === p.id}
        class:drag-over={dragOverId === p.id}
        on:click={() => onNavItemClick(p.id)}
        on:pointerdown={(e) => onNavPointerDown(e, p.id)}
        on:pointermove={onNavPointerMove}
        on:pointerup={onNavPointerUp}
        on:pointercancel={onNavPointerCancel}
        on:contextmenu={(e) => e.preventDefault()}
        title={p.label}
      >
        <span class="nav-icon"><Icon name={p.id} /></span>
        {#if !effectiveCollapsed}<span class="nav-label">{p.label}</span>{/if}
      </button>
    {/each}
  </nav>

  <div class="sb-footer">
    <div class="theme-picker">
      <button
        bind:this={toggleEl}
        class="theme-toggle"
        class:theme-spin={spinning}
        on:click|stopPropagation={toggleMenu}
        title="Choose a theme"
      >
        <span class="theme-swatch-dot" style="background:{activeAccentSwatch}"></span>
        {#if !effectiveCollapsed}<span class="theme-current-label">{activeTheme.label}</span>{/if}
      </button>
      {#if showThemeMenu}
        <div class="theme-menu" style={menuStyle}>
          {#each themes as t (t.id)}
            <button
              class="theme-option"
              class:active={t.id === $theme}
              on:click|stopPropagation={() => onSelectTheme(t.id)}
              title={t.label}
            >
              <span class="theme-swatch-dot" style="background:{t.swatch}"></span>
              <span class="theme-option-label">{t.label}</span>
              {#if t.id === $theme}<span class="theme-check">✓</span>{/if}
            </button>
          {/each}
          <div class="theme-menu-divider"></div>
          <div class="accent-label">{activeTheme.label} accent</div>
          <div class="accent-row">
            <button
              class="accent-dot"
              class:active={!activeAccentId}
              style="background:{activeTheme.swatch}"
              on:click|stopPropagation={() => onSelectAccent('default')}
              title="Theme default"
            >
              {#if !activeAccentId}<span class="accent-check">✓</span>{/if}
            </button>
            {#each activeTheme.accents as a (a.id)}
              <button
                class="accent-dot"
                class:active={a.id === activeAccentId}
                style="background:{a.swatch}"
                on:click|stopPropagation={() => onSelectAccent(a.id)}
                title={a.label}
              >
                {#if a.id === activeAccentId}<span class="accent-check">✓</span>{/if}
              </button>
            {/each}
          </div>
        </div>
      {/if}
    </div>
    <button class="collapse-btn" on:click={toggleCollapsed} title="Collapse sidebar">
      <span class="collapse-arrow" class:flipped={collapsed}>«</span>
    </button>
  </div>
</aside>

<style>
  .sb {
    width: 224px;
    background: var(--sb);
    border-right: 1px solid var(--sb-border);
    display: flex;
    flex-direction: column;
    flex-shrink: 0;
    overflow: hidden;
    transition: width .3s cubic-bezier(.4,0,.2,1);
  }
  .sb.collapsed { width: 64px; }

  /* Below the breakpoint, the sidebar stops being a permanent flex column
     (competing with page content for width — the thing that made every
     page basically unusable on a phone) and becomes an off-canvas overlay
     instead: hidden via transform (not display:none, so the width/slide
     transition still animates) until .mobile-open, then it slides in on
     top of everything else. .collapsed's icon-only width is deliberately
     ignored here — see effectiveCollapsed's own comment — an overlay isn't
     sharing space with anything, so full labels are strictly more useful. */
  @media (max-width: 768px) {
    .sb {
      position: fixed; inset: 0 auto 0 0; z-index: 401;
      width: 240px; max-width: 82vw;
      transform: translateX(-100%);
      transition: transform .28s cubic-bezier(.4,0,.2,1);
      box-shadow: 4px 0 24px rgba(0,0,0,.25);
    }
    .sb.collapsed { width: 240px; max-width: 82vw; }
    .sb.mobile-open { transform: translateX(0); }
  }

  .sb-backdrop {
    position: fixed; inset: 0; background: rgba(0,0,0,.45); z-index: 400;
    animation: backdrop-in .18s ease both;
  }
  @keyframes backdrop-in {
    0% { opacity: 0; }
    100% { opacity: 1; }
  }

  .sb-logo {
    padding: 18px 16px;
    border-bottom: 1px solid var(--sb-border);
    background: var(--brand-grad);
  }
  .sb-logo-row { display: flex; align-items: center; gap: 10px; }
  .sb-logo-mark { width: 26px; height: 26px; display: block; flex-shrink: 0; }
  .sb-logo-name {
    font-size: 15px; font-weight: 800; color: #fff;
    letter-spacing: .2px; white-space: nowrap;
  }
  .sb-logo-sub {
    font-size: 10px; color: var(--sb-muted); font-weight: 500;
    white-space: nowrap; margin-top: 4px;
  }
  .sb-divider {
    width: 32px; height: 2px; background: var(--brand-divider);
    border-radius: 1px; margin-top: 10px;
  }

  .sb-nav {
    flex: 1; min-height: 0; padding: 10px 8px; display: flex; flex-direction: column; gap: 2px;
    overflow-y: auto; overflow-x: hidden;
  }
  /* Thin, unobtrusive scrollbar — the sidebar's dark background makes a
     default full-width system scrollbar stand out much more than it would
     on a light page body. */
  .sb-nav { scrollbar-width: thin; scrollbar-color: var(--sb-hover) transparent; }
  .sb-nav::-webkit-scrollbar { width: 6px; }
  .sb-nav::-webkit-scrollbar-track { background: transparent; }
  .sb-nav::-webkit-scrollbar-thumb { background: var(--sb-hover); border-radius: 3px; }
  .nav-item {
    display: flex; align-items: center; gap: 10px; width: 100%;
    padding: 9px 12px; border-radius: 8px;
    border: none; background: none; cursor: pointer;
    font-size: 13px; color: var(--sb-muted);
    transition: all .18s cubic-bezier(.4,0,.2,1);
    white-space: nowrap; overflow: hidden; text-align: left;
    /* .sb-nav's overflow-y:auto only kicks in once its children actually
       overflow it — without this, flexbox shrinks each item (the default
       flex-shrink:1) to fit a too-short container FIRST, squishing every
       item down to a sliver instead of ever triggering the scrollbar.
       Only surfaced on a short viewport (a phone in landscape, or a
       keyboard eating vertical space) since most viewports are tall
       enough for all 17 items to already fit without either happening. */
    flex-shrink: 0;
    -webkit-tap-highlight-color: transparent; -webkit-user-select: none; user-select: none;
    /* touch-action:none is load-bearing, not a nicety: without it, the
       browser's own scroll-gesture recognizer still gets first look at
       any sufficiently large touchmove and reclaims it as a real page
       scroll — which cancels the ENTIRE pointer sequence outright (a
       pointercancel observed firing within single-digit ms of the
       pointermove that triggered it) regardless of calling
       preventDefault() in the pointermove handler, since that only
       suppresses other pointer-event side effects, never the browser's
       own default touch handling. The onNavPointerMove `scrolling` branch
       is what makes this an even trade rather than a regression: since
       the browser will no longer scroll .sb-nav for us at all once a
       touch starts on a row, that function drives the scroll manually
       once it decides a touch is a scroll and not a hold.
       -webkit-touch-callout:none is the second, smaller half of the same
       problem: iOS Safari's own long-press callout (Copy/Share) fires
       around the same ~500ms mark HOLD_MS waits out and would otherwise
       hijack the sequence the same way. */
    touch-action: none;
    -webkit-touch-callout: none;
  }
  .nav-item:hover { background: var(--sb-hover); color: #fff; }
  .nav-item.active {
    background: linear-gradient(135deg, var(--sb-active), var(--sb-active-2));
    color: #fff;
  }
  /* Dragging: the item being moved fades out in place (still occupies its
     slot — no layout reflow mid-drag, which would fight the drag image's
     own position) rather than disappearing outright. Drag-over: an inset
     top border on whichever OTHER row the pointer is currently over marks
     "drop here" without needing a separate placeholder element. */
  .nav-item.dragging { opacity: .35; }
  .nav-item.drag-over { box-shadow: inset 0 2px 0 0 var(--sb-active-2); }
  .nav-icon { width: 18px; height: 18px; display: flex; align-items: center; justify-content: center; flex-shrink: 0; }

  .sb-footer {
    padding: 12px 8px; border-top: 1px solid var(--sb-border);
    display: flex; align-items: center; justify-content: space-between;
    position: relative;
  }
  .theme-picker { position: relative; min-width: 0; flex: 1; }
  .theme-toggle, .collapse-btn {
    background: none; border: none; color: var(--sb-muted); cursor: pointer;
    font-size: 13px; padding: 6px 10px; border-radius: 6px;
    display: flex; align-items: center; gap: 8px;
    transition: color .15s, background .15s;
  }
  .theme-toggle { width: 100%; min-width: 0; flex-shrink: 0; }
  .collapse-btn { flex-shrink: 0; }
  .theme-toggle:hover, .collapse-btn:hover { background: var(--sb-hover); color: #fff; }
  .theme-current-label { white-space: nowrap; overflow: hidden; text-overflow: ellipsis; }
  .collapse-arrow { display: inline-block; transition: transform .3s cubic-bezier(.4,0,.2,1); }
  .collapse-arrow.flipped { transform: rotate(180deg); }

  .theme-swatch-dot {
    width: 12px; height: 12px; border-radius: 50%; flex-shrink: 0;
    box-shadow: 0 0 0 2px rgba(255,255,255,.15);
  }
  .theme-spin .theme-swatch-dot { animation: swatch-pop .4s cubic-bezier(.34,1.56,.64,1); }

  .theme-menu {
    position: fixed;
    background: var(--sb); border: 1px solid var(--sb-border);
    border-radius: 10px; padding: 6px; box-shadow: 0 12px 32px rgba(0,0,0,.35);
    display: flex; flex-direction: column; gap: 2px; min-width: 180px;
    animation: theme-menu-in .18s cubic-bezier(.22,1,.36,1) both;
    z-index: 200;
  }
  .theme-option {
    display: flex; align-items: center; gap: 10px; width: 100%;
    background: none; border: none; color: var(--sb-muted); cursor: pointer;
    font-size: 13px; padding: 8px 10px; border-radius: 6px; text-align: left;
    transition: background .15s, color .15s;
  }
  .theme-option:hover { background: var(--sb-hover); color: #fff; }
  .theme-option.active { color: #fff; font-weight: 600; }
  .theme-option-label { flex: 1; }
  .theme-check { font-size: 11px; }

  .theme-menu-divider { height: 1px; background: var(--sb-border); margin: 4px 2px; }
  .accent-label {
    font-size: 10px; font-weight: 700; color: var(--sb-muted);
    text-transform: uppercase; letter-spacing: .4px; padding: 4px 10px 2px;
  }
  .accent-row { display: flex; flex-wrap: wrap; gap: 8px; padding: 2px 10px 6px; }
  .accent-dot {
    width: 20px; height: 20px; border-radius: 50%; flex-shrink: 0;
    border: none; cursor: pointer; padding: 0;
    box-shadow: 0 0 0 2px rgba(255,255,255,.12);
    display: flex; align-items: center; justify-content: center;
    transition: transform .15s cubic-bezier(.4,0,.2,1), box-shadow .15s;
  }
  .accent-dot:hover { transform: scale(1.12); }
  .accent-dot.active { box-shadow: 0 0 0 2px #fff; }
  .accent-check { font-size: 10px; color: #fff; text-shadow: 0 1px 2px rgba(0,0,0,.6); }

  /* Deliberately last in the file: .theme-toggle, .collapse-btn's own
     unconditional "display: flex" rule further up wins over an earlier
     equal-specificity override regardless of which one sits inside a
     media query — source order decides ties, not specificity — so this
     has to come after it, not inside the (max-width: 768px) block above
     alongside .sb's other mobile rules, or it gets silently overridden
     right back (the exact bug already hit once this session, in
     Collections.svelte). The collapse toggle only means something for the
     permanent desktop column competing with page content for width; the
     mobile overlay's .sb.collapsed rule above already ignores .collapsed
     entirely (full width, full labels), so clicking this button there
     visibly did nothing — a dead control rather than a broken one, but
     confusing either way. Removed rather than wired up to do something,
     since there's nothing meaningful left for it to do once the sidebar
     is a closable overlay. */
  @media (max-width: 768px) {
    .collapse-btn { display: none; }
  }
</style>
