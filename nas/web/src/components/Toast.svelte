<script lang="ts">
  import { store } from '../lib/state.svelte'
</script>

{#if store.toast}
  <div class="toast" class:error={store.toast.error}>{store.toast.text}</div>
{/if}
{#if !store.connected}
  <div class="offline-banner">與 NAS 的連線中斷，重新連線中…</div>
{/if}
