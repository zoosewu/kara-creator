<script lang="ts">
  // 原位編輯：預設填入目前的值並全選；Enter 或點別處儲存、Esc 取消；值沒變就不儲存。
  import { onMount } from 'svelte'
  import { store } from '../lib/state.svelte'

  let {
    value,
    placeholder,
    hint = '',
    onsave,
    ondone,
  }: { value: string; placeholder: string; hint?: string; onsave: (v: string) => Promise<unknown>; ondone: () => void } = $props()

  let input = $state<HTMLInputElement>()
  // 編輯開始時的值（之後外面的值變了也不影響正在編輯的內容）
  // svelte-ignore state_referenced_locally
  let text = $state(value)
  let finished = false

  onMount(() => {
    store.busyUI = true
    input?.focus()
    input?.select()
    return () => (store.busyUI = false)
  })

  async function finish(commit: boolean) {
    if (finished) return
    finished = true
    const next = text.trim()
    ondone()
    if (commit && next !== value.trim()) await store.attempt(() => onsave(next), '已更新')
  }
</script>

<input
  bind:this={input}
  class="inline-edit"
  bind:value={text}
  {placeholder}
  spellcheck="false"
  aria-label={placeholder}
  onkeydown={(e) => {
    if (e.key === 'Enter') {
      e.preventDefault()
      finish(true)
    }
    if (e.key === 'Escape') {
      e.preventDefault()
      e.stopPropagation()
      finish(false)
    }
  }}
  onblur={() => finish(true)}
  onclick={(e) => e.stopPropagation()}
  onmousedown={(e) => e.stopPropagation()}
  ondblclick={(e) => e.stopPropagation()}
/>
{#if hint}<span class="inline-edit-hint">{hint}</span>{/if}
