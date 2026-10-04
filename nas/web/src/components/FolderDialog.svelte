<script lang="ts">
  import { onMount } from 'svelte'
  import { api } from '../lib/api'
  import { store } from '../lib/state.svelte'
  import { ui } from '../lib/ui.svelte'
  import FolderSelect from './FolderSelect.svelte'
  import Icon from './Icon.svelte'

  let { target }: { target: { id: string | null; parent: string } } = $props()
  // 對話框每次開啟時重新建立：取初始值是刻意的
  // svelte-ignore state_referenced_locally
  const folder = target.id ? store.folderById(target.id) : undefined
  let name = $state(folder?.name ?? '')
  // svelte-ignore state_referenced_locally
  let parent = $state(target.parent)
  let input = $state<HTMLInputElement>()
  onMount(() => input?.focus())

  const close = () => (ui.folder = null)

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    const ok = folder
      ? await store.attempt(() => api(`/folders/${folder.id}`, { method: 'PATCH', body: { name: name.trim(), parent } }), '已更新資料夾')
      : await store.attempt(() => api('/folders', { method: 'POST', body: { name: name.trim(), parent } }), '已新增資料夾')
    if (!ok) return
    if (parent) store.collapsed.delete(parent)
    store.remember()
    close()
  }

  async function remove() {
    if (!folder || !confirm(`刪除資料夾「${folder.name}」？\n裡面的歌曲與子資料夾會移到上一層，不會刪除任何檔案。`)) return
    if (!(await store.attempt(() => api(`/folders/${folder.id}`, { method: 'DELETE' }), '已刪除資料夾'))) return
    if (store.folder && store.pathOf(store.folder).includes(folder.id)) store.selectFolder(folder.parent)
    close()
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && close()} />

<div class="overlay center" role="presentation" onmousedown={(e) => e.target === e.currentTarget && close()}>
  <!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role -->
  <form class="dialog" role="dialog" aria-modal="true" aria-labelledby="folder-heading" onsubmit={submit}>
    <header class="dialog-head">
      <h2 id="folder-heading">{folder ? '編輯資料夾' : '新增資料夾'}</h2>
      <button type="button" class="icon-btn" aria-label="關閉" onclick={close}><Icon name="close" size={18} /></button>
    </header>
    <div class="dialog-body">
      <label class="field">名稱<input bind:this={input} bind:value={name} required autocomplete="off" /></label>
      <label class="field">位置<FolderSelect bind:value={parent} exclude={folder?.id ?? ''} /></label>
      <p class="hint">資料夾只影響曲庫整理和匯出資料夾，不會搬動處理用的檔案。也可以直接拖曳資料夾到其他位置，或拖曳調整順序。</p>
    </div>
    <footer class="dialog-foot">
      {#if folder}<button type="button" class="btn danger-text" onclick={remove}>刪除資料夾</button>{/if}
      <span class="grow"></span>
      <button type="button" class="btn" onclick={close}>取消</button>
      <button type="submit" class="btn primary">儲存</button>
    </footer>
  </form>
</div>
