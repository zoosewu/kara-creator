<script lang="ts">
  import { onMount } from 'svelte'
  import { api, enc } from '../lib/api'
  import { store } from '../lib/state.svelte'
  import { ui } from '../lib/ui.svelte'
  import { copyText, exportName, fmtClock, parseClock } from '../lib/util'
  import FolderSelect from './FolderSelect.svelte'
  import Icon from './Icon.svelte'

  let props: { id: string } = $props()
  // 一個元件只對應一首歌（App 用 {#key}）：關閉時外面會先把 ui.* 清空，props 也會跟著變空，所以開啟時就記下來
  // svelte-ignore state_referenced_locally
  const { id } = props
  // 對話框每次開啟時重新建立：取初始值是刻意的
  // svelte-ignore state_referenced_locally
  const song = store.songs.get(id)!

  const LANGUAGE_HINTS: Record<string, string> = {
    nan: '請在歌詞編輯器的「逐句編輯」替每個漢字標台羅（例：我 → guá），沒標的字會對得比較不準。',
    yue: '請在歌詞編輯器的「逐句編輯」替每個漢字標粵拼（例：我 → ngo5），沒標的字會對得比較不準。',
  }

  let folder = $state(song.folder)
  let title = $state(song.info.title)
  let artist = $state(song.info.artist)
  let note = $state(song.info.note)
  let translation = $state(song.info.translation)
  let prebuilt = $state(song.info.prebuilt ?? false)
  let language = $state(song.info.language)
  let link = $state(song.source.kind === 'url' ? (song.source.url ?? '') : song.info.link)
  // 剪頭尾：時間照沒剪的影片算；留白 = 不剪
  const trim0 = song.info.trim
  let trimStart = $state(trim0?.start ? fmtClock(trim0.start) : '')
  let trimEnd = $state(trim0?.end ? fmtClock(trim0.end) : '')
  let titleInput = $state<HTMLInputElement>()
  const downloaded = song.source.kind === 'url'

  // 手動欄位留空時用的值：歌詞檔的 # title 優先，其次是自動辨識
  const auto = { metadata: '影片的歌曲資訊', title: '影片標題', fallback: '影片標題（無法辨識格式）' }[song.guess.source] ?? '影片標題'
  const fbTitle = song.lyrics_meta.title || song.guess.title
  const fbArtist = song.lyrics_meta.artist || song.guess.artist
  const titleFrom = song.lyrics_meta.title ? '歌詞檔' : auto
  const artistFrom = song.lyrics_meta.artist ? '歌詞檔' : auto

  let exportPath = $derived.by(() => {
    const dir = folder ? store.pathOf(folder).map((f) => store.folderById(f)?.name).join('/') + '/' : ''
    return `${dir}${exportName(title.trim() || fbTitle, artist.trim() || fbArtist)}`
  })
  let languageChanged = $derived(language !== song.info.language && !['pending', 'no_lyrics'].includes(song.status.karaoke))

  // 觸控裝置不自動聚焦：鍵盤一跳出來會擋住一半的畫面
  onMount(() => {
    if (!matchMedia('(hover: none)').matches) titleInput?.focus()
  })

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    const body: Record<string, unknown> = { title: title.trim(), artist: artist.trim(), language, note: note.trim(), translation, prebuilt }
    if (!downloaded) body.link = link.trim()
    const start = parseClock(trimStart)
    const end = parseClock(trimEnd)
    if (start == null || end == null) {
      store.notify('剪頭尾的時間看不懂：請寫成「分:秒」或秒數，例如 1:23.5', true)
      return
    }
    if (start !== (trim0?.start ?? 0) || end !== (trim0?.end ?? 0)) body.trim = { start, end }
    const ok = await store.run(async () => {
      await api(`/songs/${enc(id)}`, { method: 'PATCH', body })
      if (folder !== song.folder) await api('/library/place', { method: 'POST', body: { kind: 'song', ids: [id], parent: folder, before: '' } })
      return true
    }, '已更新歌曲資訊')
    if (ok) ui.song = null
  }
</script>

<svelte:window onkeydown={(e) => e.key === 'Escape' && (ui.song = null)} />

<div class="overlay center" role="presentation" onmousedown={(e) => e.target === e.currentTarget && (ui.song = null)}>
  <!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role -->
  <form class="dialog" role="dialog" aria-modal="true" aria-labelledby="song-heading" onsubmit={submit}>
    <header class="dialog-head">
      <h2 id="song-heading">歌曲資訊</h2>
      <button type="button" class="icon-btn" aria-label="關閉" onclick={() => (ui.song = null)}><Icon name="close" size={18} /></button>
    </header>
    <div class="dialog-body">
      <div class="field-row">
        <label class="field" title="留空則用「{fbTitle}」（{titleFrom}）"
          >歌名<input bind:this={titleInput} bind:value={title} autocomplete="off" placeholder={fbTitle} /></label
        >
        <label class="field" title={fbArtist ? `留空則用「${fbArtist}」（${artistFrom}）` : ''}
          >演唱者<input bind:value={artist} autocomplete="off" placeholder={fbArtist || '（未填）'} /></label
        >
      </div>
      <label class="check-field" title="影片裡已經有字幕：不需要歌詞，去人聲後原影片的畫面加上伴奏就是伴唱帶"
        ><input type="checkbox" bind:checked={prebuilt} /> 本來就是卡拉 OK 版</label
      >
      {#if !prebuilt}
        <!-- 只在燒字幕時有作用：卡拉 OK 版的歌用不到（值保留，取消勾選就回來） -->
        <label class="field" title="顯示在歌曲開頭的標題畫面（歌名、演唱者下面）"
          >備註<input bind:value={note} autocomplete="off" placeholder="例如：作詞：○○／作曲：○○" /></label
        >
        <label class="check-field" title={song.translation_lines ? '顯示在畫面上方' : '在歌詞編輯器每句下面填'}
          ><input type="checkbox" bind:checked={translation} /> 燒上中文翻譯<span class="muted"
            >{song.translation_lines ? `（${song.translation_lines} 句）` : '（還沒有翻譯）'}</span
          ></label
        >
        <label class="field" title="依歌詞文字：假名 → 日文、漢字 → 國語、英文字母 → 英文"
          >語言
          <select bind:value={language}>
            <option value="">依歌詞文字</option>
            <option value="zh">國語</option>
            <option value="nan">台語</option>
            <option value="yue">粵語</option>
            <option value="ja">日文</option>
            <option value="en">英文</option>
          </select>
        </label>
        {#if LANGUAGE_HINTS[language] || languageChanged}
          <p class="hint">{LANGUAGE_HINTS[language] ?? ''}{languageChanged ? ' 改了語言要重新對時，已確認會失效。' : ''}</p>
        {/if}
      {/if}
      <div class="field-row" title="剪掉開頭、結尾的非音樂片段（時間照沒剪的影片算，例如 0:12.5）；也可以在播放畫面邊聽邊設定">
        <label class="field">伴唱帶從<input bind:value={trimStart} placeholder="開頭" inputmode="decimal" autocomplete="off" spellcheck="false" /></label>
        <label class="field">到<input bind:value={trimEnd} placeholder="結尾" inputmode="decimal" autocomplete="off" spellcheck="false" /></label>
      </div>
      <label class="field">資料夾<FolderSelect bind:value={folder} /></label>
      <label class="field" title={downloaded ? '從這個連結下載' : '補上原始連結，從備份重建時會用它重新下載'}
        >影片連結
        <span class="link-row">
          <input type="url" bind:value={link} readonly={downloaded} autocomplete="off" spellcheck="false" placeholder={downloaded ? '' : 'https://www.youtube.com/watch?v=…'} />
          <button type="button" class="btn small" disabled={!link.trim()} title="複製連結" onclick={async () => (await copyText(link.trim()), store.notify('已複製連結'))}>複製</button>
        </span>
      </label>
      <dl class="facts">
        <dt>匯出檔名</dt><dd class="mono">{exportPath}</dd>
        <dt>原始標題</dt><dd>{song.source.title}</dd>
        <dt>資料夾路徑</dt><dd class="mono">{song.path}</dd>
      </dl>
    </div>
    <footer class="dialog-foot">
      <button type="button" class="btn" onclick={() => (ui.song = null)}>取消</button>
      <button type="submit" class="btn primary">儲存</button>
    </footer>
  </form>
</div>
