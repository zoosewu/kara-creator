<script lang="ts">
  import { menu } from '../lib/menu.svelte'
  import Icon from './Icon.svelte'

  let el = $state<HTMLDivElement>()
  let left = $state(0)
  let top = $state(0)

  // 打開後量選單大小，放在按鈕下方（放不下就放上方），不超出畫面
  $effect(() => {
    if (!menu.open || !el || !menu.anchor) return
    const r = menu.anchor.getBoundingClientRect()
    const w = el.offsetWidth
    const h = el.offsetHeight
    left = Math.max(8, Math.min(r.right - w, window.innerWidth - w - 8))
    top = r.bottom + 6 + h < window.innerHeight ? r.bottom + 6 : Math.max(8, r.top - h - 6)
  })

  function onDown(e: MouseEvent) {
    if (!menu.open) return
    const t = e.target as Node
    if (el?.contains(t) || menu.anchor?.contains(t)) return
    menu.close()
  }
</script>

<svelte:window onmousedown={onDown} onscrollcapture={() => menu.close()} />

{#if menu.open}
  <div class="menu" role="menu" bind:this={el} style="left:{left}px;top:{top}px">
    <div class="menu-title">{menu.title}</div>
    {#each menu.entries as e}
      <button
        type="button"
        role="menuitem"
        class="menu-item"
        class:danger={e.danger}
        disabled={e.disabled}
        style={e.indent ? `padding-left:${10 + e.indent * 18}px` : undefined}
        onclick={() => {
          menu.close()
          e.action()
        }}
      >
        <Icon name={e.icon ?? 'play'} />
        <span class="label">{e.label}</span>
        <span class="desc">{e.desc ?? ''}</span>
      </button>
    {/each}
  </div>
{/if}
