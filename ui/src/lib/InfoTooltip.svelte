<script>
  // A small "?" affordance that reveals an explanatory tip on hover/focus —
  // used next to fields and section headings whose purpose or exact syntax
  // isn't self-evident from the label alone (match-type semantics,
  // templating syntax, what a mock type actually listens on, etc.).
  // Keyboard-accessible via focus/blur so it isn't hover-only.
  export let text = '';

  let show = false;

  // The bubble is centered on the icon via CSS's left:50%/translateX(-50%)
  // by default, which overflows off-screen — cut off exactly as reported —
  // whenever the icon sits near the left/right edge of the viewport (e.g.
  // the last field in a row). Runs once right after the bubble mounts
  // (behind the {#if}, so its real rendered position/width are known),
  // repositioning it to stay fully on-screen horizontally.
  //
  // Deliberately overrides `left` in pixels rather than adjusting
  // `transform`: this element's own `animation` (theme-menu-in, its
  // fade/scale-in) ends on a `transform: none` keyframe, and a CSS
  // animation's keyframe values ALWAYS win over an inline style for
  // whatever property they touch, for as long as the animation is
  // filling — so setting style.transform here would be silently
  // overridden back to `none` the instant the animation finishes, undoing
  // both this clamp AND the base centering. `left` isn't part of the
  // animation, so an inline override of it sticks. The little arrow
  // (positioned relative to the bubble's own box, not the icon) drifts
  // off-center from the icon when this kicks in — an acceptable trade-off
  // next to the text actually being readable.
  function positionBubble(node) {
    const wrap = node.parentElement;
    const wrapRect = wrap.getBoundingClientRect();
    const bubbleWidth = node.getBoundingClientRect().width;
    const margin = 8;

    let left = wrapRect.left + wrapRect.width / 2 - bubbleWidth / 2; // the default centered position, in viewport coordinates
    if (left + bubbleWidth > window.innerWidth - margin) {
      left = window.innerWidth - margin - bubbleWidth;
    }
    if (left < margin) {
      left = margin;
    }
    node.style.left = `${left - wrapRect.left}px`; // `left` is relative to .info-tooltip-wrap (position:relative)
  }

  // Positions once immediately (so there's no flash at the wrong spot) and
  // once more when the entrance animation actually finishes: the 0%
  // keyframe is scale(.96), slightly smaller than the bubble's true
  // rendered width, so a measurement taken at mount time (right as that
  // animation starts) under-measures bubbleWidth and leaves a few pixels
  // of residual overflow once it scales up to its real size.
  function clampToViewport(node) {
    positionBubble(node);
    node.addEventListener('animationend', () => positionBubble(node), { once: true });
  }
</script>

<span class="info-tooltip-wrap">
  <button
    type="button"
    class="info-tooltip-icon"
    on:mouseenter={() => (show = true)}
    on:mouseleave={() => (show = false)}
    on:focus={() => (show = true)}
    on:blur={() => (show = false)}
    aria-label="More info"
  >?</button>
  {#if show}
    <span class="info-tooltip-bubble" role="tooltip" use:clampToViewport>{text}</span>
  {/if}
</span>

<style>
  .info-tooltip-wrap { position: relative; display: inline-flex; margin-left: 5px; vertical-align: middle; }
  .info-tooltip-icon {
    width: 15px; height: 15px; border-radius: 50%; border: 1px solid var(--border);
    background: var(--surface2, var(--hover)); color: var(--muted); font-size: 10px; font-weight: 700;
    line-height: 1; display: inline-flex; align-items: center; justify-content: center;
    cursor: help; padding: 0; flex-shrink: 0; transition: border-color .15s, color .15s;
  }
  .info-tooltip-icon:hover, .info-tooltip-icon:focus-visible { border-color: var(--primary); color: var(--primary); }

  .info-tooltip-bubble {
    position: absolute; bottom: calc(100% + 8px); left: 50%; transform: translateX(-50%);
    background: var(--card); color: var(--text); border: 1px solid var(--border);
    font-size: 12px; font-weight: 400; text-transform: none; letter-spacing: normal;
    padding: 8px 10px; border-radius: 8px; width: max-content; max-width: 260px;
    box-shadow: 0 10px 28px rgba(0,0,0,.22); z-index: 80; line-height: 1.4; pointer-events: none;
    animation: theme-menu-in .15s ease both;
    /* white-space isn't reset anywhere above, so this inherited whatever
       ambient value happened to be set on whatever label/field wraps this
       icon — several call sites (e.g. .rule-required for the checkbox
       fields, .read-timeout-field .label-text) set white-space:nowrap on
       themselves for their OWN one-line text, which then leaked into this
       bubble too (position:absolute takes an element out of the layout
       flow, but not out of property inheritance) and made the tooltip's
       actual sentence-length text refuse to wrap, overflowing sideways
       past its own 260px max-width instead of staying inside it. Explicit
       here so the tooltip always wraps correctly regardless of whatever
       white-space its parent context happens to set.
    */
    white-space: normal;
  }
  .info-tooltip-bubble::after {
    content: ''; position: absolute; top: 100%; left: 50%; transform: translateX(-50%);
    width: 8px; height: 8px; background: var(--card); border-right: 1px solid var(--border);
    border-bottom: 1px solid var(--border); margin-top: -5px; rotate: 45deg;
  }
</style>
