<script lang="ts">
  import { onMount } from 'svelte'
  import { api, type Font, type System } from '../lib/api'
  import { store } from '../lib/state.svelte'
  import { ui } from '../lib/ui.svelte'
  import Icon from './Icon.svelte'

  const BASE_RATIO = 0.075 // 預設字幕字高 / 畫面高（和 worker 的 subtitles.Style 一致）
  const LANGS: [string, string][] = [
    ['ja', '日文'],
    ['zh', '國語'],
    ['nan', '台語'],
    ['yue', '粵語'],
    ['en', '英文'],
    ['ko', '韓文'],
  ]

  let scale = $state(Math.round((store.settings.subtitle_scale ?? 1) * 100))
  let fonts = $state<Font[]>([])
  let chosen = $state<Record<string, string>>({ ...(store.settings.fonts ?? {}) })
  let exportOriginal = $state(store.settings.export_original ?? false)
  let system = $state<System | null>(null)
  let box = $state<HTMLDivElement>()
  let boxHeight = $state(0)

  onMount(() => {
    api<Font[]>('/fonts').then((f) => (fonts = f)).catch(() => {})
    api<System>('/system').then((s) => (system = s)).catch(() => {})
  })

  const close = () => (ui.settings = false)
  let made = $derived([...store.songs.values()].filter((s) => s.status.karaoke === 'done').length)
  let changed = $derived(Math.abs(scale / 100 - (store.settings.subtitle_scale ?? 1)) > 0.001)
  let fontChanged = $derived(LANGS.some(([l]) => (chosen[l] ?? '') !== (store.settings.fonts?.[l] ?? '')))
  let fs = $derived(boxHeight * BASE_RATIO * (scale / 100))

  function fontOf(lang: string): Font | undefined {
    const id = chosen[lang]
    if (id) return fonts.find((f) => f.id === id)
    return fonts.find((f) => f.default && f.full_name === ({ ja: 'Noto Sans CJK JP Bold', en: 'Noto Sans CJK JP Bold', ko: 'Noto Sans CJK KR Bold' }[lang] ?? 'Noto Sans CJK TC Bold'))
  }

  /** 前端預覽用的 @font-face（瀏覽器載入 NAS 上的同一個字型檔）。 */
  function face(f?: Font) {
    return f ? `@font-face { font-family: "kara-${f.sha256.slice(0, 12)}"; src: url("/fonts/${f.sha256}"); }` : ''
  }
  let preview = $derived(fontOf('ja'))

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    const fontBody: Record<string, string | null> = {}
    for (const [l] of LANGS) if ((chosen[l] ?? '') !== (store.settings.fonts?.[l] ?? '')) fontBody[l] = chosen[l] || null
    if (await store.attempt(() => api('/settings', { method: 'PATCH', body: { subtitle_scale: scale / 100, fonts: fontBody, export_original: exportOriginal } }), '已儲存設定')) close()
  }

  async function rollback() {
    const s = await store.run(() => api<System>('/system/ytdlp/rollback', { method: 'POST' }), '已換成上一版 yt-dlp')
    if (s) system = s
  }

  const gb = (n?: number) => (n ? `${(n / 1024 ** 3).toFixed(1)} GB` : '')
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && close()} />
<svelte:head>{@html `<style>${face(preview)}</style>`}</svelte:head>

<div class="overlay center" role="presentation" onmousedown={(e) => e.target === e.currentTarget && close()}>
  <!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role -->
  <form class="dialog" role="dialog" aria-modal="true" aria-labelledby="settings-heading" onsubmit={submit}>
    <header class="dialog-head">
      <h2 id="settings-heading">設定（所有歌）</h2>
      <button type="button" class="icon-btn" aria-label="關閉" onclick={close}><Icon name="close" size={18} /></button>
    </header>
    <div class="dialog-body">
      <label class="field"
        >字幕大小 <span class="scale-value">{scale}%</span>
        <input type="range" min="60" max="160" step="5" bind:value={scale} />
      </label>
      <div
        class="size-preview"
        aria-hidden="true"
        bind:this={box}
        bind:clientHeight={boxHeight}
        style="--fs:{fs}px;{preview ? `font-family:'kara-${preview.sha256.slice(0, 12)}', var(--lyric-font)` : ''}"
      >
        <div class="pv-trans">窗外開了一朵小花</div>
        <div class="pv-line pv-upper"><span class="pv-sung">窓の外に</span> 小さな花が咲いた</div>
        <div class="pv-line pv-lower">君の声は 私の灯り</div>
      </div>
      <p class="hint">
        歌詞、假名、翻譯與開頭標題畫面會一起縮放；一行放不下時會自動拆成兩行。{changed && made
          ? `儲存後已做好的 ${made} 首伴唱帶會顯示只需重燒（不會重新對時，已確認不受影響），可以用批次「製作伴唱帶」一次更新。`
          : ''}
      </p>

      <h3 class="field">字型</h3>
      {#each LANGS as [lang, label]}
        <label class="field"
          >{label}
          <select bind:value={chosen[lang]}>
            <option value={undefined}>預設（{fontOf(lang) && !chosen[lang] ? fontOf(lang)!.full_name : 'Noto Sans CJK'}）</option>
            {#each fonts as f}<option value={f.id}>{f.full_name || f.family}{f.weight >= 700 ? '' : '（非粗體）'}</option>{/each}
          </select>
        </label>
      {/each}
      <p class="hint">
        字型檔放在 NAS 曲庫的 fonts 資料夾。{fontChanged ? '換了字型的語言，做好的伴唱帶會顯示只需重燒（已確認不受影響）。' : ''}{fonts.length
          ? ''
          : ' 目前沒有找到字型檔。'}
      </p>

      <h3 class="field">匯出</h3>
      <label class="check-field"><input type="checkbox" bind:checked={exportOriginal} /> 也匯出原曲音訊</label>
      <p class="hint">
        已確認的歌除了伴唱帶，另外放一份原曲（有人聲）的音訊「歌手 - 歌名_original.m4a」在旁邊。{exportOriginal !==
        (store.settings.export_original ?? false)
          ? exportOriginal
            ? '下次按「匯出」時加上。'
            : '下次按「匯出」時把已經匯出的原曲音訊拿掉。'
          : ''}
      </p>

      {#if system}
        <h3 class="field">系統</h3>
        <dl class="facts">
          <dt>曲庫</dt><dd class="mono">{system.library}</dd>
          <dt>磁碟</dt><dd>{system.disk ? `剩 ${gb(system.disk.free)} / 共 ${gb(system.disk.total)}` : '—'}</dd>
          <dt>yt-dlp</dt><dd>{system.ytdlp || '找不到'}{#if system.ytdlp_prev}　<button type="button" class="view-link" title="新版下載失敗時，換回 {system.ytdlp_prev}" onclick={rollback}>退回上一版</button>{/if}</dd>
          <dt>版本</dt><dd class="mono">{Object.entries(system.versions).filter(([k]) => k !== '$schema').map(([k, v]) => `${k} ${v}`).join(' · ')}</dd>
        </dl>
      {/if}
    </div>
    <footer class="dialog-foot">
      <button type="button" class="btn" onclick={() => ((scale = 100), (chosen = {}))}>恢復預設</button>
      <span class="grow"></span>
      <button type="button" class="btn" onclick={close}>取消</button>
      <button type="submit" class="btn primary">儲存</button>
    </footer>
  </form>
</div>
