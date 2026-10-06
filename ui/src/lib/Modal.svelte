<script>
  export let title = '';
  export let onClose = () => {};
  export let width = '560px';

  function onKeydown(e) {
    if (e.key === 'Escape') onClose();
  }

  // Renders the backdrop directly under <body> instead of wherever the
  // modal was mounted — an ancestor with any animated `transform` (e.g.
  // App.svelte's .page-enter page-transition wrapper) creates a new
  // containing block for `position: fixed`, which otherwise traps this
  // backdrop inside the scrolling page content instead of the viewport.
  function portal(node) {
    document.body.appendChild(node);
    return {
      destroy() {
        node.remove();
      },
    };
  }
</script>

<svelte:window on:keydown={onKeydown} />

<!-- svelte-ignore a11y_click_events_have_key_events -->
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<div class="modal-backdrop" use:portal on:click={onClose} role="presentation">
  <!-- svelte-ignore a11y_click_events_have_key_events -->
  <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
  <div class="modal-card" style="width: {width}" on:click|stopPropagation role="dialog" tabindex="-1" aria-modal="true" aria-label={title}>
    <div class="modal-header">
      <h3>{title}</h3>
      <button class="modal-close" on:click={onClose} title="Close">×</button>
    </div>
    <div class="modal-body">
      <slot />
    </div>
  </div>
</div>

<style>
  .modal-backdrop {
    position: fixed; inset: 0; background: rgba(0,0,0,.5);
    display: flex; align-items: center; justify-content: center; z-index: 1000;
    animation: modal-backdrop-in .18s ease both;
  }
  .modal-card {
    background: var(--card); border: 1px solid var(--border); border-radius: 14px;
    box-shadow: 0 24px 64px rgba(0,0,0,.35); max-width: 92vw; max-height: 86vh;
    display: flex; flex-direction: column; overflow: hidden;
    animation: modal-card-in .22s cubic-bezier(.22,1,.36,1) both;
  }
  .modal-header {
    display: flex; align-items: center; justify-content: space-between;
    padding: 16px 20px; border-bottom: 1px solid var(--border); flex-shrink: 0;
  }
  .modal-header h3 { margin: 0; font-size: 15px; }
  .modal-close { background: none; border: none; font-size: 18px; color: var(--muted); cursor: pointer; padding: 2px 8px; border-radius: 6px; }
  .modal-close:hover { color: var(--error); background: var(--hover); }
  .modal-body { padding: 20px; overflow-y: auto; }

  @keyframes modal-backdrop-in { from { opacity: 0; } to { opacity: 1; } }
  @keyframes modal-card-in {
    from { opacity: 0; transform: translateY(12px) scale(.97); }
    to { opacity: 1; transform: none; }
  }
</style>
