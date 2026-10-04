<script lang="ts">
  // 播放畫面：左邊播放影片，右邊歌詞清單，可以邊聽邊改歌詞、演唱者、時間、讓 AI 重對（v1 的 studio）。
  // 播放沒燒字幕的版本時，網頁即時畫逐字填色的字幕（含假名、翻譯、演唱者顏色），調整後立刻看得到；
  // 已燒字幕的成品不另外疊字幕，一調整就自動切到伴奏預覽。
  import { onMount, tick } from 'svelte'
  import { api, enc, type EditorLine, type JobSummary, type LyricsViews, type QAView, type TimingView } from '../lib/api'
  import { menu } from '../lib/menu.svelte'
  import { store } from '../lib/state.svelte'
  import { ui, type StudioOpts } from '../lib/ui.svelte'
  import { fmtClock, nextSinger, sameText } from '../lib/util'
  import Icon from './Icon.svelte'

  let props: { id: string; opts: StudioOpts } = $props()
  // 一個元件只對應一首歌（App 用 {#key}）：關閉時外面會先把 ui.* 清空，props 也會跟著變空，所以開啟時就記下來
  // svelte-ignore state_referenced_locally
  const { id, opts } = props
  let song = $derived(store.songs.get(id)!)

  const BURNED = new Set(['karaoke', 'karaoke_original'])
  const SUB_PREVIEW = 4 // 還沒開始唱時，提前幾秒先顯示第一句
  const SUB_LONG_GAP = 8 // 兩句之間空這麼久算間奏：唱完 SUB_LINGER 秒後先收起來
  const SUB_LINGER = 2
  const BASE_RATIO = 0.075

  let lines = $state<EditorLine[]>([])
  let language = $state<string | null>(null)
  let timing = $state<TimingView['lines']>([])
  let qa = $state(new Map<number, QAView['lines'][number]>())
  let media = $state<SongView_media | null>(null)
  type SongView_media = (typeof song.media)[number]
  let selected = $state(0)
  let playing = $state(-1)
  let follow = $state(true)
  let retimed = $state(false)
  let textChanged = $state(false)
  let editingText = $state<number | null>(null)
  let pendingJob = $state<{ id: string; index: number; mode: string; count: number } | null>(null)
  let statusText = $state('')
  let rate = $state(1)
  let paused = $state(true)
  let now = $state(0)
  let duration = $state(1)
  let seeking = false

  let video = $state<HTMLVideoElement>()
  let frame = $state<HTMLDivElement>()
  let subBox = $state<HTMLDivElement>()
  let rows: HTMLElement[] = $state([])
  let raf = 0
  let subFont = $state(16)
  let transFont = $state(10)
  let transTop = $state(0)

  // 第幾句歌詞（依序）在 lines 裡的位置
  let lyricIdx = $derived(lines.map((l, i) => (l.kind === 'lyric' ? i : -1)).filter((i) => i >= 0))
  let changed = $derived(new Set(song?.approval.status === 'stale' ? (song.approval.changed ?? []) : []))
  let burned = $derived(media ? BURNED.has(media.kind) : false)

  function timeOf(i: number) {
    const t = timing[i]
    if (!t) return null
    const l = lines[lyricIdx[i]]
    return { ...t, mismatch: l ? !sameText(t.text, l.text) : true }
  }
  function qaOf(i: number) {
    const c = qa.get(i)
    const l = lines[lyricIdx[i]]
    return c && l && sameText(c.text, l.text) ? c : null
  }
  let qaIndexes = $derived(lyricIdx.map((_, i) => (qaOf(i) ? i : -1)).filter((i) => i >= 0))
  let mismatch = $derived(lyricIdx.length !== timing.length || lyricIdx.some((_, i) => timeOf(i)?.mismatch))

  onMount(() => {
    load()
    const off = store.on('job', (j: JobSummary) => watchJob(j))
    return () => {
      off()
      cancelAnimationFrame(raf)
    }
  })

  async function load() {
    const media0 = song.media
    if (!media0.length) {
      store.notify('還沒有可以播放的檔案', true)
      ui.studio = null
      return
    }
    try {
      const [v, t, q] = await Promise.all([
        api<LyricsViews>(`/songs/${enc(id)}/lyrics`),
        api<TimingView>(`/songs/${enc(id)}/timing`).catch(() => ({ lines: [] }) as unknown as TimingView),
        api<QAView>(`/songs/${enc(id)}/qa`).catch(() => ({ lines: [] }) as unknown as QAView),
      ])
      lines = v.doc.lines
      language = v.doc.language ?? null
      timing = t.lines
      qa = new Map(q.lines.map((c) => [c.index, c]))
    } catch (e) {
      store.notify((e as Error).message, true)
      ui.studio = null
      return
    }
    let line = opts.line ?? null
    if (opts.jumpToQa) line = qaIndexes[0] ?? line
    let start = opts.at ?? 0
    if (line != null && timing[line]) {
      start = Math.max(0, timing[line].start - 1.5)
      select(line, { manual: false })
    } else {
      const near = timing.findIndex((t) => t.end >= start)
      select(near >= 0 ? near : 0, { manual: false })
    }
    setSource(media0.find((m) => m.kind === opts.kind) ?? media0[0], start, true)
    cancelAnimationFrame(raf)
    raf = requestAnimationFrame(loop)
  }

  function setSource(m: SongView_media, at: number, autoplay?: boolean) {
    const v = video!
    const play = autoplay ?? !v.paused
    media = m
    v.src = m.url
    v.playbackRate = rate
    v.addEventListener(
      'loadedmetadata',
      () => {
        v.currentTime = at || 0
        duration = v.duration || 1
        fit()
        if (play) v.play().catch(() => {})
      },
      { once: true },
    )
  }

  /** 調整時正在看已燒字幕的成品：換到沒有字幕的版本，才看得到即時預覽。 */
  function ensureLive() {
    if (!media || !BURNED.has(media.kind)) return
    const live = ['instrumental', 'source'].map((k) => song.media.find((m) => m.kind === k)).find(Boolean)
    if (!live) return
    setSource(live, video!.currentTime)
    store.notify(`已切換到「${live.label}」，即時預覽調整後的字幕`)
  }

  /** 即時字幕的字級：影片實際顯示的高度 × 字幕比例（和燒進影片的一樣大）。 */
  function fit() {
    if (!frame || !video) return
    const ratio = BASE_RATIO * (store.settings.subtitle_scale ?? 1)
    const shown = video.videoWidth && video.videoHeight ? Math.min(frame.clientHeight, (frame.clientWidth * video.videoHeight) / video.videoWidth) : frame.clientHeight
    subFont = Math.max(10, shown * ratio)
    transFont = Math.max(8, shown * ratio * 0.6)
    transTop = (frame.clientHeight - shown) / 2 + shown * 0.05
  }

  $effect(() => {
    if (!frame) return
    const ro = new ResizeObserver(fit)
    ro.observe(frame)
    return () => ro.disconnect()
  })

  // ---- 即時字幕 ----

  let top = $state(-1)
  let bottom = $state(-1)
  let trans = $state('')

  function loop() {
    raf = requestAnimationFrame(loop)
    const v = video
    if (!v) return
    now = v.currentTime
    paused = v.paused
    if (!timing.length) return
    let current = -1
    timing.forEach((t, i) => {
      if (t.start <= now) current = i
    })
    const following = timing[current + 1]
    const longGap = current >= 0 && following && following.start - timing[current].end > SUB_LONG_GAP
    const show =
      current >= 0 && !(longGap && now > timing[current].end + SUB_LINGER) && !(!following && now > timing[current].end + SUB_LINGER) ? current : -1
    const p = current >= 0 && now <= timing[current].end + 0.3 ? current : -1
    if (p !== playing) {
      playing = p
      if (follow && p >= 0) {
        selected = p
        if (!v.paused) rows[p]?.scrollIntoView({ block: 'nearest', behavior: 'smooth' })
      }
    }
    let t0 = show
    if (t0 < 0) {
      const next = timing.findIndex((t) => t.start > now)
      if (next >= 0 && timing[next].start - now <= SUB_PREVIEW) t0 = next
    }
    const b = t0 >= 0 && t0 + 1 < timing.length ? t0 + 1 : -1
    const tl = show >= 0 && song.info.translation !== false ? lines[lyricIdx[show]] : null
    const tr = tl?.translation ?? ''
    if (t0 !== top) top = t0
    if (b !== bottom) bottom = b
    if (tr !== trans) trans = tr
    for (const span of subBox?.querySelectorAll<HTMLElement>('.w') ?? []) {
      const s = parseFloat(span.dataset.s!)
      const e = parseFloat(span.dataset.e!)
      const pr = now <= s ? 0 : now >= e ? 1 : (now - s) / (e - s)
      span.style.setProperty('--p', `${(pr * 100).toFixed(1)}%`)
    }
  }

  /** 把逐字時間攤到歌詞的每個字元上；對不上時回傳 null（改用不加假名的顯示）。 */
  function charTimes(text: string, words: { text: string; start: number; end: number }[]) {
    const timed: { c: string; s: number; e: number }[] = []
    for (const w of words) {
      const chars = Array.from(w.text).filter((c) => c.trim())
      chars.forEach((c, k) => timed.push({ c, s: w.start + (w.end - w.start) * (k / chars.length), e: w.start + (w.end - w.start) * ((k + 1) / chars.length) }))
    }
    const out: { s: number; e: number }[] = []
    let j = 0
    let last = words[0]?.start ?? 0
    for (const c of Array.from(text)) {
      if (!c.trim()) {
        out.push({ s: last, e: last })
        continue
      }
      if (j >= timed.length || timed[j].c !== c) return null
      out.push(timed[j])
      last = timed[j].e
      j++
    }
    return j === timed.length ? out : null
  }

  function subOf(i: number) {
    const t = timing[i]
    const line = lines[lyricIdx[i]]
    const words = t.words.length ? t.words : [{ text: t.text, start: t.start, end: t.end }]
    const ruby = language === 'ja' && line?.segments?.some((s) => s.ruby) && !timeOf(i)?.mismatch
    const chars = ruby ? charTimes(line.text, words) : null
    return { line, words, chars, text: Array.from(line?.text ?? '') }
  }

  // ---- 選取與調整 ----

  function select(i: number, { seek = false, manual = true } = {}) {
    const count = lyricIdx.length
    if (!count) return
    selected = Math.max(0, Math.min(count - 1, i))
    if (manual) follow = false
    tick().then(() => rows[selected]?.scrollIntoView({ block: 'nearest' }))
    const t = timing[selected]
    if (seek && t && video) video.currentTime = Math.max(0, t.start - 1.5)
  }

  function playLine(i: number) {
    select(i, { seek: true })
    video?.play().catch(() => {})
  }

  async function shift(i: number, delta: number) {
    delta = Math.round(delta * 1000) / 1000
    if (!timing[i] || Math.abs(delta) < 0.01) return false
    const v = await store.run(() => api<TimingView>(`/songs/${enc(id)}/timing/lines/${i}`, { method: 'PATCH', body: { delta } }))
    if (!v) return false
    timing = v.lines
    retimed = true
    const pushed = v.pushed ?? []
    statusText =
      `第 ${i + 1} 句 ${delta > 0 ? '+' : ''}${delta} 秒` +
      (pushed.length ? `，第 ${[...pushed].sort((a, b) => a - b).map((k) => k + 1).join('、')} 句跟著往${pushed[0] < i ? '前' : '後'}挪` : '') +
      '，已儲存'
    ensureLive()
    return true
  }

  async function setNow(i = selected) {
    const t = timing[i]
    if (!t || !video) return
    if ((await shift(i, video.currentTime - t.start)) && i + 1 < lyricIdx.length) select(i + 1)
  }

  async function editLine(i: number, body: { text?: string; singer?: string }) {
    const v = await store.run(() => api<LyricsViews>(`/songs/${enc(id)}/lines/${i}`, { method: 'PATCH', body }))
    if (!v) return false
    lines = v.doc.lines
    return true
  }

  async function cycleSinger(i: number) {
    const next = nextSinger(lines[lyricIdx[i]].singer) ?? ''
    if (await editLine(i, { singer: next })) {
      retimed = true // 演唱者只影響顏色：要重新燒錄才會更新
      ensureLive()
    }
  }

  async function saveText(i: number, value: string) {
    editingText = null
    const text = value.trim()
    if (!text || text === lines[lyricIdx[i]].text) return
    if (await editLine(i, { text })) {
      textChanged = true
      statusText = `第 ${i + 1} 句歌詞已儲存`
    }
  }

  function retimeMenu(anchor: HTMLElement, i: number) {
    const count = timing.length - i
    menu.show(anchor, `以第 ${i + 1} 句目前的開頭為準（先播到開唱的瞬間設定好）`, [
      { label: `重對這句及之後全部（${count} 句）`, desc: `AI 重新對第 ${i + 1} 句到最後一句；這句之前的不動。約 10–30 秒`, action: () => retime(i, 'from') },
      { label: '只重對這句', desc: '範圍到下一句的開頭，重新抓這句每個字的時間。幾秒完成', action: () => retime(i, 'line') },
    ])
  }

  const retimeLabel = (p: { index: number; mode: string; count: number }) =>
    p.mode === 'from' ? `AI 重新對時第 ${p.index + 1} 句及之後全部（共 ${p.count} 句）` : `AI 重新對時第 ${p.index + 1} 句（只有這句）`

  async function retime(i: number, mode: string) {
    if (pendingJob) return
    const res = await store.runJob([id], ['retime'], { line: i, mode })
    const job = res?.created[0]
    if (!job) return
    pendingJob = { id: job.id, index: i, mode, count: timing.length - i }
    statusText = `${retimeLabel(pendingJob)}：排隊中…`
  }

  async function watchJob(j: JobSummary) {
    const p = pendingJob
    if (!p || j.id !== p.id) return
    if (j.status === 'queued' || j.status === 'running') {
      statusText = `${retimeLabel(p)}：${j.status === 'queued' ? '排隊中' : '處理中'}…`
      return
    }
    pendingJob = null
    if (j.status !== 'done') {
      statusText = ''
      store.notify(j.status === 'failed' ? `${retimeLabel(p)}失敗：${j.error || ''}` : `已取消${retimeLabel(p)}`, j.status === 'failed')
      return
    }
    const v = await store.run(() => api<TimingView>(`/songs/${enc(id)}/timing`))
    if (!v) return
    timing = v.lines
    retimed = true
    statusText = `${retimeLabel(p)}完成，播放確認看看`
    ensureLive()
    playLine(p.index)
  }

  function close() {
    video?.pause()
    cancelAnimationFrame(raf)
    if (retimed || textChanged) store.notify('已儲存調整。伴唱帶需要更新，按「更新伴唱帶」重新製作後生效')
    ui.studio = null
  }

  async function approve() {
    const on = song.approval.status !== 'approved'
    await store.attempt(() => api(`/songs/${enc(id)}/approval`, { method: 'PUT', body: { approved: on } }), on ? '已確認這一版伴唱帶沒問題' : '已取消確認')
  }

  async function runKaraoke() {
    retimed = textChanged = false
    close()
    const res = await store.runJob([id], ['karaoke'])
    if (res?.created.length) store.notify('已排入佇列：製作伴唱帶')
  }

  function onKey(e: KeyboardEvent) {
    if (ui.song || ui.editor) return
    if (e.key === 'Escape') {
      if (menu.open) return menu.close()
      return close()
    }
    if (e.ctrlKey || e.metaKey || e.altKey) return
    const t = e.target as HTMLElement
    if (t?.closest('input:not([type=range]):not([type=checkbox]), select, textarea')) return
    const v = video!
    const actions: Record<string, () => unknown> = {
      ' ': () => (v.paused ? v.play().catch(() => {}) : v.pause()),
      ArrowLeft: () => (v.currentTime = Math.max(0, v.currentTime - 2)),
      ArrowRight: () => (v.currentTime = v.currentTime + 2),
      ArrowUp: () => select(selected - 1),
      ArrowDown: () => select(selected + 1),
      Enter: () => setNow(),
      '[': () => shift(selected, -0.1),
      ']': () => shift(selected, 0.1),
      '{': () => shift(selected, -0.5),
      '}': () => shift(selected, 0.5),
    }
    const a = actions[e.key]
    if (!a) return
    e.preventDefault()
    a()
  }

  let approved = $derived(song?.approval.status === 'approved')
  let fontFamily = $derived(song?.font ? `kara-${song.font.id.slice(0, 12)}` : '')
</script>

<svelte:window onkeydown={onKey} />
<svelte:head>
  {#if song?.font}{@html `<style>@font-face { font-family: "${fontFamily}"; src: url("${song.font.url}"); }</style>`}{/if}
</svelte:head>

{#snippet sub(i: number, cls: string)}
  <div class="sub-row {cls}">
    {#if i >= 0}
      {@const s = subOf(i)}
      <span class="sub-line" data-singer={s.line?.singer || undefined}>
        {#if s.chars}
          {#each s.line.segments ?? [] as seg}
            {#if seg.ruby}
              <ruby
                >{#each s.text.slice(seg.start, seg.end) as ch, k}<span class="w" data-s={s.chars[seg.start + k].s} data-e={s.chars[seg.start + k].e}>{ch}</span>{/each}<rt
                  ><span class="w" data-s={s.chars[seg.start].s} data-e={s.chars[seg.end - 1].e}>{seg.ruby}</span></rt
                ></ruby
              >
            {:else}
              {#each s.text.slice(seg.start, seg.end) as ch, k}<span class="w" data-s={s.chars[seg.start + k].s} data-e={s.chars[seg.start + k].e}>{ch}</span>{/each}
            {/if}
          {/each}
        {:else}
          {#each s.words as w}<span class="w" data-s={w.start} data-e={w.end}>{w.text}</span>{/each}
        {/if}
      </span>
    {/if}
  </div>
{/snippet}

<div class="overlay center" role="presentation" onmousedown={(e) => e.target === e.currentTarget && close()}>
  <div class="studio" role="dialog" aria-modal="true" aria-labelledby="studio-heading">
    <header class="sheet-head">
      <div class="grow">
        <h2 id="studio-heading">播放</h2>
        <div class="muted ellipsis">{song.artist ? `${song.artist} - ${song.title}` : song.title}</div>
      </div>
      <button
        type="button"
        class="btn small"
        class:approved
        disabled={!approved && song.status.karaoke !== 'done'}
        title={approved
          ? '已確認這一版沒問題；點一下取消確認（之後只需重燒的更新不影響確認）'
          : song.status.karaoke !== 'done'
            ? '伴唱帶還沒做好或需要更新，做好後才能確認'
            : '確認目前這一版伴唱帶沒問題；之後需要重新對時才會變回需重新確認'}
        onclick={approve}><Icon name="check" /><span>{approved ? '已確認（取消）' : '確認沒問題'}</span></button
      >
      <button type="button" class="btn small" title="開啟歌詞編輯器" onclick={() => (close(), ui.openEditor(id))}><Icon name="lyrics" />完整歌詞</button>
      <div class="seg-toggle" title="播放的版本（不影響成品）">
        {#each song.media as m}
          <button type="button" class:on={media?.kind === m.kind} title={m.hint} onclick={() => setSource(m, video?.currentTime ?? 0)}>{m.label}</button>
        {/each}
      </div>
      <button type="button" class="icon-btn" title="關閉（Esc）" aria-label="關閉" onclick={close}><Icon name="close" size={18} /></button>
    </header>
    <div class="studio-body">
      <div class="studio-stage">
        <div class="stage-frame" bind:this={frame}>
          <!-- svelte-ignore a11y_media_has_caption -->
          <video bind:this={video} preload="auto" playsinline onclick={() => (video!.paused ? video!.play() : video!.pause())}></video>
          {#if !burned}
            <div class="live-trans" style="font-size:{transFont}px;top:{transTop}px">{trans}</div>
            <div class="live-sub" bind:this={subBox} style="font-size:{subFont}px;{fontFamily ? `font-family:'${fontFamily}', var(--lyric-font)` : ''}">
              {#key `${top}|${bottom}|${timing.length}|${lines.length}`}
                {@render sub(top, 'current')}
                {@render sub(bottom, 'next')}
              {/key}
            </div>
          {/if}
        </div>
        {#if burned && timing.length}
          <div class="studio-note">這個版本的字幕已經燒在影片裡；調整後要「更新伴唱帶」才會反映。做任何調整時會自動切到伴奏，即時預覽調整後的字幕。</div>
        {/if}
        <div class="studio-controls">
          <button type="button" class="btn small" title="播放 / 暫停（空白鍵）" onclick={() => (video!.paused ? video!.play() : video!.pause())}
            ><Icon name={paused ? 'play' : 'pause'} /><span>{paused ? '播放' : '暫停'}</span></button
          >
          <span class="studio-clock">{fmtClock(now)}</span>
          <input
            type="range"
            min="0"
            max={duration}
            step="0.01"
            value={now}
            aria-label="播放位置"
            onpointerdown={() => (seeking = true)}
            onpointerup={() => (seeking = false)}
            oninput={(e) => (video!.currentTime = parseFloat((e.currentTarget as HTMLInputElement).value))}
          />
          <select
            title="播放速度"
            aria-label="播放速度"
            bind:value={rate}
            onchange={() => video && (video.playbackRate = rate)}
          >
            <option value={0.5}>0.5×</option>
            <option value={0.75}>0.75×</option>
            <option value={1}>1×</option>
          </select>
        </div>
        <div class="studio-keys">
          <span><kbd>空白</kbd> 播放 / 暫停</span>
          <span><kbd>←</kbd><kbd>→</kbd> 倒退 / 快轉 2 秒</span>
          <span><kbd>↑</kbd><kbd>↓</kbd> 上一句 / 下一句</span>
          <span><kbd>Enter</kbd> 選取的句子從現在開始</span>
          <span><kbd>[</kbd><kbd>]</kbd> 選取的句子 −/+ 0.1 秒（Shift：0.5 秒）</span>
        </div>
      </div>
      <div class="studio-side">
        <label
          class="time-following studio-follow"
          title="開著時，選取的句子會跟著播放時間走；手動點選某一句後會自動關掉，選取就停在你點的那句"
          ><input type="checkbox" bind:checked={follow} onchange={() => follow && playing >= 0 && select(playing, { manual: false })} /> 選取跟著播放走</label
        >
        {#if song.approval.status === 'stale'}
          <div class="approval-note">
            <Icon name="warn" />{changed.size
              ? `上次確認之後改了第 ${[...changed].map((i) => i + 1).join('、')} 句（左邊有橘色標記），看過這幾句即可再確認。`
              : '上次確認之後重新對時了（歌詞沒改），請重新看過再確認。'}
          </div>
        {/if}
        {#if lyricIdx.length && !timing.length}
          <div class="studio-warn">還沒有對時結果：製作伴唱帶之後才有每句的時間與字幕預覽，現在可以先邊聽邊修改歌詞。</div>
        {:else if lyricIdx.length && (mismatch || textChanged)}
          <div class="studio-warn">歌詞文字和目前的對時結果不同：按「更新伴唱帶」會重新對時（約一分鐘），這裡手動調整的時間會被取代。建議先改完文字並更新伴唱帶，再回來調時間。</div>
        {/if}
        {#if qaIndexes.length}
          {@const wrong = qaIndexes.filter((i) => qa.get(i)!.status === 'wrong').length}
          <div class="studio-qa">
            <Icon name="warn" />
            <span class="grow"
              >對時檢查：{[wrong ? `${wrong} 句可能不準` : '', qaIndexes.length - wrong ? `${qaIndexes.length - wrong} 句待確認` : ''].filter(Boolean).join('、')}</span
            >
            <button type="button" class="btn small" onclick={() => playLine([...qaIndexes].reverse().find((i) => i < selected) ?? qaIndexes.at(-1)!)}>上一個</button>
            <button type="button" class="btn small" onclick={() => playLine(qaIndexes.find((i) => i > selected) ?? qaIndexes[0])}>下一個</button>
          </div>
        {/if}
        <div class="studio-lines">
          {#if !lyricIdx.length}
            <div class="empty-lyrics">
              還沒有歌詞。<button type="button" class="view-link attention" onclick={() => (close(), ui.openEditor(id))}><Icon name="lyrics" />輸入歌詞</button>
            </div>
          {/if}
          {#each lines as line, docIndex}
            {#if line.kind === 'blank'}
              <div class="blank-line"></div>
            {:else if line.kind === 'comment'}
              <div class="comment-line">{line.text}</div>
            {:else}
              {@const i = lyricIdx.indexOf(docIndex)}
              {@const t = timeOf(i)}
              {@const check = qaOf(i)}
              <div
                bind:this={rows[i]}
                class="studio-line {check ? `qa-${check.status}` : ''}"
                class:mismatch={t?.mismatch}
                class:selected={i === selected}
                class:tools-open={Math.abs(i - selected) <= 1}
                class:playing={i === playing}
                class:changed={changed.has(i)}
                role="button"
                tabindex="-1"
                onclick={() => select(i)}
                onkeydown={() => {}}
              >
                <span class="line-no">{i + 1}</span>
                <button
                  type="button"
                  class="singer"
                  data-singer={line.singer || undefined}
                  title="演唱者：點一下切換"
                  onclick={(e) => {
                    e.stopPropagation()
                    cycleSinger(i)
                  }}>{line.singer || '—'}</button
                >
                <div class="studio-text" title="點兩下修改歌詞" role="presentation" ondblclick={(e) => (e.stopPropagation(), (editingText = i))}>
                  {#if editingText === i}
                    <!-- svelte-ignore a11y_autofocus -->
                    <input
                      class="inline-edit"
                      value={line.text}
                      spellcheck="false"
                      aria-label="歌詞"
                      autofocus
                      onclick={(e) => e.stopPropagation()}
                      onkeydown={(e) => {
                        e.stopPropagation()
                        if (e.key === 'Enter') saveText(i, (e.currentTarget as HTMLInputElement).value)
                        if (e.key === 'Escape') editingText = null
                      }}
                      onblur={(e) => saveText(i, (e.currentTarget as HTMLInputElement).value)}
                    />
                  {:else}
                    <span class="studio-words">{line.text}{#if line.translation}<small class="studio-trans">{line.translation}</small>{/if}</span>
                    <button
                      type="button"
                      class="edit-btn"
                      title="修改歌詞"
                      aria-label="修改歌詞"
                      onclick={(e) => {
                        e.stopPropagation()
                        editingText = i
                      }}><Icon name="edit" /></button
                    >
                  {/if}
                </div>
                {#if check}
                  <button
                    type="button"
                    class="qa-flag"
                    title="{check.status === 'wrong' ? '可能不準' : '待確認'}：{check.reasons.join('；')}&#10;點一下從這句開始播放"
                    aria-label="播放這句"
                    onclick={(e) => (e.stopPropagation(), playLine(i))}><Icon name="warn" /></button
                  >
                {:else}<span></span>{/if}
                <span class="studio-time" title={t?.mismatch ? '歌詞改過，這是改之前的時間' : '開始時間'}>{t ? fmtClock(t.start) : '—'}</span>
                <div class="studio-tools">
                  <div class="nudge-group" role="group" aria-label="微調開始時間">
                    {#each [-0.5, -0.1, 0.1, 0.5] as d}
                      <button type="button" disabled={!t} title="這句的開始 {d > 0 ? '延後' : '提前'} {Math.abs(d)} 秒" onclick={(e) => (e.stopPropagation(), shift(i, d))}
                        >{d > 0 ? `+${d}` : `−${-d}`}</button
                      >
                    {/each}
                  </div>
                  <button type="button" class="btn small primary" disabled={!t} title="把這句的開始設成目前播放的位置（選取的句子也可以按 Enter）" onclick={(e) => (e.stopPropagation(), setNow(i))}
                    >設為現在</button
                  >
                  <button type="button" class="icon-btn tool-icon" disabled={!t} title="從這句開始前 1.5 秒播放" aria-label="播放這句" onclick={(e) => (e.stopPropagation(), playLine(i))}
                    ><Icon name="play" /></button
                  >
                  <button
                    type="button"
                    class="btn small ai-btn"
                    disabled={!t || t.mismatch || Boolean(pendingJob)}
                    aria-haspopup="menu"
                    title="以這句目前的開頭為準，讓 AI 重新對時"
                    onclick={(e) => (e.stopPropagation(), retimeMenu(e.currentTarget, i))}>AI 重對<Icon name="caret" /></button
                  >
                </div>
              </div>
            {/if}
          {/each}
        </div>
        <footer class="studio-foot">
          <span class="muted ellipsis">{statusText}</span>
          <button type="button" class="btn primary" disabled={song.status.lyrics !== 'done'} title="用調整後的歌詞與時間重新製作伴唱帶" onclick={runKaraoke}
            >{['pending', 'no_lyrics'].includes(song.status.karaoke) ? '製作伴唱帶' : '更新伴唱帶'}</button
          >
        </footer>
      </div>
    </div>
  </div>
</div>
