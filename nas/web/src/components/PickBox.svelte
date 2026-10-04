<script lang="ts">
  import type { SongView } from '../lib/api'
  import { store } from '../lib/state.svelte'

  let { songs, title }: { songs: SongView[]; title: string } = $props()
  let picked = $derived(songs.filter((s) => store.selected.has(s.id)).length)
</script>

<input
  type="checkbox"
  class="pick"
  {title}
  aria-label={title}
  disabled={!songs.length}
  checked={songs.length > 0 && picked === songs.length}
  indeterminate={picked > 0 && picked < songs.length}
  onclick={(e) => {
    e.stopPropagation()
    store.pick(songs, (e.currentTarget as HTMLInputElement).checked, e.shiftKey)
  }}
  onmousedown={(e) => e.stopPropagation()}
  ondblclick={(e) => e.stopPropagation()}
/>
