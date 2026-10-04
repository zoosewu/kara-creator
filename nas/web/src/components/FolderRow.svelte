<script lang="ts">
  import type { FolderView } from '../lib/api'
  import { api } from '../lib/api'
  import { dnd } from '../lib/drag.svelte'
  import { store } from '../lib/state.svelte'
  import { ui } from '../lib/ui.svelte'
  import Icon from './Icon.svelte'
  import InlineEdit from './InlineEdit.svelte'
  import PickBox from './PickBox.svelte'

  let { folder, depth }: { folder: FolderView; depth: number } = $props()
  let editing = $state(false)
  let row = $state<HTMLDivElement>()
  let open = $derived(!store.collapsed.has(folder.id))
  let songs = $derived(store.songsIn(folder.id).length)
  let subs = $derived(store.childFolders(folder.id).length)
  let counts = $derived([songs ? `${songs} 首` : '', subs ? `${subs} 個資料夾` : ''].filter(Boolean).join(' · ') || '空的')
  let key = $derived('f:' + folder.id)
  const info = $derived({ kind: 'folder' as const, id: folder.id, parent: folder.parent })
</script>

<div
  bind:this={row}
  class="folder-row tree-row {dnd.over?.key === key ? dnd.over.cls : ''}"
  class:selected={store.folder === folder.id}
  class:dragging-source={dnd.isSource('folder', folder.id)}
  style="--depth:{depth}"
  title="點一下選取（新歌存入這裡）；可拖曳到其他資料夾"
  role="treeitem"
  aria-selected={store.folder === folder.id}
  aria-expanded={open}
  tabindex="0"
  draggable={!editing}
  onclick={() => store.selectFolder(folder.id)}
  onkeydown={(e) => e.key === 'Enter' && store.selectFolder(folder.id)}
  ondragstart={(e) => dnd.start(e, 'folder', folder.id, folder.parent)}
  ondragend={() => dnd.end()}
  ondragover={(e) => dnd.overRow(e, row!, key, info)}
  ondragleave={(e) => dnd.leave(e, row!, key)}
  ondrop={(e) => dnd.drop(e, row!, info)}
>
  <button
    type="button"
    class="caret"
    class:open
    class:leaf={!songs && !subs}
    aria-label={open ? '收合' : '展開'}
    onclick={(e) => {
      e.stopPropagation()
      store.toggleFolder(folder.id)
    }}><Icon name="caret" /></button
  >
  <PickBox songs={store.songsUnder(folder.id)} title="勾選「{folder.name}」裡的所有歌曲（含子資料夾）" />
  <svg class="folder-icon" aria-hidden="true"><use href="#i-folder" /></svg>
  <span class="title-line">
    {#if editing}
      <InlineEdit
        value={folder.name}
        placeholder="資料夾名稱"
        ondone={() => (editing = false)}
        onsave={(name) => (name ? api(`/folders/${folder.id}`, { method: 'PATCH', body: { name } }) : Promise.resolve())}
      />
    {:else}
      <span class="folder-name">{folder.name}</span>
      <button
        type="button"
        class="edit-btn"
        title="重新命名"
        aria-label="重新命名"
        onclick={(e) => {
          e.stopPropagation()
          editing = true
        }}><Icon name="edit" /></button
      >
    {/if}
  </span>
  <span class="folder-meta grow">{counts}</span>
  <div class="row-tools">
    <button
      type="button"
      class="view-link"
      title="在這個資料夾裡新增子資料夾"
      onclick={(e) => {
        e.stopPropagation()
        ui.folder = { id: null, parent: folder.id }
      }}><Icon name="plus" />子資料夾</button
    >
    <button
      type="button"
      class="view-link"
      onclick={(e) => {
        e.stopPropagation()
        ui.folder = { id: folder.id, parent: folder.parent }
      }}><Icon name="tag" />編輯</button
    >
  </div>
</div>
