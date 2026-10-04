<script lang="ts">
  // 資料夾下拉選單（依曲庫結構縮排）；exclude 的資料夾與它的子資料夾不列出（不能移到自己裡面）。
  import { store } from '../lib/state.svelte'

  let { value = $bindable(''), exclude = '' }: { value?: string; exclude?: string } = $props()

  let options = $derived.by(() => {
    const out: { id: string; label: string }[] = []
    const walk = (parent: string, depth: number) => {
      for (const f of store.childFolders(parent)) {
        if (exclude && f.id === exclude) continue
        out.push({ id: f.id, label: '　'.repeat(depth) + f.name })
        walk(f.id, depth + 1)
      }
    }
    walk('', 0)
    return out
  })
</script>

<select bind:value>
  <option value="">曲庫最上層</option>
  {#each options as o}<option value={o.id}>{o.label}</option>{/each}
</select>
