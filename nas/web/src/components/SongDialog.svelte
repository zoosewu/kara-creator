<script lang="ts">
  import { onMount } from 'svelte'
  import { api, enc } from '../lib/api'
  import { store } from '../lib/state.svelte'
  import { ui } from '../lib/ui.svelte'
  import { copyText, exportName } from '../lib/util'
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
    '': '依歌詞文字判斷（假名 → 日文、漢字 → 國語、英文字母 → 英文）。',
    nan: '台語用 CTC 對時（不經 Whisper）。請在歌詞的「標註」模式點每個漢字標上台羅（例如 我 → guá），沒標的字會用國語拼音代替、對得比較不準。對時檢查只做規則檢查。',
    yue: '粵語用 CTC 對時（不經 Whisper）。請在歌詞的「標註」模式點每個漢字標上粵拼（例如 我 → ngo5），沒標的字會用國語拼音代替、對得比較不準。對時檢查只做規則檢查。',
  }

  let folder = $state(song.folder)
  let title = $state(song.info.title)
  let artist = $state(song.info.artist)
  let note = $state(song.info.note)
  let translation = $state(song.info.translation)
  let language = $state(song.info.language)
  let link = $state(song.source.kind === 'url' ? (song.source.url ?? '') : song.info.link)
  let titleInput = $state<HTMLInputElement>()
  const downloaded = song.source.kind === 'url'

  // 手動欄位留空時用的值：歌詞檔的 # title 優先，其次是自動辨識
  const auto = { metadata: '影片的歌曲資訊', title: '影片標題', fallback: '影片標題（無法辨識格式）' }[song.guess.source] ?? '影片標題'
  const fbTitle = song.lyrics_meta.title || song.guess.title
  const fbArtist = song.lyrics_meta.artist || song.guess.artist
  const titleFrom = song.lyrics_meta.title ? '歌詞檔' : auto
  const artistFrom = song.lyrics_meta.artist ? '歌詞檔' : auto
  const from = titleFrom === artistFrom ? `來自${titleFrom}` : `歌名來自${titleFrom}、演唱者來自${artistFrom}`

  let exportPath = $derived.by(() => {
    const dir = folder ? store.pathOf(folder).map((f) => store.folderById(f)?.name).join('/') + '/' : ''
    return `${dir}${exportName(title.trim() || fbTitle, artist.trim() || fbArtist)}`
  })
  let languageChanged = $derived(language !== song.info.language && !['pending', 'no_lyrics'].includes(song.status.karaoke))

  onMount(() => titleInput?.focus())

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    const body: Record<string, unknown> = { title: title.trim(), artist: artist.trim(), language, note: note.trim(), translation }
    if (!downloaded) body.link = link.trim()
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
      <label class="field">資料夾<FolderSelect bind:value={folder} /></label>
      <label class="field">歌名<input bind:this={titleInput} bind:value={title} autocomplete="off" placeholder={fbTitle} /></label>
      <label class="field">演唱者<input bind:value={artist} autocomplete="off" placeholder={fbArtist || '（未填）'} /></label>
      <p class="hint">留空則自動使用「{fbTitle}{fbArtist ? ` / ${fbArtist}` : ''}」（{from}）。</p>
      <label class="field">備註<input bind:value={note} autocomplete="off" placeholder="例如：作詞：○○／作曲：○○" /></label>
      <p class="hint">顯示在開頭標題畫面的第三行（演唱者下面）；前奏太短（不到約 3 秒）時不會有標題畫面。</p>
      <label class="check-field"><input type="checkbox" bind:checked={translation} /> 燒上中文翻譯</label>
      <p class="hint">
        {song.translation_lines
          ? `歌詞裡有 ${song.translation_lines} 句翻譯。只顯示正在唱的那一句，放在畫面中央上方；切換後只重新燒錄，不會重新對時。`
          : '歌詞還沒有翻譯：在歌詞編輯器每句下面填翻譯（或在每句下一行寫「> 翻譯」）。'}
      </p>
      <label class="field"
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
      <p class="hint">{LANGUAGE_HINTS[language] ?? ''}{languageChanged ? ' 改了語言，伴唱帶會需要重新對時（已確認會失效）。' : ''}</p>
      <label class="field"
        >影片連結
        <span class="link-row">
          <input type="url" bind:value={link} readonly={downloaded} autocomplete="off" spellcheck="false" placeholder={downloaded ? '' : 'https://www.youtube.com/watch?v=…'} />
          <button type="button" class="btn small" disabled={!link.trim()} title="複製連結" onclick={async () => (await copyText(link.trim()), store.notify('已複製連結'))}>複製</button>
        </span>
      </label>
      <p class="hint">
        {downloaded ? '這首歌是從這個連結下載的（唯讀）。' : '手動放入的影片：可以補上原始影片的連結，從資料備份重做時會用它重新下載（換了來源會重新對時）。'}
      </p>
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
