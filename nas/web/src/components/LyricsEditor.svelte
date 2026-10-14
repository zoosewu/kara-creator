<script lang="ts">
  import { onMount, tick } from 'svelte'
  import { api, enc, type EditorLine, type LyricsViews, type QAView, type Segment, type TimingView, type Views } from '../lib/api'
  import { store } from '../lib/state.svelte'
  import { ui } from '../lib/ui.svelte'
  import { fmtClock, nextSinger, sameText } from '../lib/util'
  import Icon from './Icon.svelte'

  let props: { id: string } = $props()
  // 一個元件只對應一首歌（App 用 {#key}）：關閉時外面會先把 ui.* 清空，props 也會跟著變空，所以開啟時就記下來
  // svelte-ignore state_referenced_locally
  const { id } = props
  let song = $derived(store.songs.get(id)!)
  // 疊在播放畫面上面：關掉就回到播放畫面，不需要「播放」按鈕
  const overStudio = ui.studio?.id === id

  type Mode = 'visual' | 'annotated' | 'plain'
  const HINTS: Record<Mode, string> = {
    visual:
      '點標籤切換演唱者（Shift：整段）；點假名改讀音（灰色自動、藍色手動）；點右側時間移動這句。',
    annotated:
      '<code>[男]</code> <code>[女]</code> <code>[合]</code> 演唱者、<code>&gt; 翻譯</code>、<code>{よみ}</code> 讀音（改了就變手動）、<code>#</code> 註解。',
    plain:
      '一行一句，可直接貼上。沒改的句子保留演唱者與讀音；<code>&gt;</code> 開頭是翻譯，<code>#</code> 開頭是註解。',
  }

  let loaded = $state(false)
  let mode = $state<Mode>('visual')
  let views = $state<Views | null>(null)
  let lines = $state<EditorLine[]>([])
  let text = $state('')
  let dirty = $state(false)
  let exists = $state(false)
  let status = $state('')
  let qa = $state(new Map<number, QAView['lines'][number]>())
  let timing = $state<TimingView['lines']>([])
  let ruby = $state<{ line: number; seg: Segment; el: HTMLElement; value: string } | null>(null)
  let time = $state<{ index: number; el: HTMLElement; delta: number } | null>(null)
  let textarea = $state<HTMLTextAreaElement>()
  let audio = $state<HTMLAudioElement>()
  let listenTimer = 0

  let hasLines = $derived(lines.some((l) => l.kind === 'lyric'))
  let pending = $derived(views?.pending_readings ?? [])
  let changed = $derived(new Set(song?.approval.status === 'stale' ? (song.approval.changed ?? []) : []))

  onMount(() => {
    load()
    // AI 算好假名了：重新產生檢視（文字模式還沒轉回結構時不打擾）
    return store.on('readings', (d: { song: string }) => {
      if (d.song === id && mode === 'visual' && pending.length) convert({ doc: plainDoc() }).catch(() => {})
    })
  })

  async function load() {
    try {
      const [v, q, t] = await Promise.all([
        api<LyricsViews>(`/songs/${enc(id)}/lyrics`),
        api<QAView>(`/songs/${enc(id)}/qa`).catch(() => ({ lines: [] }) as unknown as QAView),
        api<TimingView>(`/songs/${enc(id)}/timing`).catch(() => ({ lines: [] }) as unknown as TimingView),
      ])
      apply(v)
      exists = v.exists
      qa = new Map(q.lines.map((c) => [c.index, c]))
      timing = t.lines
      mode = v.doc.lines.some((l) => l.kind === 'lyric') ? 'visual' : 'plain'
      if (mode !== 'visual') text = views![mode]
      status = exists ? '' : '還沒有歌詞檔'
      loaded = true
      if (mode === 'plain') tick().then(() => textarea?.focus())
    } catch (e) {
      store.notify((e as Error).message, true)
      ui.editor = null
    }
  }

  function apply(v: Views) {
    views = v
    lines = v.doc.lines
  }

  /** 送給 API 的結構：拿掉畫面用的 segments。 */
  function plainDoc() {
    return {
      meta: views?.doc.meta ?? {},
      lines: lines.map((l) => ({ kind: l.kind, text: l.text, singer: l.singer ?? null, rubies: l.rubies ?? [], translation: l.translation ?? '' })),
    }
  }

  async function convert(body: Record<string, unknown>) {
    const v = await api<Views>('/lyrics/convert', { method: 'POST', body: { ...body, language: song.info.language, song: id } })
    apply(v)
    return v
  }

  /** 把目前畫面上的內容（標註模式的結構，或文字模式裡改過的文字）轉成最新的結構。 */
  async function sync() {
    if (mode === 'visual') await convert({ doc: plainDoc() })
    else await convert({ text, format: mode, doc: plainDoc() })
  }

  async function setMode(m: Mode) {
    if (m === mode) return
    closePops()
    if (!(await store.attempt(sync))) return
    mode = m
    if (m !== 'visual') text = views![m]
  }

  function markDirty() {
    dirty = true
    status = '尚未儲存'
  }

  /** 存檔時自動轉成讀音標註的括號讀音（提示用）。 */
  function convertedNote(v: LyricsViews) {
    const c = v.paren_converted ?? []
    return c.length ? `；已把 ${c.length} 處括號讀音（${c.slice(0, 3).join('、')}${c.length > 3 ? '…' : ''}）轉成讀音標註` : ''
  }

  async function save(): Promise<LyricsViews | undefined> {
    return store.run(async () => {
      await sync()
      const v = await api<LyricsViews>(`/songs/${enc(id)}/lyrics`, { method: 'PUT', body: { text: views!.text } })
      apply(v)
      dirty = false
      if (mode !== 'visual') text = views![mode]
      return v
    })
  }

  function close(force = false) {
    if (!force && dirty && !confirm('歌詞還沒儲存，確定要關閉嗎？')) return
    closePops()
    ui.editor = null
  }

  async function parenFix() {
    const count = views!.paren.length
    const ok = await store.attempt(async () => {
      await sync()
      await convert({ text: views!.text, format: 'file', paren_to_ruby: true })
    })
    if (!ok) return
    markDirty()
    if (mode !== 'visual') text = views![mode]
    store.notify(`已轉換 ${count} 處讀音，記得儲存`)
  }

  function cycleSinger(index: number, block: boolean) {
    const next = nextSinger(lines[index].singer)
    lines[index].singer = next
    for (let j = index + 1; block && j < lines.length && lines[j].kind === 'lyric'; j++) lines[j].singer = next
    markDirty()
  }

  // ---- 讀音 ----

  function openRuby(el: HTMLElement, line: number, seg: Segment) {
    closePops()
    ruby = { line, seg, el, value: seg.ruby ?? '' }
  }

  async function applyRuby(reading: string, advance = false) {
    if (!ruby) return
    const { line, seg } = ruby
    ruby = null
    if (seg.manual || reading !== (seg.ruby ?? '')) {
      const l = lines[line]
      l.rubies = (l.rubies ?? []).filter((r) => r.end <= seg.start || r.start >= seg.end)
      if (reading) l.rubies.push({ start: seg.start, end: seg.end, reading })
      if (!(await store.attempt(() => convert({ doc: plainDoc() })))) return
      markDirty()
    }
    if (advance) {
      await tick()
      const next = [...document.querySelectorAll<HTMLElement>('.lines ruby.slot')].find((n) => {
        const li = Number(n.dataset.line)
        return li > line || (li === line && Number(n.dataset.start) > seg.start)
      })
      if (next) {
        const s = lines[Number(next.dataset.line)].segments?.find((x) => x.start === Number(next.dataset.start))
        if (s) {
          next.scrollIntoView({ block: 'nearest' })
          openRuby(next, Number(next.dataset.line), s)
        }
      }
    }
  }

  // ---- 時間 ----

  function timingFor(index: number, t: string) {
    const x = timing[index]
    return x && sameText(x.text, t) ? x : null
  }

  function qaFor(index: number, t: string) {
    const c = qa.get(index)
    return c && sameText(c.text, t) ? c : null
  }

  function listenSource() {
    return (song.media.find((m) => m.kind === 'vocals') ?? song.media.find((m) => m.kind === 'source'))?.url
  }

  async function applyTime() {
    if (!time || !time.delta) return
    const { index, delta } = time
    const v = await store.run(() => api<TimingView>(`/songs/${enc(id)}/timing/lines/${index}`, { method: 'PATCH', body: { delta } }))
    if (!v) return
    closePops()
    timing = v.lines
    const pushed = v.pushed ?? []
    store.notify(
      `已移動 ${delta > 0 ? '+' : ''}${delta} 秒` +
        (pushed.length ? `（第 ${[...pushed].sort((a, b) => a - b).map((k) => k + 1).join('、')} 句跟著往${pushed[0] < index ? '前' : '後'}挪）` : '') +
        '。重新製作後生效',
    )
  }

  function listen() {
    if (!time || !audio) return
    const start = Math.max(0, timing[time.index].start + time.delta - 1)
    const url = listenSource()
    if (!url) return
    ui.pauseStudio?.() // 疊在播放畫面上面時，先停掉背景的影片
    clearTimeout(listenTimer)
    const go = () => {
      audio!.currentTime = start
      audio!.play().catch(() => {})
      listenTimer = window.setTimeout(() => audio?.pause(), 5000)
    }
    if (!audio.src.endsWith(url)) {
      audio.src = url
      audio.addEventListener('loadedmetadata', go, { once: true })
      audio.load()
    } else go()
  }

  function closePops() {
    ruby = null
    time = null
    audio?.pause()
    clearTimeout(listenTimer)
  }

  /** 彈出框的位置：放在目標下方，放不下就放上方。 */
  function place(node: HTMLElement, target: HTMLElement) {
    const r = target.getBoundingClientRect()
    const left = Math.min(Math.max(8, r.left), window.innerWidth - node.offsetWidth - 8)
    const below = r.bottom + 8 + node.offsetHeight < window.innerHeight
    node.style.left = `${left}px`
    node.style.top = `${below ? r.bottom + 8 : Math.max(8, r.top - node.offsetHeight - 8)}px`
    node.querySelector('input')?.focus()
    node.querySelector('input')?.select()
  }

  function onKey(e: KeyboardEvent) {
    if (e.key !== 'Escape' || ui.song) return
    if (ruby || time) closePops()
    else close()
  }

  function onDown(e: MouseEvent) {
    const t = e.target as HTMLElement
    if (ruby && !t.closest('.popover') && !ruby.el.contains(t)) ruby = null
    if (time && !t.closest('.popover') && !time.el.contains(t)) closePops()
  }

  let qaChecks = $derived([...qa.values()].filter((c) => lines.some((l) => sameText(l.text, c.text))))
</script>

<svelte:window onkeydown={onKey} onmousedown={onDown} onbeforeunload={(e) => dirty && e.preventDefault()} />

<div class="overlay" role="presentation" onmousedown={(e) => e.target === e.currentTarget && close()}>
  <div class="sheet" role="dialog" aria-modal="true" aria-labelledby="editor-heading">
    <header class="sheet-head">
      <div class="grow">
        <h2 id="editor-heading">歌詞</h2>
        <div class="muted ellipsis">{song.artist ? `${song.artist} - ${song.title}` : song.title}</div>
      </div>
      {#if song.media.length && !overStudio}
        <button
          type="button"
          class="btn small"
          title="邊播放邊預覽與修改"
          onclick={async () => {
            if (dirty) {
              if (!confirm('歌詞有尚未儲存的修改，要先儲存再播放嗎？')) return
              if (!(await save())) return
            }
            ui.openStudio(id)
          }}><Icon name="play" />播放</button
        >
      {/if}
      <div class="seg-toggle">
        <button type="button" class:on={mode === 'visual'} title="點選切換演唱者、修改讀音" onclick={() => setMode('visual')}>標註</button>
        <button type="button" class:on={mode === 'annotated'} title="含演唱者標籤與每個漢字讀音的完整原文" onclick={() => setMode('annotated')}>標註原文</button>
        <button type="button" class:on={mode === 'plain'} title="只有歌詞本身的純文字" onclick={() => setMode('plain')}>原始歌詞</button>
      </div>
      <button type="button" class="icon-btn" title="關閉（Esc）" aria-label="關閉" onclick={() => close()}><Icon name="close" size={18} /></button>
    </header>
    <div class="sheet-body">
      {#if views?.paren.length}
        <div class="paren-bar">
          <span class="grow"
            >有 {views.paren.length} 處括號讀音（{views.paren.slice(0, 4).join('、')}{views.paren.length > 4 ? '…' : ''}），存檔時會轉成讀音標註。</span
          >
          <button type="button" class="btn small" onclick={parenFix}>現在轉換</button>
        </div>
      {/if}
      {#if song.approval.status === 'stale'}
        <div class="approval-note">
          <Icon name="warn" />
          {changed.size
            ? `改了第 ${[...changed].map((i) => i + 1).join('、')} 句，重新製作後看過再確認。`
            : '重新對時過，重新製作後看過再確認。'}
        </div>
      {/if}
      {#if mode === 'visual'}
        <div class="legend">
          <span><span class="singer-dot" data-singer="男"></span>男</span>
          <span><span class="singer-dot" data-singer="女"></span>女</span>
          <span><span class="singer-dot" data-singer="合"></span>合唱</span>
          <span class="sep"></span>
          <span><ruby class="auto">字<rt>じ</rt></ruby> 自動讀音</span>
          <span><ruby class="manual">字<rt>じ</rt></ruby> 手動指定</span>
          {#if pending.length}<span class="pending-readings">· {pending.length} 句的假名稍後補上</span>{/if}
        </div>
        {#if qaChecks.length}
          {@const wrong = qaChecks.filter((c) => c.status === 'wrong').length}
          <div class="qa-summary">
            <Icon name="warn" /><span
              >對時檢查：{[wrong ? `${wrong} 句可能不準` : '', qaChecks.length - wrong ? `${qaChecks.length - wrong} 句待確認` : ''].filter(Boolean).join('、')}。點 ⚠ 播放那句。</span
            >
          </div>
        {/if}
        <div class="lines">
          {#if loaded && !hasLines}
            <div class="empty-lyrics">還沒有歌詞。切到「原始歌詞」貼上歌詞，一行一句。</div>
          {/if}
          {#each lines as line, index}
            {#if line.kind === 'blank'}
              <div class="blank-line"></div>
            {:else if line.kind === 'comment'}
              <div class="comment-line">{line.text}</div>
            {:else}
              {@const no = lines.slice(0, index).filter((l) => l.kind === 'lyric').length}
              {@const check = qaFor(no, line.text)}
              {@const t = timingFor(no, line.text)}
              <div class="lyric-line {check ? `qa-${check.status}` : ''}" class:changed={changed.has(no)}>
                <span class="line-no">{no + 1}</span>
                <button
                  type="button"
                  class="singer"
                  data-singer={line.singer || undefined}
                  title="切換演唱者（Shift：整段）"
                  onclick={(e) => cycleSinger(index, e.shiftKey)}>{line.singer || '—'}</button
                >
                <div class="line-body">
                  <div class="line-text">
                    {#each line.segments ?? [] as seg}
                      {#if seg.ruby || seg.slot}
                        <!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role -->
                        <ruby
                          class={seg.ruby ? (seg.manual ? 'manual' : 'auto') : 'slot'}
                          class:editing={ruby?.seg === seg}
                          data-line={index}
                          data-start={seg.start}
                          title={seg.ruby ? '修改讀音' : '標讀音（Enter 接著標下一個字）'}
                          role="button"
                          tabindex="0"
                          onclick={(e) => openRuby(e.currentTarget as HTMLElement, index, seg)}
                          onkeydown={(e) => e.key === 'Enter' && openRuby(e.currentTarget as HTMLElement, index, seg)}>{seg.text}<rt>{seg.ruby || '·'}</rt></ruby
                        >
                      {:else}{seg.text}{/if}
                    {/each}
                  </div>
                  <input
                    class="trans-input"
                    value={line.translation ?? ''}
                    placeholder="中文翻譯（選填）"
                    spellcheck="false"
                    aria-label="中文翻譯"
                    oninput={(e) => {
                      lines[index].translation = (e.currentTarget as HTMLInputElement).value
                      markDirty()
                    }}
                  />
                </div>
                {#if check}
                  <button
                    type="button"
                    class="qa-flag"
                    title="{check.status === 'wrong' ? '可能不準' : '待確認'}：{check.reasons.join('；')}&#10;點一下跳到這句播放"
                    aria-label="播放這句"
                    onclick={() => (dirty && !confirm('歌詞有尚未儲存的修改，關掉編輯器會遺失，確定嗎？') ? null : ui.openStudio(id, { line: check.index }))}
                    ><Icon name="warn" /></button
                  >
                {/if}
                {#if t}
                  <button
                    type="button"
                    class="line-time"
                    class:editing={time?.index === t.index}
                    title="調整這句的開始時間"
                    onclick={(e) => {
                      closePops()
                      time = { index: t.index, el: e.currentTarget, delta: 0 }
                    }}>{fmtClock(t.start)}</button
                  >
                {/if}
              </div>
            {/if}
          {/each}
        </div>
      {:else}
        <textarea class="editor-text" spellcheck="false" bind:this={textarea} bind:value={text} oninput={markDirty}></textarea>
      {/if}
    </div>
    <footer class="sheet-foot">
      <div class="hint">{@html HINTS[mode]}</div>
      <div class="foot-actions">
        <span class="muted ellipsis">{status}</span>
        <button
          type="button"
          class="btn"
          onclick={async () => {
            const v = await save()
            if (v) {
              close(true)
              store.notify(`歌詞已儲存${convertedNote(v)}`)
            }
          }}>儲存</button
        >
        <button
          type="button"
          class="btn primary"
          onclick={async () => {
            const v = await save()
            if (!v) return
            if (!v.doc.lines.some((l) => l.kind === 'lyric')) return store.notify('歌詞是空的，無法製作伴唱帶', true)
            close(true)
            const res = await store.runJob([id], ['karaoke'])
            if (res?.created.length) store.notify(`已排入佇列：製作伴唱帶${convertedNote(v)}`)
          }}>儲存並製作伴唱帶</button
        >
      </div>
    </footer>
  </div>
</div>

{#if ruby}
  <div class="popover" use:place={ruby.el}>
    <div class="pop-base">{ruby.seg.text}</div>
    <input
      bind:value={ruby.value}
      placeholder="讀音"
      autocomplete="off"
      onkeydown={(e) => {
        if (e.key === 'Enter') {
          e.preventDefault()
          applyRuby(ruby!.value.trim(), true)
        }
      }}
    />
    <div class="pop-actions">
      <button type="button" class="btn small" disabled={!ruby.seg.manual} title="移除手動讀音，改回自動判斷" onclick={() => applyRuby('')}>還原自動</button>
      <button type="button" class="btn small primary" onclick={() => applyRuby(ruby!.value.trim())}>套用</button>
    </div>
  </div>
{/if}

{#if time}
  {@const t = timing[time.index]}
  <div class="popover time-pop" use:place={time.el}>
    <div class="pop-title">第 {time.index + 1} 句：{t.text}</div>
    <div class="time-now">
      開始 {fmtClock(t.start)}{#if time.delta}<span> → <b>{fmtClock(Math.max(0, t.start + time.delta))}</b>（{time.delta > 0 ? '+' : ''}{time.delta} 秒）</span>{/if}
    </div>
    <div class="nudge" role="group" aria-label="微調">
      {#each [-1, -0.5, -0.1, 0.1, 0.5, 1] as d}
        <button type="button" class="btn small" onclick={() => (time!.delta = Math.round((time!.delta + d) * 1000) / 1000)}>{d > 0 ? `+${d}` : `−${-d}`}</button>
      {/each}
    </div>
    <label class="time-delta"
      >移動 <input
        type="number"
        step="0.05"
        bind:value={time.delta}
        onkeydown={(e) => {
          if (e.key === 'Enter') {
            e.preventDefault()
            applyTime()
          }
        }}
      /> 秒<span class="muted">（負數往前）</span></label
    >
    {#if dirty}<div class="time-warn">歌詞還沒儲存，建議先儲存。</div>{/if}
    <div class="pop-actions">
      <button type="button" class="btn small" disabled={!listenSource()} title="從新的開始時間前 1 秒播放人聲" onclick={listen}>試聽</button>
      <button type="button" class="btn small primary" disabled={!time.delta} onclick={applyTime}>套用</button>
    </div>
  </div>
{/if}
<audio bind:this={audio} preload="none"></audio>
