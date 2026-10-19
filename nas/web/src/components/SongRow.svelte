<script lang="ts">
  import type { SongView } from '../lib/api'
  import { api, enc } from '../lib/api'
  import { dnd } from '../lib/drag.svelte'
  import { menu } from '../lib/menu.svelte'
  import { store } from '../lib/state.svelte'
  import { ui } from '../lib/ui.svelte'
  import { copyText, fmtDateTime, fmtDuration, LANGUAGE_NAMES } from '../lib/util'
  import Icon from './Icon.svelte'
  import InlineEdit from './InlineEdit.svelte'
  import PickBox from './PickBox.svelte'

  let { song, depth }: { song: SongView; depth: number } = $props()
  let editing = $state(false)
  let row = $state<HTMLElement>()
  let key = $derived('s:' + song.id)
  const info = $derived({ kind: 'song' as const, id: song.id, parent: song.folder })

  const STAGES: [keyof SongView['status'], string][] = [
    ['download', '來源'],
    ['separate', '去人聲'],
    ['lyrics', '歌詞'],
    ['karaoke', '伴唱帶'],
  ]
  const STAGE_OF: Record<string, string> = { 下載: 'download', 去人聲: 'separate', 製作伴唱帶: 'karaoke' }

  let job = $derived(song.job)
  let busy = $derived(Boolean(job))
  let running = $derived(job?.status === 'running' ? STAGE_OF[job.stage] : null)
  let hasLyrics = $derived(song.status.lyrics === 'done')
  let karaoke = $derived(song.status.karaoke)
  let approval = $derived(song.approval)

  function stageView(k: string, value: string) {
    const label = STAGES.find(([x]) => x === k)![1]
    if (running === k) return { cls: 'running', text: `${label}中`, tip: '處理中' }
    switch (value) {
      case 'done':
        return { cls: 'done', text: label, tip: '已完成' }
      case 'outdated':
        return { cls: 'needs_align', text: `${label}需更新`, tip: '需要重新去人聲' }
      case 'needs_render':
        return { cls: 'needs_render', text: '伴唱帶需重燒', tip: '只重新燒錄；已確認不受影響' }
      case 'needs_align':
        if (song.align_patch)
          return song.align_patch.realign
            ? { cls: 'needs_align', text: `伴唱帶需重對 ${song.align_patch.realign} 句`, tip: '只重對改過的句子；已確認會失效' }
            : { cls: 'needs_align', text: '伴唱帶需更新', tip: '刪了句子，不必重新對時；已確認會失效' }
        return { cls: 'needs_align', text: '伴唱帶需重新對時', tip: '整首重新對時，手動調整會被取代；已確認會失效' }
      case 'missing':
        return k === 'download'
          ? { cls: 'missing', text: '來源不見了', tip: '請重新下載或放回 inbox' }
          : { cls: 'missing', text: '缺歌詞', tip: '按「輸入歌詞」' }
      case 'no_lyrics':
        return { cls: 'pending', text: label, tip: '需要先有歌詞' }
      default:
        return { cls: 'pending', text: label, tip: '尚未處理' }
    }
  }

  type Step = { label: string; cls: string; state: string; detail?: string }

  /** 提示裡一個階段的狀態（「伴唱帶需重燒」→「需重燒」，避免重複階段名稱）。 */
  function stageStep(k: string, label: string): Step {
    const v = stageView(k, song.status[k as keyof SongView['status']] as string)
    if (v.cls === 'done') return { label, cls: 'done', state: '已完成' }
    const rest = v.text.startsWith(label) ? v.text.slice(label.length) : v.text
    return rest && rest !== '中' ? { label, cls: v.cls, state: rest, detail: v.tip } : { label, cls: v.cls, state: v.tip }
  }

  const EXPORT_STEP: Record<string, Omit<Step, 'label'>> = {
    exported: { cls: 'done', state: '已匯出' },
    pending: { cls: 'needs_render', state: '待匯出', detail: '按上方「匯出」' },
    remove: { cls: 'needs_align', state: '待移除', detail: '按上方「匯出」移除舊檔' },
  }

  // 全部的狀態合併成一個「✓ 4/6」：滑上去看每個步驟，點一下播放（有問題的話跳到第一個有問題的句子）
  let steps = $derived.by((): Step[] => {
    const list = STAGES.map(([k, label]) => stageStep(k, label))
    const at = approval.at ? `（${fmtDateTime(approval.at)}）` : ''
    if (approval.status === 'approved') list.push({ label: '確認', cls: 'done', state: '已確認' + at })
    else if (approval.status === 'stale')
      list.push({
        label: '確認',
        cls: 'needs_align',
        state: '需重新確認',
        detail: approval.changed?.length ? `改了第 ${approval.changed.map((i) => i + 1).join('、')} 句` : undefined,
      })
    else list.push({ label: '確認', cls: 'pending', state: karaoke === 'done' ? '還沒確認' : '伴唱帶做好後確認' })
    const ex = song.exported ? EXPORT_STEP[song.exported] : null
    list.push({ label: '匯出', ...(ex ?? { cls: 'pending', state: '確認後按上方「匯出」' }), ...(song.exported === 'exported' ? { detail: song.export } : {}) })
    return list
  })
  let doneCount = $derived(steps.filter((v) => v.cls === 'done').length)
  let qa = $derived(song.status.qa && (song.status.qa.wrong || song.status.qa.suspect) ? song.status.qa : null)
  let qaText = $derived(qa ? [qa.wrong ? `${qa.wrong} 句可能不準` : '', qa.suspect ? `${qa.suspect} 句待確認` : ''].filter(Boolean).join('、') : '')
  let problem = $derived(Boolean(qa?.wrong) || steps.some((v) => ['missing', 'needs_align'].includes(v.cls)))
  let jump = $derived(qa ? { jumpToQa: true } : approval.status === 'stale' ? { line: approval.changed?.[0] ?? null } : {})

  // 觸控裝置沒有「滑上去」：點一下打開提示（提示裡有「播放」），點旁邊或捲動就收起來
  const touch = matchMedia('(hover: none)').matches
  let tip = $state<{ left: number; top: number } | null>(null)
  function showTip(e: Event) {
    const r = (e.currentTarget as HTMLElement).getBoundingClientRect()
    tip = { left: Math.max(8, Math.min(r.left, window.innerWidth - 340)), top: r.bottom + 6 }
  }
  function tapTag(e: MouseEvent) {
    if (!touch) return ui.openStudio(song.id, jump)
    if (tip) tip = null
    else showTip(e)
  }
  $effect(() => {
    if (!tip || !touch) return
    const close = (e: Event) => {
      if (!(e.target as HTMLElement).closest?.('.status-tip, .status-tag')) tip = null
    }
    const hide = () => (tip = null)
    window.addEventListener('pointerdown', close)
    window.addEventListener('scroll', hide, true)
    return () => {
      window.removeEventListener('pointerdown', close)
      window.removeEventListener('scroll', hide, true)
    }
  })

  let meta = $derived(
    [
      song.title !== song.source.title ? song.source.title : '',
      fmtDuration(song.source.duration),
      song.source.mode === 'audio' ? '音訊' : '影片',
      LANGUAGE_NAMES[song.language] ?? '',
    ].filter(Boolean),
  )

  let badge = $derived.by(() => {
    if (!job) return ''
    const pct = job.progress != null ? ` ${Math.round(job.progress * 100)}%` : ''
    return job.status === 'queued' ? '排隊中' : job.cancelling ? '取消中' : `${job.stage || '處理中'}${pct}`
  })

  let karaokeLabel = $derived(
    karaoke === 'needs_render' ? '重新燒錄' : karaoke === 'needs_align' ? (song.align_patch ? '更新伴唱帶' : '重新對時並更新') : '製作伴唱帶',
  )

  function saveName(value: string) {
    let title = value
    let artist: string | undefined
    if (!value) {
      title = ''
      artist = ''
    } else {
      const cut = value.indexOf(' - ')
      if (cut > 0) {
        artist = value.slice(0, cut).trim()
        title = value.slice(cut + 3).trim()
      }
    }
    return api(`/songs/${enc(song.id)}`, { method: 'PATCH', body: artist === undefined ? { title } : { title, artist } })
  }

  async function run(steps: string[], extra: Record<string, unknown> = {}) {
    const res = await store.runJob([song.id], steps, extra)
    if (res?.created.length) {
      const mode = extra.force ? '（強制重做）' : extra.realign ? '（重新對時）' : ''
      store.notify(`已排入佇列：${steps.map((s) => ({ separate: '去人聲', karaoke: '製作伴唱帶', check: '檢查對時' })[s]).join(' → ')}${mode}`)
    }
  }

  function redoMenu(anchor: HTMLElement) {
    const hasTiming = ['done', 'needs_render', 'needs_align'].includes(karaoke)
    const phone = matchMedia('(max-width: 640px)').matches
    menu.show(anchor, busy ? '處理中，請等目前的工作結束' : '重新處理', [
      ...(phone ? [{ label: '複製檔案位置', desc: song.path, action: reveal }] : []),
      { label: '重新去人聲', desc: '之後需重新對時', disabled: busy, action: () => run(['separate'], { force: true }) },
      { label: '重新對時', desc: '重新抓每個字的時間；已確認會失效', disabled: busy || !hasLyrics, action: () => run(['karaoke'], { realign: true }) },
      { label: '檢查對時', desc: '標出可能不準的句子', disabled: busy || !hasTiming, action: () => run(['check'], { force: true }) },
      {
        label: '全部重做',
        desc: '全部重來，會覆蓋手動改過的字幕檔',
        danger: true,
        disabled: busy,
        action: () => {
          if (confirm(`全部重做「${song.title}」？\n手動改過的字幕檔會被覆蓋，已確認會失效。`)) run(['separate', 'karaoke'], { force: true })
        },
      },
    ])
  }

  function previewMenu(anchor: HTMLElement) {
    menu.show(
      anchor,
      '播放其他版本',
      song.media.map((m, i) => ({
        label: m.label + (i === 0 ? '（預設）' : ''),
        desc: m.hint,
        action: () => ui.openStudio(song.id, { kind: m.kind }),
      })),
    )
  }

  async function reveal() {
    await copyText(song.path)
    store.notify(`已複製這首歌的資料夾路徑：${song.path}`)
  }
</script>

<article
  bind:this={row}
  class="song tree-row {dnd.over?.key === key ? dnd.over.cls : ''}"
  class:picked={store.selected.has(song.id)}
  class:dragging-source={dnd.isSource('song', song.id)}
  style="--depth:{depth}"
  draggable={!editing}
  ondragstart={(e) => dnd.start(e, 'song', song.id, song.folder)}
  ondragend={() => dnd.end()}
  ondragover={(e) => dnd.overRow(e, row!, key, info)}
  ondragleave={(e) => dnd.leave(e, row!, key)}
  ondrop={(e) => dnd.drop(e, row!, info)}
>
  <svg class="grip" aria-hidden="true"><use href="#i-grip" /></svg>
  <PickBox songs={[song]} title="勾選；Shift 可連續勾選" />
  <div class="song-main">
    <div class="song-top">
      <div class="grow">
        <div class="title-line">
          {#if editing}
            <InlineEdit
              value={song.artist ? `${song.artist} - ${song.title}` : song.title}
              placeholder="歌手 - 歌名"
              hint="Enter 儲存 · Esc 取消 · 清空還原自動"
              ondone={() => (editing = false)}
              onsave={saveName}
            />
          {:else}
            <div class="song-title ellipsis" title="{song.title}&#10;輸出：{song.export}">
              {song.title}{#if song.artist}<span class="artist">{song.artist}</span>{/if}
            </div>
            <button type="button" class="edit-btn" title="編輯歌手與歌名" aria-label="編輯歌手與歌名" onclick={() => (editing = true)}
              ><Icon name="edit" /></button
            >
          {/if}
        </div>
        <div class="song-meta ellipsis" title={meta.join(' · ')}>{meta.join(' · ')}</div>
      </div>
      {#if badge}<span class="badge">{badge}</span>{/if}
    </div>
    <div class="song-bottom">
      <div class="stages-wrap">
        <button
          type="button"
          class="status-tag"
          class:problem
          style="--done: {doneCount / steps.length}"
          disabled={!song.media.length}
          aria-describedby={tip ? `tip-${song.id}` : undefined}
          aria-label="狀態 {doneCount}/{steps.length}：{steps.map((v) => `${v.label}${v.state}`).join('、')}"
          onmouseenter={(e) => !touch && showTip(e)}
          onmouseleave={() => !touch && (tip = null)}
          onfocus={(e) => !touch && showTip(e)}
          onblur={() => !touch && (tip = null)}
          onclick={tapTag}
        >
          {#if running}<i class="pulse"></i>{:else}<i></i>{/if}✓ {doneCount}/{steps.length}
        </button>
        {#if tip}
          <div class="status-tip" class:touch id="tip-{song.id}" role="tooltip" style="left:{tip.left}px;top:{tip.top}px">
            {#each steps as v (v.label)}
              <div class="status-step stage {v.cls}">
                <i></i><span class="status-label">{v.label}</span><span class="status-state">{v.state}{#if v.detail}<small>{v.detail}</small>{/if}</span>
              </div>
            {/each}
            {#if qa}
              <div class="status-step stage {qa.wrong ? 'needs_align' : 'pending'}">
                <i></i><span class="status-label">對時檢查</span><span class="status-state">{qaText}</span>
              </div>
            {/if}
            {#if song.media.length && touch}
              <button type="button" class="btn small primary status-play" onclick={() => ((tip = null), ui.openStudio(song.id, jump))}
                ><Icon name="play" />播放{qa ? `（從第一句${qa.wrong ? '可能不準' : '待確認'}的開始）` : ''}</button
              >
            {:else if song.media.length}
              <div class="status-foot">點一下播放{qa ? `，從第一句${qa.wrong ? '可能不準' : '待確認'}的開始` : approval.status === 'stale' && approval.changed?.length ? '，從改過的句子開始' : ''}</div>
            {/if}
          </div>
        {/if}
      </div>
      <div class="actions">
        {#if song.status.separate !== 'done'}
          <button type="button" class="run-btn" disabled={busy} title="分開人聲與伴奏" onclick={() => run(['separate'])}
            ><Icon name="play" />去人聲</button
          >
        {/if}
        {#if karaoke !== 'done'}
          <button
            type="button"
            class="run-btn"
            disabled={busy || !hasLyrics}
            title={hasLyrics ? '對時並做出伴唱帶' : '需要先輸入歌詞'}
            onclick={() => run(['karaoke'])}><Icon name="play" />{karaokeLabel}</button
          >
        {/if}
        {#if song.status.separate !== 'done' || karaoke !== 'done'}<span class="divider"></span>{/if}
        <button type="button" class="view-link" class:attention={!hasLyrics} onclick={() => ui.openEditor(song.id)}
          ><Icon name="lyrics" />{hasLyrics ? '歌詞' : '輸入歌詞'}</button
        >
        <button type="button" class="view-link" onclick={() => (ui.song = song.id)}><Icon name="tag" />資訊</button>
        <button type="button" class="view-link hide-phone" title="複製這首歌在 NAS 上的資料夾路徑：{song.path}" onclick={reveal}
          ><Icon name="folder" />檔案位置</button
        >
        <button
          type="button"
          class="view-link"
          title="重新處理…"
          aria-haspopup="menu"
          aria-label="重新處理"
          onclick={(e) => redoMenu(e.currentTarget)}><Icon name="more" /></button
        >
      </div>
    </div>
  </div>
  <div class="preview">
    <div class="preview-split">
      <button
        type="button"
        class="preview-main"
        disabled={!song.media.length}
        title={song.media.length ? `播放${song.media[0].label}` : '還沒有可播放的影片'}
        aria-label={song.media.length ? `播放${song.media[0].label}` : '播放'}
        onclick={() => ui.openStudio(song.id)}><Icon name="play" /></button
      >
      <button
        type="button"
        class="preview-more"
        disabled={song.media.length < 2}
        title="播放其他版本"
        aria-label="播放其他版本"
        aria-haspopup="menu"
        onclick={(e) => previewMenu(e.currentTarget)}><Icon name="caret" /></button
      >
    </div>
    <span class="preview-label">{song.media[0]?.label ?? '尚無影片'}</span>
  </div>
</article>
