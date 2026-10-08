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

  function stageView(k: string, value: string) {
    const label = STAGES.find(([x]) => x === k)![1]
    if (running === k) return { cls: 'running', text: `${label}中`, tip: '處理中' }
    switch (value) {
      case 'done':
        return { cls: 'done', text: label, tip: '已完成' }
      case 'outdated':
        return { cls: 'needs_align', text: `${label}需更新`, tip: '來源或去人聲方法有變動，需要重新去人聲' }
      case 'needs_render':
        return { cls: 'needs_render', text: '伴唱帶需重燒', tip: '只需重新燒錄（不重新對時）；已確認不受影響' }
      case 'needs_align':
        return { cls: 'needs_align', text: '伴唱帶需重新對時', tip: '歌詞、人聲、語言或對時方法有變動，會整首重新對時；已確認會失效' }
      case 'missing':
        return k === 'download'
          ? { cls: 'missing', text: '來源不見了', tip: '來源檔不見了，請重新下載或放回 inbox' }
          : { cls: 'missing', text: '缺歌詞', tip: '按「輸入歌詞」' }
      case 'no_lyrics':
        return { cls: 'pending', text: label, tip: '需要先有歌詞' }
      default:
        return { cls: 'pending', text: label, tip: '尚未處理' }
    }
  }

  // 已完成的階段合併成一個「✓ 3/4」（省空間），沒完成的照樣分開顯示
  let stageViews = $derived(STAGES.map(([k, label]) => ({ k, label, ...stageView(k, song.status[k] as string) })))
  let doneCount = $derived(stageViews.filter((v) => v.cls === 'done').length)
  let doneTip = $derived(stageViews.map((v) => `${v.label}：${stageState(v)}`).join('\n'))

  /** 提示裡一個階段的狀態（「伴唱帶需重燒」→「需重燒」，避免重複階段名稱）。 */
  function stageState(v: { cls: string; label: string; text: string; tip: string }) {
    if (v.cls === 'done') return '已完成'
    const rest = v.text.startsWith(v.label) ? v.text.slice(v.label.length) : v.text
    return rest && rest !== '中' ? rest : v.tip
  }

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
    karaoke === 'needs_render' ? '重新燒錄' : karaoke === 'needs_align' ? '重新對時並更新' : '製作伴唱帶',
  )

  const EXPORT_CHIP: Record<string, { text: string; tip: string }> = {
    exported: { text: '已匯出', tip: '' },
    pending: { text: '待匯出', tip: '已確認、還沒匯出（或成品更新了、改了名）：按上方「匯出」' },
    remove: { text: '待移除', tip: '取消確認了，但匯出資料夾裡還有舊檔：按上方「匯出」才會拿掉' },
  }

  let approval = $derived(song.approval)
  let approvalTip = $derived.by(() => {
    if (approval.status === 'approved') return `已確認成品沒問題（${fmtDateTime(approval.at)}）`
    const changed = approval.changed ?? []
    const what = changed.length
      ? `上次確認（${fmtDateTime(approval.at)}）之後改了第 ${changed.map((i) => i + 1).join('、')} 句`
      : `上次確認（${fmtDateTime(approval.at)}）之後重新對時了（歌詞沒改）`
    return `${what}，請看過再確認（點一下開始播放）`
  })

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
    menu.show(anchor, busy ? '處理中，請等目前的工作結束' : '重新處理（會排入佇列）', [
      { label: '重新去人聲', desc: '重新分離人聲與伴奏；之後伴唱帶需重新對時', disabled: busy, action: () => run(['separate'], { force: true }) },
      { label: '重新對時', desc: '歌詞不變、重新抓每個字的時間並重新燒錄（已確認會失效）', disabled: busy || !hasLyrics, action: () => run(['karaoke'], { realign: true }) },
      { label: '檢查對時', desc: '獨立聽寫比對，標出可能不準的句子', disabled: busy || !hasTiming, action: () => run(['check'], { force: true }) },
      {
        label: '全部重做',
        desc: '去人聲、對時、字幕、燒錄全部重來；會覆蓋手動修改過的字幕檔',
        danger: true,
        disabled: busy,
        action: () => {
          if (confirm(`全部重做「${song.title}」？\n手動修改過的字幕檔（.ass）會被覆蓋，已確認也會失效。`)) run(['separate', 'karaoke'], { force: true })
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
  <PickBox songs={[song]} title="勾選（批次操作）；按住 Shift 可連續勾選" />
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
        <ol class="stages">
          {#if doneCount}
            <li class="stage summary" style="--done: {doneCount / STAGES.length}" title={doneTip}><i></i>✓ {doneCount}/{STAGES.length}</li>
          {/if}
          {#each stageViews.filter((v) => v.cls !== 'done') as v (v.k)}
            <li class="stage {v.cls}" title={v.tip}><i></i>{v.text}</li>
          {/each}
        </ol>
        {#if approval.status}
          <button
            type="button"
            class="approval-chip"
            class:stale={approval.status !== 'approved'}
            title={approvalTip}
            onclick={() => ui.openStudio(song.id, { line: approval.changed?.[0] ?? null })}
          >
            <Icon name="check" size={12} />{approval.status === 'approved' ? '已確認' : '需重新確認'}
            {#if approval.status === 'stale' && approval.changed?.length}<span class="muted">（改了 {approval.changed.length} 句）</span>{/if}
          </button>
        {/if}
        {#if song.exported}
          {@const chip = EXPORT_CHIP[song.exported]}
          <span class="export-chip {song.exported}" title={song.exported === 'exported' ? `已匯出：${song.export}` : chip.tip}
            ><Icon name="export" size={12} />{chip.text}</span
          >
        {/if}
        {#if song.status.qa && (song.status.qa.wrong || song.status.qa.suspect)}
          {@const qa = song.status.qa}
          <button
            type="button"
            class="qa-chip"
            class:soft={!qa.wrong}
            title="{[qa.wrong ? `${qa.wrong} 句與獨立聽寫差 1.5 秒以上` : '', qa.suspect ? `${qa.suspect} 句待確認` : ''].filter(Boolean).join('、')}（點一下從第一句開始播放確認）"
            onclick={() => ui.openStudio(song.id, { jumpToQa: true })}
          >
            <Icon name="warn" size={13} />{qa.wrong ? `${qa.wrong} 句可能不準` : `${qa.suspect} 句待確認`}
          </button>
        {/if}
      </div>
      <div class="actions">
        {#if song.status.separate !== 'done'}
          <button type="button" class="run-btn" disabled={busy} title="排入佇列：把人聲和伴奏分開" onclick={() => run(['separate'])}
            ><Icon name="play" />去人聲</button
          >
        {/if}
        {#if karaoke !== 'done'}
          <button
            type="button"
            class="run-btn"
            disabled={busy || !hasLyrics}
            title={hasLyrics ? '排入佇列：對時、產生字幕並輸出伴唱帶' : '需要先輸入歌詞'}
            onclick={() => run(['karaoke'])}><Icon name="play" />{karaokeLabel}</button
          >
        {/if}
        {#if song.status.separate !== 'done' || karaoke !== 'done'}<span class="divider"></span>{/if}
        <button type="button" class="view-link" class:attention={!hasLyrics} onclick={() => ui.openEditor(song.id)}
          ><Icon name="lyrics" />{hasLyrics ? '歌詞' : '輸入歌詞'}</button
        >
        <button type="button" class="view-link" onclick={() => (ui.song = song.id)}><Icon name="tag" />資訊</button>
        <button type="button" class="view-link" title="複製這首歌在 NAS 上的資料夾路徑：{song.path}" onclick={reveal}
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
        title={song.media.length ? `播放${song.media[0].label}，同時預覽 / 編輯歌詞` : '還沒有可播放的影片'}
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
