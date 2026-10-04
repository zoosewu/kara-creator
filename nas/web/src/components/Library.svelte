<script lang="ts">
  import { api, enc } from '../lib/api'
  import { dnd, moveSongs } from '../lib/drag.svelte'
  import { menu, type MenuEntry } from '../lib/menu.svelte'
  import { store } from '../lib/state.svelte'
  import { ui } from '../lib/ui.svelte'
  import { STEP_NAMES } from '../lib/util'
  import FolderRow from './FolderRow.svelte'
  import Icon from './Icon.svelte'
  import SongRow from './SongRow.svelte'

  let root = $state<HTMLButtonElement>()
  let all = $derived([...store.songs.values()])
  let picked = $derived(all.filter((s) => store.selected.has(s.id)).length)
  let made = $derived(all.filter((s) => s.status.karaoke === 'done' || s.approval.status).length)
  let approved = $derived(all.filter((s) => s.approval.status === 'approved').length)
  let pendingExport = $derived(all.filter((s) => s.exported === 'pending' || s.exported === 'remove').length)
  let exporting = $state(false)

  // 批次動作：一般動作只處理還沒做完的（伺服器依狀態略過）；重新處理的動作強制重做。
  const BATCH: Record<string, { steps: string[]; extra: Record<string, unknown> }> = {
    separate: { steps: ['separate'], extra: { only_needed: true } },
    karaoke: { steps: ['karaoke'], extra: { only_needed: true } },
    check: { steps: ['check'], extra: { only_needed: true } },
    redoSeparate: { steps: ['separate'], extra: { force: true, only_needed: true } },
    realign: { steps: ['karaoke'], extra: { realign: true, only_needed: true } },
    recheck: { steps: ['check'], extra: { force: true, only_needed: true } },
    redoAll: { steps: ['separate', 'karaoke'], extra: { force: true, only_needed: true } },
  }

  async function runBatch(name: string) {
    const a = BATCH[name]
    const songs = store.pickedSongs()
    if (name === 'redoAll' && !confirm(`全部重做 ${songs.length} 首？\n手動修改過的字幕檔（.ass）會被覆蓋，已確認也會失效。`)) return
    const res = await store.runJob(
      songs.map((s) => s.id),
      a.steps,
      a.extra,
    )
    if (!res) return
    const notes = Object.entries(res.skipped).map(([r, n]) => `${r} ${n} 首`).join('、')
    const label = a.steps.map((s) => STEP_NAMES[s]).join(' → ')
    let msg = res.created.length ? `已排入 ${res.created.length} 首：${label}` + (notes ? `（略過：${notes}）` : '') : `沒有需要處理的歌（${notes}）`
    if (res.errors?.length) msg += `；${res.errors.length} 首失敗：${res.errors[0]}`
    store.notify(msg, Boolean(res.errors?.length))
  }

  async function batchApproval(on: boolean) {
    const songs = store.pickedSongs()
    const todo = on ? songs.filter((s) => s.status.karaoke === 'done' && s.approval.status !== 'approved') : songs.filter((s) => s.approval.status === 'approved')
    let done = 0
    for (const s of todo) {
      if (await store.attempt(() => api(`/songs/${enc(s.id)}/approval`, { method: 'PUT', body: { approved: on } }))) done++
    }
    const skipped = songs.length - todo.length
    const why = on ? '已確認或伴唱帶還沒做好 / 需要更新' : '原本就沒確認'
    store.notify(`${on ? '已標記確認' : '已取消確認'} ${done} 首` + (skipped ? `（略過 ${skipped} 首：${why}）` : ''))
  }

  function moveMenu(anchor: HTMLElement) {
    const songs = store.pickedSongs()
    const ids = songs.map((s) => s.id)
    const current = new Set(songs.map((s) => s.folder))
    const here = (id: string) => current.size === 1 && current.has(id)
    const entries: MenuEntry[] = [
      { label: '曲庫最上層', desc: here('') ? '都已經在這裡' : '', disabled: here(''), icon: 'folder', action: () => moveSongs(ids, '') },
    ]
    const walk = (parent: string, depth: number) => {
      for (const f of store.childFolders(parent)) {
        entries.push({ label: f.name, desc: here(f.id) ? '都已經在這裡' : '', disabled: here(f.id), icon: 'folder', indent: depth, action: () => moveSongs(ids, f.id) })
        walk(f.id, depth + 1)
      }
    }
    walk('', 1)
    menu.show(anchor, `把 ${ids.length} 首移到…`, entries)
  }

  function redoMenu(anchor: HTMLElement) {
    menu.show(anchor, '重新處理選取的歌（已完成的也會重做）', [
      { label: '重新去人聲', desc: '重新分離人聲與伴奏；之後伴唱帶需重新對時', action: () => runBatch('redoSeparate') },
      { label: '重新對時', desc: '歌詞不變、重新抓每個字的時間並重新燒錄（缺歌詞的略過；已確認會失效）', action: () => runBatch('realign') },
      { label: '重新檢查對時', desc: '已經檢查過的也重新聽寫比對（沒有伴唱帶的略過）', action: () => runBatch('recheck') },
      { label: '全部重做', desc: '去人聲、對時、字幕、燒錄全部重來；會覆蓋手動修改過的字幕檔', danger: true, action: () => runBatch('redoAll') },
    ])
  }

  async function doExport() {
    exporting = true
    const res = await store.run(() => api<{ added: string[]; removed: string[]; kept: number; skipped: string[] }>('/export', { method: 'POST' }))
    exporting = false
    if (!res) return
    const parts = [
      res.added.length ? `新增或更新 ${res.added.length} 首` : '',
      res.removed.length ? `移除 ${res.removed.length} 首` : '',
      res.kept ? `${res.kept} 首已是最新` : '',
    ].filter(Boolean)
    store.notify(`匯出完成：${parts.join('、') || '沒有已確認的伴唱帶'}` + (res.skipped.length ? `；${res.skipped.length} 首因為目的地已有同名的其他檔案而略過` : ''), res.skipped.length > 0)
  }
</script>

{#snippet level(parent: string, depth: number)}
  {#each store.childFolders(parent) as f (f.id)}
    <FolderRow folder={f} {depth} />
    {#if !store.collapsed.has(f.id)}{@render level(f.id, depth + 1)}{/if}
  {/each}
  {#each store.songsIn(parent) as s (s.id)}
    <SongRow song={s} {depth} />
  {/each}
{/snippet}

<section class="panel library">
  <div class="lib-head">
    <input
      type="checkbox"
      class="pick"
      title="全選 / 全部取消"
      aria-label="全選"
      disabled={!all.length}
      checked={all.length > 0 && picked === all.length}
      indeterminate={picked > 0 && picked < all.length}
      onclick={(e) => store.pick(all, (e.currentTarget as HTMLInputElement).checked)}
    />
    <button
      bind:this={root}
      type="button"
      class="lib-root {dnd.over?.key === 'root' ? dnd.over.cls : ''}"
      class:selected={store.folder === ''}
      title="選取最上層（新歌存入這裡）；也可以把歌曲或資料夾拖到這裡移回最上層"
      onclick={() => store.selectFolder('')}
      ondragover={(e) => dnd.overRow(e, root!, 'root', { kind: 'root' })}
      ondragleave={(e) => dnd.leave(e, root!, 'root')}
      ondrop={(e) => dnd.drop(e, root!, { kind: 'root' })}
    >
      曲庫 <span class="muted">{all.length ? `${all.length} 首` + (made ? ` · 已確認 ${approved} / ${made}` : '') : ''}</span>
    </button>
    <div class="lib-tools">
      <button type="button" class="view-link" onclick={() => (store.collapsed.clear(), store.remember())}>全部展開</button>
      <button type="button" class="view-link" onclick={() => (store.folders.forEach((f) => store.collapsed.add(f.id)), store.remember())}>全部收合</button>
      <button type="button" class="view-link" title="全域設定（套用到所有歌）" onclick={() => (ui.settings = true)}><Icon name="gear" />設定</button>
      <button type="button" class="btn small" title="在最上層新增資料夾" onclick={() => (ui.folder = { id: null, parent: '' })}><Icon name="plus" />新資料夾</button>
      <button
        type="button"
        class="btn small"
        class:primary={pendingExport > 0}
        disabled={exporting}
        title="把「已確認」的伴唱帶同步到匯出資料夾（只匯出已確認的，只在按這裡時同步；括號是待匯出 + 待移除的歌數）"
        onclick={doExport}><Icon name="export" />匯出{pendingExport ? `（${pendingExport}）` : ''}</button
      >
    </div>
  </div>
  {#if picked}
    <div class="batch-bar">
      <span class="batch-count">已選 {picked} 首</span>
      <div class="batch-actions">
        <button type="button" class="run-btn" title="只處理還沒去人聲的歌" onclick={() => runBatch('separate')}><Icon name="play" />去人聲</button>
        <button type="button" class="run-btn" title="只處理還沒完成或需要更新的伴唱帶；沒去過人聲的會先去人聲" onclick={() => runBatch('karaoke')}
          ><Icon name="play" />製作伴唱帶</button
        >
        <button type="button" class="run-btn" title="只檢查還沒檢查過、或對時有變動的歌" onclick={() => runBatch('check')}><Icon name="play" />檢查對時</button>
        <button type="button" class="view-link" aria-haspopup="menu" title="重新處理（已完成的也會重做）" onclick={(e) => redoMenu(e.currentTarget)}
          ><Icon name="more" />重新處理</button
        >
        <span class="divider"></span>
        <button type="button" class="view-link" title="把選取的歌標成「已確認成品沒問題」（只對已完成的伴唱帶有效）" onclick={() => batchApproval(true)}
          ><Icon name="check" />標記已確認</button
        >
        <button type="button" class="view-link" onclick={() => batchApproval(false)}>取消確認</button>
        <button type="button" class="view-link" aria-haspopup="menu" title="移到其他資料夾；也可以直接拖曳任一首勾選的歌" onclick={(e) => moveMenu(e.currentTarget)}
          ><Icon name="folder" />移動到…</button
        >
        <button type="button" class="view-link" onclick={() => store.selected.clear()}>取消選取</button>
      </div>
    </div>
  {/if}
  <div class="items" role="tree">
    {#if store.loaded && !all.length && !store.folders.length}
      <div class="empty">還沒有歌曲。在上方貼上網址，按「製作伴唱帶」開始；或把影音檔放進 NAS 的 inbox 資料夾。</div>
    {:else}
      {@render level('', 0)}
    {/if}
  </div>
</section>
