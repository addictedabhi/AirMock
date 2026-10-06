<script>
  import { toast } from './toast.js';
</script>

{#if $toast}
  {#key $toast.id}
    <div class="toast" class:show={$toast.visible} class:ok={$toast.kind === 'ok'} class:warn={$toast.kind === 'warn'} class:err={$toast.kind === 'err'}>
      <span class="toast-icon">{$toast.kind === 'err' ? '✕' : $toast.kind === 'warn' ? '!' : '✓'}</span>
      <span class="toast-message">{$toast.message}</span>
      {#if $toast.visible}<div class="toast-bar"><div class="toast-bar-fill"></div></div>{/if}
    </div>
  {/key}
{/if}

<style>
  .toast { display: flex; align-items: center; gap: 10px; overflow: hidden; }
  .toast-icon {
    flex-shrink: 0; width: 18px; height: 18px; border-radius: 50%;
    display: inline-flex; align-items: center; justify-content: center;
    font-size: 11px; font-weight: 700;
  }
  .toast.ok .toast-icon { color: var(--success); background: rgba(22,163,74,.15); }
  .toast.warn .toast-icon { color: #b45309; background: rgba(245,158,11,.18); }
  .toast.err .toast-icon { color: var(--error); background: rgba(220,38,38,.15); }
  .toast-message { flex: 1; min-width: 0; }
  .toast-bar {
    position: absolute; left: 0; bottom: 0; width: 100%; height: 2px;
    background: transparent;
  }
  .toast-bar-fill {
    height: 100%; background: currentColor; opacity: .4;
    animation: toast-countdown 2.6s linear forwards;
  }
  @keyframes toast-countdown {
    from { width: 100%; }
    to { width: 0%; }
  }
</style>
