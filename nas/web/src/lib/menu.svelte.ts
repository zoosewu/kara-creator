// 共用的彈出選單。
export type MenuEntry = {
  label: string
  desc?: string
  icon?: string
  danger?: boolean
  disabled?: boolean
  indent?: number
  action: () => void
}

class MenuState {
  open = $state(false)
  title = $state('')
  entries = $state<MenuEntry[]>([])
  x = $state(0)
  y = $state(0)
  anchor: HTMLElement | null = null

  show(anchor: HTMLElement, title: string, entries: MenuEntry[]) {
    this.anchor = anchor
    this.title = title
    this.entries = entries
    this.open = true
    const r = anchor.getBoundingClientRect()
    this.x = r.right
    this.y = r.bottom
  }

  close() {
    this.open = false
    this.anchor = null
  }
}

export const menu = new MenuState()
