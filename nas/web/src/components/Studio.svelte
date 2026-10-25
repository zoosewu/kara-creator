<script lang="ts">
  // 播放畫面：左邊播放影片，右邊歌詞清單，可以邊聽邊改歌詞、演唱者、時間、讓 AI 重對。
  // 播放沒燒字幕的版本時，網頁即時畫逐字填色的字幕（含假名、翻譯、演唱者顏色），調整後立刻看得到；
  // 已燒字幕的成品不另外疊字幕，一調整就自動切到伴奏預覽。
  import { onMount, tick } from 'svelte'
  import { api, enc, type EditorLine, type JobSummary, type LyricsViews, type QAView, type RetimeResult, type TimingView } from '../lib/api'
  import { menu } from '../lib/menu.svelte'
  import { choose, MODES, plan, TRACKS, type Mode, type Track } from '../lib/playback'
  import { prefs } from '../lib/prefs.svelte'
  import { store } from '../lib/state.svelte'
  import { ui, type StudioOpts } from '../lib/ui.svelte'
  import { fmtClock, nextSinger, sameText } from '../lib/util'
  import Icon from './Icon.svelte'

  let props: { id: string; opts: StudioOpts } = $props()
  // 一個元件只對應一首歌（App 用 {#key}）：關閉時外面會先把 ui.* 清空，props 也會跟著變空，所以開啟時就記下來
  // svelte-ignore state_referenced_locally
  const { id, opts } = props
  let song = $derived(store.songs.get(id)!)

  const SUB_PREVIEW = 4 // 還沒開始唱時，提前幾秒先顯示第一句
  const SUB_LONG_GAP = 8 // 兩句之間空這麼久算間奏：唱完 SUB_LINGER 秒後先收起來
  const SUB_LINGER = 2
  const BREAK_GAP = 6 // 和 worker 的 subtitles.Style.break_gap 相同：句間超過這麼久算新的一段
  const BASE_RATIO = 0.075
  const FONT_SCALE = 0.69 // 讀不到字型的縮放時用 Noto Sans CJK 的值

  let lines = $state<EditorLine[]>([])
  let language = $state<string | null>(null)
  let pendingReadings = $state(0) // 自動讀音還沒算好的句數（AI 伺服器算好會通知）
  let timing = $state<TimingView['lines']>([])
  let qa = $state(new Map<number, QAView['lines'][number]>())
  // 播放的版本：字幕（模擬 / 燒錄）× 聲音（伴唱 / 人聲 / 原始影片）；聲音不是畫面本身的時候，畫面靜音、另外播放（extAudio）
  let mode = $state<Mode>('burned')
  let track = $state<Track>('instrumental')
  let extAudio = $state(false)
  let sheetOpen = $state(false) // 手機：「⋯」打開的面板（標題列、版本、速度、音量、提示都收在裡面）
  let audio = $state<HTMLAudioElement>()
  let selected = $state(0)
  let playing = $state(-1)
  let follow = $state(prefs.studioFollow)
  let retimed = $state(false)
  let textChanged = $state(false)
  // 還沒儲存的調整（按「儲存」才送出，避免誤觸）：時間的調整（移動、AI 重對的結果）依序記在 ops，
  // NAS 用 dry run 算出預覽（連鎖推動和存檔時相同）；文字、演唱者記在 edits。saved* 是 NAS 上目前的內容，復原時用
  type TimingOp = { line: number; delta?: number; retime?: RetimeResult }
  let ops = $state<TimingOp[]>([])
  let edits = $state(new Map<number, { text?: string; singer?: string }>())
  let savedLines: EditorLine[] = []
  let savedTiming = $state<TimingView['lines']>([])
  let saving = $state(false)
  /** 和存檔不同的句子（時間變了，或文字、演唱者改了）。 */
  let draftLines = $derived.by(() => {
    const out = new Set(edits.keys())
    timing.forEach((t, i) => {
      const s = savedTiming[i]
      if (!s || Math.abs(t.start - s.start) > 0.0005 || Math.abs(t.end - s.end) > 0.0005) out.add(i)
    })
    return out
  })
  let draftCount = $derived(draftLines.size)
  let dirty = $derived(ops.length > 0 || edits.size > 0)
  let editingText = $state<number | null>(null)
  let pendingJob = $state<{ id: string; index: number; mode: string; count: number } | null>(null)
  let statusText = $state('')
  let rate = $state(prefs.rate)
  let volume = $state(prefs.volume)
  let muted = $state(prefs.muted)
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
  let subSide = $state(0) // 字幕左右的邊界（px）
  let subBottom = $state(0)

  // 第幾句歌詞（依序）在 lines 裡的位置
  let lyricIdx = $derived(lines.map((l, i) => (l.kind === 'lyric' ? i : -1)).filter((i) => i >= 0))
  let changed = $derived(new Set(song?.approval.status === 'stale' ? (song.approval.changed ?? []) : []))
  let burned = $derived(mode === 'burned')

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
    ui.pauseStudio = () => video?.pause()
    const off = store.on('job', (j: JobSummary) => watchJob(j))
    const offReadings = store.on('readings', (d: { song: string }) => d.song === id && refreshReadings())
    return () => {
      off()
      offReadings()
      ui.pauseStudio = null
      cancelAnimationFrame(raf)
    }
  })

  /** 讀歌詞、時間、檢查結果（開啟時，以及疊在上面的歌詞編輯器 / 資訊關掉時；不影響播放）。 */
  async function fetchData() {
    const [v, t, q] = await Promise.all([
      api<LyricsViews>(`/songs/${enc(id)}/lyrics`),
      api<TimingView>(`/songs/${enc(id)}/timing`).catch(() => ({ lines: [] }) as unknown as TimingView),
      api<QAView>(`/songs/${enc(id)}/qa`).catch(() => ({ lines: [] }) as unknown as QAView),
    ])
    lines = v.doc.lines
    savedLines = structuredClone($state.snapshot(v.doc.lines))
    language = v.doc.language ?? null
    pendingReadings = v.pending_readings?.length ?? 0
    timing = savedTiming = t.lines
    ops = []
    edits = new Map()
    qa = new Map(q.lines.map((c) => [c.index, c]))
  }

  /** AI 算好自動讀音：只換上新的假名（還沒儲存的文字、演唱者不動；文字改過的句子等儲存後再算）。 */
  async function refreshReadings() {
    const v = await api<LyricsViews>(`/songs/${enc(id)}/lyrics`).catch(() => null)
    if (!v) return
    pendingReadings = v.pending_readings?.length ?? 0
    v.doc.lines.forEach((fresh, k) => {
      if (lines[k] && savedLines[k] && fresh.text === savedLines[k].text) {
        savedLines[k].segments = fresh.segments
        if (lines[k].text === fresh.text) lines[k].segments = fresh.segments
      }
    })
  }

  // 疊在上面的歌詞編輯器 / 資訊關掉了：重新讀一次（歌詞、語言可能改了），影片照常播
  let overlaid = $derived(Boolean(ui.editor || ui.song))
  let wasOverlaid = false
  $effect(() => {
    const on = overlaid
    if (wasOverlaid && !on)
      fetchData()
        .then(() => select(Math.min(selected, Math.max(0, lyricIdx.length - 1)), { manual: false }))
        .catch((e) => store.notify((e as Error).message, true))
    wasOverlaid = on
  })

  async function load() {
    const media0 = song.media
    if (!media0.length) {
      store.notify('還沒有可以播放的檔案', true)
      ui.studio = null
      return
    }
    try {
      await fetchData()
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
    const pick = choose(media0, opts.mode ?? prefs.studioMode, opts.track ?? prefs.studioTrack)
    if (pick) setSource(pick[0], pick[1], start, true)
    cancelAnimationFrame(raf)
    raf = requestAnimationFrame(loop)
  }

  /** 換成另一個版本，從 at 秒繼續（沒有那個組合時換成最接近的）。 */
  function setSource(m: Mode, t: Track, at: number, autoplay?: boolean) {
    const pick = choose(song.media, m, t)
    if (!pick) return
    ;[mode, track] = pick
    const p = plan(song.media, mode, track)!
    const v = video!
    const play = autoplay ?? !v.paused
    extAudio = Boolean(p.audio)
    if (audio) {
      audio.pause()
      if (p.audio) audio.src = p.audio
      else audio.removeAttribute('src')
    }
    v.src = p.picture
    v.playbackRate = rate
    v.addEventListener(
      'loadedmetadata',
      () => {
        v.currentTime = at || 0
        duration = v.duration || 1
        fit()
        syncAudio()
        if (play) v.play().catch(() => {})
      },
      { once: true },
    )
  }

  /** 另外播放的聲音跟著畫面：時間、速度、播放 / 暫停都以畫面為準。 */
  function syncAudio() {
    const v = video
    if (!extAudio || !audio || !v) return
    audio.playbackRate = v.playbackRate
    if (Math.abs(audio.currentTime - v.currentTime) > 0.2) audio.currentTime = v.currentTime
    if (v.paused !== audio.paused) (v.paused ? audio.pause() : audio.play().catch(() => {}))
  }

  // 音量、靜音套用到聽得到的那個：另外播放聲音時畫面一律靜音
  $effect(() => {
    const ext = extAudio
    if (video) {
      video.volume = volume
      video.muted = ext || muted
    }
    if (audio) {
      audio.volume = volume
      audio.muted = muted
    }
  })

  /** 調整時正在看燒好的字幕：換到模擬（同樣的聲音），才看得到調整後的字幕。 */
  function ensureLive() {
    if (mode !== 'burned' || !choose(song.media, 'live', track)) return
    setSource('live', track, video!.currentTime)
    store.notify('已切換到「模擬」預覽調整')
  }

  /**
   * 即時字幕的大小與位置，照 worker 燒錄時的版面（subtitles.build）：以影片實際顯示的範圍為準，
   * 字級 = 顯示高度 × 字幕比例 × 字型的 libass 縮放（ASS 的字級是字型「上緣到下緣」的高度，字本身比它小），
   * 左右各留 6%、下緣 7%。
   */
  function fit() {
    if (!frame || !video) return
    const ratio = BASE_RATIO * (store.settings.subtitle_scale ?? 1)
    const fw = frame.clientWidth
    const fh = frame.clientHeight
    const vw = video.videoWidth || 16
    const vh = video.videoHeight || 9
    const shownH = Math.min(fh, (fw * vh) / vw)
    const shownW = (shownH * vw) / vh
    const em = ratio * (song?.font?.scale || FONT_SCALE)
    subFont = Math.max(10, shownH * em)
    transFont = Math.max(8, shownH * em * 0.6)
    transTop = (fh - shownH) / 2 + shownH * 0.05
    subSide = (fw - shownW) / 2 + shownW * 0.06
    subBottom = (fh - shownH) / 2 + shownH * 0.07
  }

  // 字幕大小的設定或字型換了：重新算
  $effect(() => {
    void store.settings.subtitle_scale
    void song?.font?.scale
    fit()
  })

  $effect(() => {
    if (!frame) return
    const ro = new ResizeObserver(fit)
    ro.observe(frame)
    return () => ro.disconnect()
  })

  // ---- 即時字幕 ----

  let top = $state(-1)
  let bottom = $state(-1)
  // 每句放在上排靠左（0）或下排靠右（1）：同一段輪流放，間奏之後的新段落從上排開始（同 worker 的 _schedule）
  let slots = $derived.by(() => {
    let first = 0
    return timing.map((t, i) => {
      if (i === 0 || t.start - timing[i - 1].end > BREAK_GAP) first = i
      return (i - first) % 2
    })
  })
  let trans = $state('')

  function loop() {
    raf = requestAnimationFrame(loop)
    const v = video
    if (!v) return
    now = v.currentTime
    paused = v.paused
    syncAudio()
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
    // 下一句和這句在同一段（位置錯開）才一起顯示
    const b = t0 >= 0 && t0 + 1 < timing.length && slots[t0 + 1] !== slots[t0] ? t0 + 1 : -1
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

  const SMALL_KANA = new Set('ゃゅょぁぃぅぇぉゎャュョァィゥェォヮ')

  /** 讀音切成一拍一拍（拗音的小假名跟著前一個字），每拍依位置對到底下的字取時間來填色；和燒進影片的字幕相同。 */
  function rubyParts(reading: string, chars: { s: number; e: number }[], start: number, end: number) {
    const units: string[] = []
    for (const c of Array.from(reading)) {
      if (SMALL_KANA.has(c) && units.length) units[units.length - 1] += c
      else units.push(c)
    }
    const at = (f: number) => {
      const pos = start + f * (end - start)
      const k = Math.min(Math.floor(pos), end - 1)
      return chars[k].s + (pos - k) * (chars[k].e - chars[k].s)
    }
    return units.map((u, k) => ({ text: u, s: at(k / units.length), e: at((k + 1) / units.length) }))
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

  /** 移動一句（記進草稿）：NAS 算出套用全部草稿後的時間來預覽，移不了的話不記。 */
  async function shift(i: number, delta: number) {
    delta = Math.round(delta * 1000) / 1000
    if (!timing[i] || Math.abs(delta) < 0.01) return false
    const v = await preview([...ops, { line: i, delta }])
    if (!v) return false
    const pushed = v.pushed ?? []
    statusText =
      `第 ${i + 1} 句 ${delta > 0 ? '+' : ''}${delta} 秒` +
      (pushed.length ? `，第 ${[...pushed].sort((a, b) => a - b).map((k) => k + 1).join('、')} 句跟著往${pushed[0] < i ? '前' : '後'}挪` : '')
    ensureLive()
    return true
  }

  /** 用 NAS 的 dry run 算出套用 next 之後的時間；成功才換成新的草稿。 */
  async function preview(next: TimingOp[]) {
    const v = await store.run(() => api<TimingView>(`/songs/${enc(id)}/timing/edits`, { method: 'POST', body: { edits: next, dry_run: true } }))
    if (!v) return null
    ops = next
    timing = v.lines
    return v
  }

  async function setNow(i = selected) {
    const t = timing[i]
    if (!t || !video) return
    if ((await shift(i, video.currentTime - t.start)) && i + 1 < lyricIdx.length) select(i + 1)
  }

  function edit(i: number, change: { text?: string; singer?: string }) {
    const line = lines[lyricIdx[i]]
    Object.assign(line, change)
    const saved = savedLines[lyricIdx[i]]
    const e = { ...edits.get(i), ...change }
    if (e.text === saved.text) delete e.text
    if ((e.singer || null) === (saved.singer || null)) delete e.singer
    const next = new Map(edits)
    if (Object.keys(e).length) next.set(i, e)
    else next.delete(i)
    edits = next
  }

  function cycleSinger(i: number) {
    edit(i, { singer: nextSinger(lines[lyricIdx[i]].singer) ?? '' })
    ensureLive()
  }

  function setText(i: number, value: string) {
    editingText = null
    const text = value.trim()
    if (!text || text === lines[lyricIdx[i]].text) return
    edit(i, { text })
  }

  /** 送出草稿：先改文字、演唱者，再依序套用時間的調整。ask 時先確認。 */
  async function save({ ask = true } = {}) {
    if (!dirty || saving) return true
    if (ask && !confirm(`儲存 ${draftCount} 句的調整？`)) return false
    saving = true
    const ok = await store.run(async () => {
      let v: LyricsViews | null = null
      for (const [i, body] of [...edits].sort((a, b) => a[0] - b[0])) {
        v = await api<LyricsViews>(`/songs/${enc(id)}/lines/${i}`, { method: 'PATCH', body })
        if (body.text !== undefined) textChanged = true
      }
      if (v) {
        lines = v.doc.lines
        savedLines = structuredClone($state.snapshot(v.doc.lines))
      }
      edits = new Map()
      if (ops.length) {
        const t = await api<TimingView>(`/songs/${enc(id)}/timing/edits`, { method: 'POST', body: { edits: ops } })
        timing = savedTiming = t.lines
        ops = []
      }
      retimed = true
      return true
    }, '已儲存')
    saving = false
    if (ok) statusText = ''
    return Boolean(ok)
  }

  function discard() {
    lines = structuredClone(savedLines)
    timing = savedTiming
    ops = []
    edits = new Map()
    statusText = ''
  }

  /** 要做會讀寫 NAS 的事之前：有沒儲存的調整就先問要不要儲存（取消就不做）。 */
  async function settle(what: string) {
    if (!dirty) return true
    if (!confirm(`有 ${draftCount} 句的調整還沒儲存，要先儲存再${what}嗎？`)) return false
    return save({ ask: false })
  }

  function retimeMenu(anchor: HTMLElement, i: number) {
    const count = timing.length - i
    menu.show(anchor, `以第 ${i + 1} 句目前的開頭為準重對`, [
      { label: `重對這句及之後全部（${count} 句）`, desc: `第 ${i + 1} 句到最後一句，約 10–30 秒`, action: () => retime(i, 'from') },
      { label: '只重對這句', desc: '只重抓這句的字，幾秒完成', action: () => retime(i, 'line') },
    ])
  }

  const retimeLabel = (p: { index: number; mode: string; count: number }) =>
    p.mode === 'from' ? `AI 重新對時第 ${p.index + 1} 句及之後全部（共 ${p.count} 句）` : `AI 重新對時第 ${p.index + 1} 句（只有這句）`

  /** AI 用的是 NAS 上的歌詞：範圍內有還沒儲存的文字修改時不能重對。 */
  function retimeBlocked(i: number) {
    return [...edits].some(([k, e]) => e.text !== undefined && k >= i)
  }

  /** AI 重對：以畫面上（含還沒儲存的調整）這句的開頭為準；結果放進草稿，按儲存才存檔。 */
  async function retime(i: number, mode: string) {
    if (pendingJob) return
    if (mode === 'line' ? edits.get(i)?.text !== undefined : retimeBlocked(i)) return store.notify('改過的歌詞還沒儲存，AI 要用儲存後的歌詞重對：請先儲存', true)
    const next = mode === 'line' ? timing[i + 1]?.start : undefined
    const res = await store.runJob([id], ['retime'], { line: i, mode, anchor: timing[i].start, ...(next !== undefined ? { next } : {}) })
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
    const r = await store.run(() => api<RetimeResult>(`/jobs/${enc(p.id)}/result`))
    if (!r || !(await preview([...ops, { line: r.line, retime: r }]))) return
    statusText = `${retimeLabel(p)}完成，播放確認看看`
    ensureLive()
    playLine(p.index)
  }

  function close() {
    if (dirty && !confirm(`有 ${draftCount} 句的調整還沒儲存，確定不儲存就關閉？`)) return
    if (!dirty && pendingJob && !confirm('AI 重對還沒做完，關閉後結果不會留下，確定關閉？')) return
    video?.pause()
    cancelAnimationFrame(raf)
    if (retimed || textChanged) store.notify('已儲存，按「更新伴唱帶」後生效')
    ui.studio = null
  }

  async function approve() {
    const on = song.approval.status !== 'approved'
    await store.attempt(() => api(`/songs/${enc(id)}/approval`, { method: 'PUT', body: { approved: on } }), on ? '已確認這一版伴唱帶沒問題' : '已取消確認')
  }

  async function runKaraoke() {
    if (!(await save())) return
    retimed = textChanged = false
    close()
    const res = await store.runJob([id], ['karaoke'], { interactive: true }) // 編輯完在等結果：比批次優先
    if (res?.created.length) store.notify('已排入佇列：製作伴唱帶')
  }

  function onKey(e: KeyboardEvent) {
    if (ui.song || ui.editor) return
    if (e.key === 'Escape') {
      if (menu.open) return menu.close()
      if (sheetOpen) return (sheetOpen = false)
      return close()
    }
    if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
      e.preventDefault()
      return save()
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

<svelte:window onkeydown={onKey} onbeforeunload={(e) => (dirty || pendingJob) && e.preventDefault()} />
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
              <span class="rb"
                ><span class="rb-text"
                  >{#each rubyParts(seg.ruby, s.chars, seg.start, seg.end) as r}<span class="w" data-s={r.s} data-e={r.e}>{r.text}</span>{/each}</span
                ><span class="rb-base"
                  >{#each s.text.slice(seg.start, seg.end) as ch, k}<span class="w" data-s={s.chars[seg.start + k].s} data-e={s.chars[seg.start + k].e}>{ch}</span>{/each}</span
                ></span
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

{#snippet actions()}
  <button
    type="button"
    class="btn small"
    class:approved
    disabled={!approved && song.status.karaoke !== 'done'}
    title={approved
      ? '已確認，點一下取消'
      : song.status.karaoke !== 'done'
        ? '伴唱帶做好後才能確認'
        : '確認這一版沒問題'}
    onclick={approve}><Icon name="check" /><span>{approved ? '已確認（取消）' : '確認沒問題'}</span></button
  >
  <button type="button" class="btn small" title="編輯歌名、歌手、語言" onclick={async () => (await settle('開資訊')) && (ui.song = id)}><Icon name="tag" />資訊</button>
  <button type="button" class="btn small" title="開啟歌詞編輯器" onclick={async () => (await settle('開歌詞編輯器')) && ui.openEditor(id, { over: true })}><Icon name="lyrics" />完整歌詞</button>
  <div class="seg-toggle" role="group" aria-label="字幕">
    {#each MODES as [m, label, hint]}
      <button
        type="button"
        class:on={mode === m}
        disabled={!TRACKS.some(([t]) => plan(song.media, m, t))}
        title={TRACKS.some(([t]) => plan(song.media, m, t)) ? hint : '還沒有伴唱帶'}
        onclick={() => setSource(m, track, video?.currentTime ?? 0)}>{label}</button
      >
    {/each}
  </div>
  <div class="seg-toggle" role="group" aria-label="聲音">
    {#each TRACKS as [t, label]}
      <button
        type="button"
        class:on={track === t}
        disabled={!plan(song.media, mode, t)}
        title={plan(song.media, mode, t) ? '' : '還沒有去人聲'}
        onclick={() => setSource(mode, t, video?.currentTime ?? 0)}>{label}</button
      >
    {/each}
  </div>
{/snippet}

{#snippet playback()}
  <select
    title="播放速度"
    aria-label="播放速度"
    bind:value={rate}
    onchange={() => (video && (video.playbackRate = rate), prefs.set('rate', rate))}
  >
    <option value={0.5}>0.5×</option>
    <option value={0.75}>0.75×</option>
    <option value={1}>1×</option>
  </select>
  <button
    type="button"
    class="icon-btn"
    title={muted ? '取消靜音' : '靜音'}
    aria-label={muted ? '取消靜音' : '靜音'}
    onclick={() => ((muted = !muted), prefs.set('muted', muted))}><Icon name={muted || volume === 0 ? 'mute' : 'volume'} size={18} /></button
  >
  <input
    class="studio-volume"
    type="range"
    min="0"
    max="1"
    step="0.05"
    aria-label="音量"
    title="音量 {Math.round(volume * 100)}%"
    bind:value={volume}
    onchange={() => prefs.set('volume', volume)}
  />
{/snippet}

{#snippet notices()}
  <label
    class="time-following studio-follow"
    title="選取的句子跟著播放；手動點選時自動關閉"
    ><input type="checkbox" bind:checked={follow} onchange={() => follow && playing >= 0 && select(playing, { manual: false })} /> 選取跟著播放走</label
  >
  {#if song.approval.status === 'stale'}
    <div class="approval-note">
      <Icon name="warn" />{changed.size
        ? `改了第 ${[...changed].map((i) => i + 1).join('、')} 句，看過再確認。`
        : '重新對時過，看過再確認。'}
    </div>
  {/if}
  {#if pendingReadings && language === 'ja'}
    <div class="studio-note studio-pending">{pendingReadings} 句的假名稍後補上（AI 伺服器算好就會出現）</div>
  {/if}
  {#if lyricIdx.length && !timing.length}
    <div class="studio-warn">還沒有對時結果，可以先邊聽邊改歌詞。</div>
  {:else if lyricIdx.length && (mismatch || textChanged)}
    <div class="studio-warn">
      {song.align_patch
        ? '歌詞改過了，按「更新伴唱帶」重對改過的句子。'
        : '歌詞改過了，按「更新伴唱帶」會整首重新對時，手動調整的時間會被取代。'}
    </div>
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
{/snippet}

<div class="overlay center" role="presentation" onmousedown={(e) => e.target === e.currentTarget && close()}>
  <div class="studio" role="dialog" aria-modal="true" aria-labelledby="studio-heading">
    <header class="sheet-head desk-only">
      <div class="grow">
        <h2 id="studio-heading">播放</h2>
        <div class="muted ellipsis">{song.artist ? `${song.artist} - ${song.title}` : song.title}</div>
      </div>
      {@render actions()}
      <button type="button" class="icon-btn" title="關閉（Esc）" aria-label="關閉" onclick={close}><Icon name="close" size={18} /></button>
    </header>
    <div class="studio-body">
      <div class="studio-stage">
        <div class="stage-frame" bind:this={frame}>
          <!-- svelte-ignore a11y_media_has_caption -->
          <video
            bind:this={video}
            preload="auto"
            playsinline
            onclick={() => (video!.paused ? video!.play() : video!.pause())}
            onplay={syncAudio}
            onpause={syncAudio}
            onseeked={syncAudio}
            onratechange={syncAudio}
          ></video>
          <audio bind:this={audio} preload="auto"></audio>
          {#if !burned}
            <div class="live-trans" style="font-size:{transFont}px;top:{transTop}px;left:{subSide}px;right:{subSide}px">{trans}</div>
            <div
              class="live-sub"
              bind:this={subBox}
              style="font-size:{subFont}px;left:{subSide}px;right:{subSide}px;bottom:{subBottom}px;{fontFamily ? `font-family:'${fontFamily}', var(--lyric-font)` : ''}"
            >
              {#key `${top}|${bottom}|${timing.length}|${lines.length}`}
                {@render sub(slots[top] === 1 ? bottom : top, 'upper')}
                {@render sub(slots[top] === 1 ? top : bottom, 'lower')}
              {/key}
            </div>
          {/if}
        </div>
        {#if burned && timing.length}
          <div class="studio-note desk-only">字幕已燒在影片裡，調整後按「更新伴唱帶」才會反映。</div>
        {/if}
        <div class="studio-controls">
          <button type="button" class="btn small" title="播放 / 暫停（空白鍵）" onclick={() => (video!.paused ? video!.play() : video!.pause())}
            ><Icon name={paused ? 'play' : 'pause'} /><span class="desk-only">{paused ? '播放' : '暫停'}</span></button
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
          <span class="desk-only studio-playback">{@render playback()}</span>
          <button type="button" class="icon-btn phone-only" title="更多" aria-label="更多選項" onclick={() => (sheetOpen = true)}><Icon name="more" size={20} /></button>
          <button type="button" class="icon-btn phone-only" title="關閉" aria-label="關閉" onclick={close}><Icon name="close" size={20} /></button>
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
        <div class="desk-only studio-notices">{@render notices()}</div>
        <div class="studio-lines">
          {#if !lyricIdx.length}
            <div class="empty-lyrics">
              還沒有歌詞。<button type="button" class="view-link attention" onclick={() => ui.openEditor(id, { over: true })}><Icon name="lyrics" />輸入歌詞</button>
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
                class:draft={draftLines.has(i)}
                class:playing={i === playing}
                class:changed={changed.has(i)}
                role="button"
                tabindex="-1"
                onclick={() => select(i)}
                onkeydown={() => {}}
              >
                <button
                  type="button"
                  class="line-play"
                  disabled={!t}
                  title="從這句開始播（前 1.5 秒）"
                  aria-label="從第 {i + 1} 句開始播"
                  onclick={(e) => (e.stopPropagation(), playLine(i))}><Icon name="play" /></button
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
                        if (e.key === 'Enter') setText(i, (e.currentTarget as HTMLInputElement).value)
                        if (e.key === 'Escape') editingText = null
                      }}
                      onblur={(e) => setText(i, (e.currentTarget as HTMLInputElement).value)}
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
                <div class="line-tools">
                  <span
                    class="studio-time"
                    title={t?.mismatch
                      ? '歌詞改過，這是改之前的時間'
                      : draftLines.has(i) && savedTiming[i]
                        ? `還沒儲存（原本 ${fmtClock(savedTiming[i].start)}）`
                        : '開始時間'}>{t ? fmtClock(t.start) : '—'}</span
                  >
                  <button type="button" class="btn small set-now" disabled={!t} title="把開始設成目前位置（Enter）" onclick={(e) => (e.stopPropagation(), setNow(i))}
                    >設為現在</button
                  >
                  <button
                    type="button"
                    class="btn small ai-btn"
                    disabled={!t || t.mismatch || Boolean(pendingJob)}
                    aria-haspopup="menu"
                    title="AI 重對：以這句目前的開頭為準重新對時"
                    aria-label="AI 重對"
                    onclick={(e) => (e.stopPropagation(), retimeMenu(e.currentTarget, i))}>AI<Icon name="caret" /></button
                  >
                </div>
              </div>
            {/if}
          {/each}
        </div>
        <footer class="studio-foot" class:dirty>
          <span class="muted ellipsis grow">{dirty ? `${draftCount} 句未儲存${statusText ? `：${statusText}` : ''}` : statusText}</span>
          {#if dirty}
            <button type="button" class="btn" disabled={saving} onclick={discard}>復原</button>
            <button type="button" class="btn primary" disabled={saving} title="儲存調整（Ctrl+S）" onclick={() => save()}>儲存</button>
          {/if}
          <button type="button" class="btn" class:primary={!dirty} disabled={song.status.lyrics !== 'done' || saving} title="用調整後的歌詞與時間重新製作" onclick={runKaraoke}
            >{dirty ? '儲存並' : ''}{['pending', 'no_lyrics'].includes(song.status.karaoke) ? '製作伴唱帶' : '更新伴唱帶'}</button
          >
        </footer>
      </div>
    </div>
    {#if sheetOpen}
      <div class="phone-sheet-backdrop phone-only" role="presentation" onclick={(e) => e.target === e.currentTarget && (sheetOpen = false)}>
        <div class="phone-sheet" role="dialog" aria-label="播放選項">
          <div class="phone-sheet-head">
            <div class="grow">
              <b>{song.title}</b>{#if song.artist}<span class="muted">　{song.artist}</span>{/if}
            </div>
            <button type="button" class="icon-btn" aria-label="收起" onclick={() => (sheetOpen = false)}><Icon name="close" size={18} /></button>
          </div>
          <div class="phone-sheet-row">{@render actions()}</div>
          <div class="phone-sheet-row">{@render playback()}</div>
          {#if burned && timing.length}<div class="studio-note">字幕已燒在影片裡，調整後按「更新伴唱帶」才會反映。</div>{/if}
          {@render notices()}
          <button
            type="button"
            class="btn primary"
            disabled={song.status.lyrics !== 'done' || saving}
            onclick={() => ((sheetOpen = false), runKaraoke())}
            >{dirty ? '儲存並' : ''}{['pending', 'no_lyrics'].includes(song.status.karaoke) ? '製作伴唱帶' : '更新伴唱帶'}</button
          >
        </div>
      </div>
    {/if}
  </div>
</div>
