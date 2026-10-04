<script lang="ts">
  import { onMount } from 'svelte'
  import { store } from './lib/state.svelte'
  import { ui } from './lib/ui.svelte'
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
    store.reload().catch((e) => store.notify(e.message, true))
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
  <div class="brand">kara <span class="muted">伴唱帶工作室</span></div>
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

{#if ui.song}{#key ui.song}<SongDialog id={ui.song} />{/key}{/if}
{#if ui.folder}<FolderDialog target={ui.folder} />{/if}
{#if ui.settings}<SettingsDialog />{/if}
{#if ui.editor}{#key ui.editor}<LyricsEditor id={ui.editor} />{/key}{/if}
{#if ui.studio}{#key ui.studio}<Studio id={ui.studio.id} opts={ui.studio.opts} />{/key}{/if}

<Menu />
<Toast />
