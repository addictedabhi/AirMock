<script>
  // Small hand-drawn stroke icon set for the sidebar nav — replaces the
  // previous plain-unicode-glyph icons (▣ ◉ ⇄ ☐ ⛨ ≡), which render
  // inconsistently across OS/fonts and read as an unfinished prototype next
  // to real text labels. Primitive-shape based (rect/line/circle/polyline)
  // rather than copied path data from a specific icon library, to avoid
  // subtly-wrong/invalid `d` strings reproduced from memory.
  export let name = '';
  export let size = 18;
</script>

<svg width={size} height={size} viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.75" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
  {#if name === 'dashboard'}
    <rect x="3" y="3" width="7" height="7" rx="1.5" />
    <rect x="14" y="3" width="7" height="7" rx="1.5" />
    <rect x="3" y="14" width="7" height="7" rx="1.5" />
    <rect x="14" y="14" width="7" height="7" rx="1.5" />
  {:else if name === 'mocks'}
    <rect x="3" y="4" width="18" height="7" rx="2" />
    <rect x="3" y="13" width="18" height="7" rx="2" />
    <circle cx="7" cy="7.5" r="0.75" fill="currentColor" stroke="none" />
    <circle cx="7" cy="16.5" r="0.75" fill="currentColor" stroke="none" />
  {:else if name === 'tcpmocks'}
    <polyline points="7 4 3 8 7 12" />
    <line x1="3" y1="8" x2="21" y2="8" />
    <polyline points="17 12 21 16 17 20" />
    <line x1="21" y1="16" x2="3" y2="16" />
  {:else if name === 'smtpmocks'}
    <rect x="3" y="8" width="18" height="12" rx="2" />
    <polyline points="3 10 12 15 21 10" />
    <line x1="12" y1="1" x2="12" y2="6.5" />
    <polyline points="9.5 4 12 6.5 14.5 4" />
  {:else if name === 'wsmocks'}
    <circle cx="12" cy="12" r="9" />
    <polyline points="13 7.5 9.5 12.5 12 12.5 11 16.5 14.5 11.5 12 11.5 13 7.5" fill="currentColor" stroke="none" />
  {:else if name === 'mqttmocks'}
    <circle cx="12" cy="12" r="2" fill="currentColor" stroke="none" />
    <circle cx="4" cy="5" r="1.5" />
    <circle cx="20" cy="5" r="1.5" />
    <circle cx="4" cy="19" r="1.5" />
    <circle cx="20" cy="19" r="1.5" />
    <line x1="10.3" y1="10.3" x2="5.3" y2="6.1" />
    <line x1="13.7" y1="10.3" x2="18.7" y2="6.1" />
    <line x1="10.3" y1="13.7" x2="5.3" y2="17.9" />
    <line x1="13.7" y1="13.7" x2="18.7" y2="17.9" />
  {:else if name === 'ftpmocks'}
    <path d="M3 7.5a1.5 1.5 0 0 1 1.5-1.5h4l1.5 1.8h8.5a1.5 1.5 0 0 1 1.5 1.5v8.2a1.5 1.5 0 0 1-1.5 1.5h-14A1.5 1.5 0 0 1 3 17.5z" />
    <line x1="12" y1="10" x2="12" y2="16" />
    <polyline points="9.2 13.2 12 16 14.8 13.2" />
  {:else if name === 'kafkamocks'}
    <line x1="4" y1="7" x2="14" y2="7" />
    <circle cx="17.5" cy="7" r="1.3" fill="currentColor" stroke="none" />
    <line x1="4" y1="12" x2="18" y2="12" />
    <circle cx="20.5" cy="12" r="1.3" fill="currentColor" stroke="none" />
    <line x1="4" y1="17" x2="11" y2="17" />
    <circle cx="14.5" cy="17" r="1.3" fill="currentColor" stroke="none" />
  {:else if name === 'smppmocks'}
    <path d="M4 5.5A1.5 1.5 0 0 1 5.5 4h13A1.5 1.5 0 0 1 20 5.5v9A1.5 1.5 0 0 1 18.5 16H10l-4 3.5V16H5.5A1.5 1.5 0 0 1 4 14.5z" />
    <circle cx="9" cy="10" r="0.9" fill="currentColor" stroke="none" />
    <circle cx="12" cy="10" r="0.9" fill="currentColor" stroke="none" />
    <circle cx="15" cy="10" r="0.9" fill="currentColor" stroke="none" />
  {:else if name === 'diametermocks'}
    <circle cx="5" cy="12" r="2.3" />
    <circle cx="19" cy="12" r="2.3" />
    <line x1="8.3" y1="9" x2="15.7" y2="9" />
    <polyline points="13.3 6.7 15.7 9 13.3 11.3" />
    <line x1="8.3" y1="15" x2="15.7" y2="15" />
    <polyline points="10.7 12.7 8.3 15 10.7 17.3" />
  {:else if name === 'jmsmocks'}
    <rect x="3.5" y="5" width="14" height="4" rx="1" />
    <rect x="3.5" y="10" width="14" height="4" rx="1" />
    <rect x="3.5" y="15" width="14" height="4" rx="1" />
    <polyline points="18.5 10 21 12 18.5 14" />
  {:else if name === 'scheduledevents'}
    <circle cx="12" cy="13" r="8" />
    <polyline points="12 9 12 13 15 15" />
    <line x1="9" y1="2" x2="15" y2="2" />
  {:else if name === 'collections'}
    <path d="M3 6.5a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v8.5a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-10.5z" />
  {:else if name === 'certificates'}
    <path d="M12 3l7 3v5c0 5-3.5 8.5-7 10-3.5-1.5-7-5-7-10V6l7-3z" />
    <polyline points="9 12 11 14 15 10" />
  {:else if name === 'smtp'}
    <rect x="3" y="5" width="18" height="14" rx="2" />
    <polyline points="3 7 12 13 21 7" />
  {:else if name === 'logs'}
    <line x1="4" y1="6" x2="20" y2="6" />
    <line x1="4" y1="12" x2="20" y2="12" />
    <line x1="4" y1="18" x2="14" y2="18" />
  {:else if name === 'settings'}
    <circle cx="12" cy="12" r="3" />
    <path d="M12 3v2.5M12 18.5V21M4.2 4.2l1.8 1.8M18 18l1.8 1.8M3 12h2.5M18.5 12H21M4.2 19.8l1.8-1.8M18 6l1.8-1.8" />
  {/if}
</svg>
