<script lang="ts">
  import { api } from '../lib/api'
  import { prefs } from '../lib/prefs.svelte'
  import { store } from '../lib/state.svelte'

  let url = $state('')
  let audioOnly = $state(prefs.audioOnly)
  let prebuilt = $state(false) // 本來就是卡拉 OK 版：不用歌詞，去人聲後原影片畫面 + 伴奏就是伴唱帶
  let lyrics = $state('')
  let lyricsOpen = $state(false)

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    const make = (e.submitter as HTMLButtonElement | null)?.dataset.make === '1'
    const job = await store.run(
      () =>
        api<{ id: string }>('/songs', {
          method: 'POST',
          body: { url: url.trim(), folder: prefs.newSongFolder, audio_only: audioOnly, lyrics: lyrics.trim(), make, prebuilt },
        }),
      '已排入佇列',
    )
    if (job) {
      url = ''
      lyrics = ''
      lyricsOpen = false
      store.selectedJob = job.id
    }
  }
</script>

<section class="panel new-song">
  <form class="new-row" onsubmit={submit}>
    <input type="url" placeholder="貼上影片網址（YouTube 等）" required autocomplete="off" bind:value={url} />
    <label class="check"><input type="checkbox" bind:checked={audioOnly} onchange={() => prefs.set('audioOnly', audioOnly)} /> 只要音訊</label>
    <label class="check" title="影片裡已經有字幕：不需要歌詞，去人聲後原影片的畫面加上伴奏就是伴唱帶"
      ><input type="checkbox" bind:checked={prebuilt} /> 本來就是卡拉 OK 版</label
    >
    <button type="submit" class="btn" data-make="0" title="只下載，之後再逐步處理">僅下載</button>
    <button type="submit" class="btn primary" data-make="1" title={prebuilt ? '下載、去人聲：原影片的畫面加上伴奏就是伴唱帶' : '下載、去人聲，有歌詞就做出伴唱帶'}
      >製作伴唱帶</button
    >
  </form>
  <div class="new-foot">
    <details bind:open={lyricsOpen}>
      <summary>附上歌詞</summary>
      <textarea rows="6" spellcheck="false" placeholder="一行一句。也可以之後再從「歌詞」輸入。" bind:value={lyrics}></textarea>
    </details>
    <span class="muted">新歌存入：{store.pathLabel(prefs.newSongFolder)}</span>
  </div>
</section>
