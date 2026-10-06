<script>
  // A themed replacement for <input list="..."> — native <datalist> popups
  // are rendered by the browser/OS and ignore all page CSS, so they look
  // wrong in a dark theme (or any theme but the browser's default). This
  // renders its own dropdown using the app's own CSS variables instead.
  //
  // Positioned via position:fixed computed from the input's own
  // getBoundingClientRect() (same technique as the sidebar theme-swatch
  // popover) rather than position:absolute, so it isn't clipped when this
  // component sits inside a scrollable list (e.g. a long header/query row
  // list).
  import { onDestroy } from 'svelte';

  export let value = '';
  export let options = [];
  export let placeholder = '';

  let open = false;
  let highlighted = -1;
  let inputEl;
  let rect = { top: 0, left: 0, width: 0, bottom: 0 };

  $: filtered = value.trim()
    ? options.filter((o) => o.toLowerCase().includes(value.toLowerCase()))
    : options;

  // Keep the top match highlighted by default whenever the dropdown opens
  // or the filtered set changes (typing) — without this, highlighted
  // stays at -1 until an arrow key is pressed, so pressing Enter right
  // after typing (the natural first instinct, before ever touching an
  // arrow key) silently did nothing.
  function openDropdown() {
    // on:input fires this on every keystroke while already open — only
    // (re)start the tracking loop on a genuine closed→open transition, or
    // typing a second character would spawn a second parallel
    // requestAnimationFrame chain alongside the first (and a third on the
    // next keystroke, and so on), each one independently re-measuring the
    // rect forever since trackPosition never stops itself while open.
    const wasOpen = open;
    open = true;
    // Always reset to the top match; harmless even if filtered ends up
    // empty for this keystroke (nothing renders, and onKeydown/choose
    // both already guard on filtered.length before using this index).
    highlighted = 0;
    if (!wasOpen) trackPosition();
  }

  // A single getBoundingClientRect() snapshot taken at focus time can be
  // stale — the row this input lives in may still be mid-entrance-
  // animation (translateY/opacity), so the "settled" position isn't known
  // yet, and the fixed-position dropdown ends up detached from the input
  // entirely. Re-measuring every animation frame while open self-corrects
  // once the animation (or any other pending layout change, scroll, etc.)
  // settles, and stops on its own the frame after open goes false.
  function trackPosition() {
    if (!open || !inputEl) return;
    rect = inputEl.getBoundingClientRect();
    requestAnimationFrame(trackPosition);
  }

  function choose(opt) {
    value = opt;
    open = false;
    highlighted = -1;
    inputEl?.focus();
  }

  function onKeydown(e) {
    if (!open || filtered.length === 0) return;
    if (e.key === 'ArrowDown') {
      e.preventDefault();
      highlighted = (highlighted + 1) % filtered.length;
    } else if (e.key === 'ArrowUp') {
      e.preventDefault();
      highlighted = (highlighted - 1 + filtered.length) % filtered.length;
    } else if (e.key === 'Enter') {
      e.preventDefault();
      choose(filtered[Math.max(highlighted, 0)]);
    } else if (e.key === 'Escape') {
      open = false;
    }
  }

  // CSS gotcha this works around: `position: fixed` is only viewport-
  // relative as long as NO ancestor has a `transform` — the moment one
  // does (e.g. this app's global `.card:hover { transform: translateY(-1px) }`,
  // which fires on ANY hover over the card this input lives in, i.e.
  // constantly during real use), that ancestor silently becomes the fixed
  // element's containing block instead, detaching the dropdown from the
  // input entirely. Portaling straight to <body> — which never has a
  // transform — sidesteps the problem regardless of what ancestors do.
  function portal(node) {
    document.body.appendChild(node);
    return {
      destroy() {
        node.remove();
      },
    };
  }

  // If this component is destroyed while the dropdown is still open (e.g.
  // the row it lives in is removed while a user is mid-focus), the
  // trackPosition RAF chain would otherwise keep rescheduling itself
  // forever since nothing else ever sets `open` back to false. Forcing it
  // false here lets the chain's own top-of-function guard stop it on the
  // very next frame.
  onDestroy(() => {
    open = false;
  });
</script>

<div class="suggest-wrap">
  <input aria-label={placeholder}
    type="text"
    {placeholder}
    bind:value
    bind:this={inputEl}
    on:focus={openDropdown}
    on:input={openDropdown}
    on:blur={() => setTimeout(() => (open = false), 150)}
    on:keydown={onKeydown}
  />
</div>

{#if open && filtered.length > 0}
  <!-- Rounded to whole pixels: getBoundingClientRect() returns fractional
       values (e.g. 432.875), and a position:fixed element placed at a
       fractional offset forces the browser to sub-pixel-render its text,
       which reads as faint/blurry next to normal-flow text elsewhere in
       the app (positioned at whole pixels and rendered crisply).

       Width is min-width (not width): the dropdown must be at least as
       wide as the input, but many real option values (e.g. the
       Content-Type suggestions' full MIME strings) are far longer than a
       typical narrow key/value input — forcing the list to exactly the
       input's width truncated them with an ellipsis, which is what was
       actually being reported as "can't see the full name," not a color
       issue. Left is clamped so a widened dropdown can't run off the
       right edge of the viewport. -->
  {@const maxWidth = Math.min(360, window.innerWidth - 24)}
  {@const left = Math.min(Math.round(rect.left), window.innerWidth - maxWidth - 12)}
  <div
    use:portal
    class="suggest-list"
    style="top: {Math.round(rect.bottom + 4)}px; left: {left}px; min-width: {Math.round(rect.width)}px; max-width: {maxWidth}px;"
  >
    {#each filtered as opt, i}
      <button
        type="button"
        class="suggest-item"
        class:highlighted={i === highlighted}
        on:mousedown|preventDefault={() => choose(opt)}
      >
        {opt}
      </button>
    {/each}
  </div>
{/if}

<style>
  .suggest-wrap { flex: 1; min-width: 0; }
  .suggest-wrap input {
    width: 100%; box-sizing: border-box;
    background: var(--surface2, var(--hover)); border: 1.5px solid var(--border); color: var(--text);
    border-radius: 6px; padding: 8px 10px; font-size: 13px; font-family: inherit; outline: none;
  }
  .suggest-wrap input:focus { border-color: var(--primary); }
  .suggest-list {
    position: fixed; z-index: 1000; width: max-content;
    background: var(--card); border: 1px solid var(--border); border-radius: 8px;
    box-shadow: 0 10px 28px rgba(0,0,0,.22); max-height: 200px; overflow-y: auto;
    padding: 4px; display: flex; flex-direction: column; gap: 1px;
  }
  .suggest-item {
    display: block; width: 100%; text-align: left; padding: 6px 10px; border-radius: 6px;
    background: none; border: none; color: var(--text); font-size: 13px; font-weight: 500; cursor: pointer;
    font-family: inherit; white-space: normal; word-break: break-word;
    -webkit-font-smoothing: antialiased; -moz-osx-font-smoothing: grayscale;
  }
  .suggest-item:hover, .suggest-item.highlighted { background: var(--hover); }
</style>
