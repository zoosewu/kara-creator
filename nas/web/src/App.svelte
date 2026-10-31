<script lang="ts">
  import { onMount } from 'svelte'
  import { store } from './lib/state.svelte'
  import { ui } from './lib/ui.svelte'
  import { unsetSettings } from './lib/prefs.svelte'
  import Onboarding from './components/Onboarding.svelte'
  import Icon from './components/Icon.svelte'
  import Toast from './components/Toast.svelte'
  import Menu from './components/Menu.svelte'
  import NewSong from './components/NewSong.svelte'
  import Library from './components/Library.svelte'
  import Activity from './components/Activity.svelte'
  import SongDialog from './components/SongDialog.svelte'
  import FolderDialog from './components/FolderDialog.svelte'
  import SettingsDialog from './components/SettingsDialog.svelte'
  import LyricsEditor from './components/LyricsEditor.svelte'
  import Studio from './components/Studio.svelte'

  onMount(() => {
    store
      .reload()
      .then(() => {
        // 第一次使用，或之後新增了設定項目：請使用者設定一次（要先載入曲庫，才能選資料夾）
        const fresh = unsetSettings()
        if (fresh.length) ui.onboarding = fresh
      })
      .catch((e) => store.notify(e.message, true))
    store.connect()
  })

  function toggleTheme() {
    const next = document.documentElement.dataset.theme === 'dark' ? 'light' : 'dark'
    document.documentElement.dataset.theme = next
    try {
      localStorage.setItem('theme', next)
    } catch {
      /* ignore */
    }
  }
</script>

<header class="topbar">
  <div class="brand">kara</div>
  <span class="grow"></span>
  <button type="button" class="view-link" title="設定" onclick={() => (ui.settings = true)}><Icon name="gear" />設定</button>
  <button class="icon-btn" title="切換亮色 / 暗色" aria-label="切換亮色 / 暗色" onclick={toggleTheme}>
    <svg class="i-sun" width="18" height="18"><use href="#i-sun" /></svg>
    <svg class="i-moon" width="18" height="18"><use href="#i-moon" /></svg>
  </button>
</header>

<main class="layout">
  <NewSong />
  <div class="columns">
    <Library />
    <Activity />
  </div>
</main>

<!-- 順序就是疊放順序：歌詞編輯器、資訊可以疊在播放畫面上面 -->
{#if ui.studio}{#key ui.studio}<Studio id={ui.studio.id} opts={ui.studio.opts} />{/key}{/if}
{#if ui.editor}{#key ui.editor}<LyricsEditor id={ui.editor} />{/key}{/if}
{#if ui.song}{#key ui.song}<SongDialog id={ui.song} />{/key}{/if}
{#if ui.folder}<FolderDialog target={ui.folder} />{/if}
{#if ui.settings}<SettingsDialog />{/if}
{#if ui.onboarding}<Onboarding fresh={ui.onboarding} />{/if}

<Menu />
<Toast />
