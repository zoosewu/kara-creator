<script lang="ts">
  // 只記在這台瀏覽器的設定欄位：設定對話框與第一次使用的對話框共用。
  // collapse：「新歌預設資料夾」以外的收在「其他設定」裡（第一次使用時），有新的項目時自動展開。
  import { api, type FolderView } from '../lib/api'
  import { MODES, TRACKS } from '../lib/playback'
  import type { SettingDraft, SettingKey } from '../lib/prefs.svelte'
  import { store } from '../lib/state.svelte'
  import FolderSelect from './FolderSelect.svelte'
  import Icon from './Icon.svelte'

  let { draft = $bindable(), fresh = [], collapse = false }: { draft: SettingDraft; fresh?: SettingKey[]; collapse?: boolean } = $props()

  const RATES: [number, string][] = [
    [0.5, '0.5×'],
    [0.75, '0.75×'],
    [1, '1×（原速）'],
  ]

  let creating = $state(false)
  let folderName = $state('')

  async function createFolder() {
    const name = folderName.trim()
    if (!name) return
    const f = await store.run(() => api<FolderView>('/folders', { method: 'POST', body: { name, parent: '' } }), `已新增資料夾「${name}」`)
    if (!f) return
    await store.reload().catch(() => {})
    draft.newSongFolder = f.id
    creating = false
    folderName = ''
  }

  // 第一次使用（新歌預設資料夾也還沒設過）：其他設定收起來；之後新增的項目：展開並標「新」
  // svelte-ignore state_referenced_locally
  const added = collapse && !fresh.includes('newSongFolder') ? fresh : []
  // svelte-ignore state_referenced_locally
  const othersOpen = !collapse || added.length > 0
  const isNew = (k: SettingKey) => added.includes(k)
</script>

{#snippet badge(k: SettingKey)}{#if isNew(k)}<span class="new-badge">新</span>{/if}{/snippet}

<div class="field" title="用網址新增的歌存在這裡（放進 inbox 的歌在曲庫最上層）">
  <span>新歌預設資料夾{@render badge('newSongFolder')}</span>
  {#if creating}
    <span class="link-row">
      <!-- svelte-ignore a11y_autofocus -->
      <input
        bind:value={folderName}
        placeholder="新資料夾的名稱"
        autocomplete="off"
        autofocus
        onkeydown={(e) => {
          if (e.key === 'Enter') (e.preventDefault(), createFolder())
          if (e.key === 'Escape') (e.stopPropagation(), (creating = false))
        }}
      />
      <button type="button" class="btn small primary" disabled={!folderName.trim()} onclick={createFolder}>建立</button>
      <button type="button" class="btn small" onclick={() => (creating = false)}>取消</button>
    </span>
  {:else}
    <span class="link-row">
      <FolderSelect bind:value={draft.newSongFolder} />
      <button type="button" class="btn small" title="在曲庫最上層建立新資料夾，並設成預設" onclick={() => (creating = true)}><Icon name="plus" />新資料夾</button>
    </span>
  {/if}
</div>

{#snippet others()}
  <div class="field-row" title="播放畫面打開時用這些；在播放畫面裡切換只影響那一次">
    <label class="field"
      ><span>播放畫面預設字幕{@render badge('studioMode')}</span>
      <select bind:value={draft.studioMode}>
        {#each MODES as [mode, label, hint]}<option value={mode} title={hint}>{label}</option>{/each}
      </select>
    </label>
    <label class="field"
      ><span>預設聲音{@render badge('studioTrack')}</span>
      <select bind:value={draft.studioTrack}>
        {#each TRACKS as [track, label]}<option value={track}>{label}</option>{/each}
      </select>
    </label>
  </div>
  <label class="field"
    ><span>預設播放速度{@render badge('studioRate')}</span>
    <select bind:value={draft.studioRate}>
      {#each RATES as [rate, label]}<option value={rate}>{label}</option>{/each}
    </select>
  </label>
  <label class="check-field"
    ><input type="checkbox" bind:checked={draft.studioFollow} /> 歌詞跟著播放捲動{@render badge('studioFollow')}</label
  >
{/snippet}

{#if collapse}
  <details class="more-prefs" open={othersOpen}>
    <summary>其他設定（之後也可以在「設定」裡改）</summary>
    <div class="more-prefs-body">{@render others()}</div>
  </details>
{:else}
  {@render others()}
{/if}
