<script lang="ts">
  // 第一次使用（或之後新增了設定項目）時跳出：只記在這台瀏覽器的設定，請使用者確認一次。
  import { saveSettings, settingDraft, type SettingKey } from '../lib/prefs.svelte'
  import { ui } from '../lib/ui.svelte'
  import BrowserPrefs from './BrowserPrefs.svelte'

  let { fresh }: { fresh: SettingKey[] } = $props()
  // svelte-ignore state_referenced_locally
  const first = fresh.includes('newSongFolder') // 新歌預設資料夾還沒設過 = 第一次使用
  let draft = $state(settingDraft())

  function done(e: SubmitEvent) {
    e.preventDefault()
    saveSettings(draft)
    ui.onboarding = null
  }
</script>

<div class="overlay center" role="presentation">
  <!-- svelte-ignore a11y_no_noninteractive_element_to_interactive_role -->
  <form class="dialog onboarding" role="dialog" aria-modal="true" aria-labelledby="onboarding-heading" onsubmit={done}>
    <header class="dialog-head">
      <h2 id="onboarding-heading">{first ? '開始使用前，先設定這台瀏覽器' : '有新的設定項目'}</h2>
    </header>
    <div class="dialog-body">
      <BrowserPrefs bind:draft {fresh} collapse />
    </div>
    <footer class="dialog-foot">
      <button type="submit" class="btn primary">{first ? '開始使用' : '完成'}</button>
    </footer>
  </form>
</div>
