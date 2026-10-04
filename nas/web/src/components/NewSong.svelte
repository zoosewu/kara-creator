<script lang="ts">
  import { api } from '../lib/api'
  import { store } from '../lib/state.svelte'

  let url = $state('')
  let audioOnly = $state(false)
  let lyrics = $state('')
  let lyricsOpen = $state(false)

  async function submit(e: SubmitEvent) {
    e.preventDefault()
    const make = (e.submitter as HTMLButtonElement | null)?.dataset.make === '1'
    const job = await store.run(
      () =>
        api<{ id: string }>('/songs', {
          method: 'POST',
          body: { url: url.trim(), folder: store.folder, audio_only: audioOnly, lyrics: lyrics.trim(), make },
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
    <label class="check"><input type="checkbox" bind:checked={audioOnly} /> 只要音訊</label>
    <button type="submit" class="btn" data-make="0" title="只下載，之後再逐步處理">僅下載</button>
    <button type="submit" class="btn primary" data-make="1" title="下載、去人聲，有歌詞就接著做出伴唱帶">製作伴唱帶</button>
  </form>
  <div class="new-foot">
    <details bind:open={lyricsOpen}>
      <summary>附上歌詞</summary>
      <textarea rows="6" spellcheck="false" placeholder="一行一句。也可以之後再從「歌詞」輸入。" bind:value={lyrics}></textarea>
    </details>
    <span class="muted">新歌存入：{store.pathLabel(store.folder)}</span>
  </div>
</section>
