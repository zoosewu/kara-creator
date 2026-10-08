// 拖曳：把歌或資料夾放進資料夾、在同一層之間插入。
import { api } from './api'
import { store } from './state.svelte'

type Drag = { type: 'folder' | 'song'; id: string; parent: string; group?: string[]; parents?: Set<string> }
type Zone = { mode: 'into'; folder: string } | { mode: 'before' | 'after'; parent: string; ref: string }
export type DropInfo = { kind: 'root' | 'folder' | 'song'; id?: string; parent?: string }

class DragState {
  drag = $state<Drag | null>(null)
  over = $state<{ key: string; cls: string } | null>(null) // 目前顯示放置提示的列
  private expandTimer = 0
  private expandFor: string | null = null

  start(e: DragEvent, type: 'folder' | 'song', id: string, parent: string) {
    if ((e.target as HTMLElement).closest('button, input, a')) {
      e.preventDefault()
      return
    }
    let group: string[] | undefined
    if (type === 'song' && store.selected.has(id) && store.selected.size > 1) group = store.pickedSongs().map((s) => s.id)
    this.drag = group
      ? { type, id, parent, group, parents: new Set(group.map((g) => store.songs.get(g)!.folder)) }
      : { type, id, parent }
    e.dataTransfer!.effectAllowed = 'move'
    e.dataTransfer!.setData('text/plain', id)
    if (group) {
      const ghost = document.createElement('div')
      ghost.className = 'drag-ghost'
      ghost.textContent = `${group.length} 首歌`
      document.body.append(ghost)
      e.dataTransfer!.setDragImage(ghost, 12, 12)
      setTimeout(() => ghost.remove())
    }
    document.body.classList.add('is-dragging')
    store.busyUI = true
  }

  end() {
    this.drag = null
    this.over = null
    clearTimeout(this.expandTimer)
    this.expandFor = null
    document.body.classList.remove('is-dragging')
    store.busyUI = false
  }

  isSource(type: string, id: string) {
    const d = this.drag
    if (!d) return false
    return d.group ? type === 'song' && d.group.includes(id) : d.type === type && d.id === id
  }

  private zone(e: DragEvent, node: HTMLElement, info: DropInfo): Zone | null {
    const d = this.drag
    if (!d) return null
    let z: Zone
    if (info.kind === 'root') z = { mode: 'into', folder: '' }
    else {
      const r = node.getBoundingClientRect()
      const y = (e.clientY - r.top) / r.height
      if (d.type === 'song') {
        z = info.kind === 'song' ? { mode: y < 0.5 ? 'before' : 'after', parent: info.parent!, ref: info.id! } : { mode: 'into', folder: info.id! }
      } else if (info.kind === 'folder') {
        z = y < 0.25 ? { mode: 'before', parent: info.parent!, ref: info.id! }
          : y > 0.75 ? { mode: 'after', parent: info.parent!, ref: info.id! } : { mode: 'into', folder: info.id! }
      } else {
        z = { mode: 'into', folder: info.parent! } // 資料夾拖到歌上：放進那首歌所在的資料夾
      }
    }
    return this.valid(d, z) ? z : null
  }

  private valid(d: Drag, z: Zone) {
    const parent = z.mode === 'into' ? z.folder : z.parent
    if (d.group) {
      if (z.mode === 'into') return !(d.parents!.size === 1 && d.parents!.has(parent))
      return !d.group.includes(z.ref)
    }
    if (z.mode === 'into' && parent === d.parent) return false
    if (z.mode !== 'into' && z.ref === d.id) return false
    if (d.type === 'folder' && (parent === d.id || store.pathOf(parent).includes(d.id))) return false
    return true
  }

  overRow(e: DragEvent, node: HTMLElement, key: string, info: DropInfo) {
    const z = this.zone(e, node, info)
    if (!z) return
    e.preventDefault()
    e.dataTransfer!.dropEffect = 'move'
    const cls = z.mode === 'into' ? 'drop-target' : `drop-${z.mode}`
    if (this.over?.key !== key || this.over.cls !== cls) this.over = { key, cls }
    // 停在收合的資料夾中段一下就自動展開
    const fid = z.mode === 'into' && info.kind === 'folder' ? z.folder : null
    if (fid && store.collapsed.has(fid)) {
      if (this.expandFor !== fid) {
        clearTimeout(this.expandTimer)
        this.expandFor = fid
        this.expandTimer = window.setTimeout(() => {
          store.collapsed.delete(fid)
          store.remember()
        }, 700)
      }
    } else {
      clearTimeout(this.expandTimer)
      this.expandFor = null
    }
  }

  leave(e: DragEvent, node: HTMLElement, key: string) {
    if (!node.contains(e.relatedTarget as Node) && this.over?.key === key) this.over = null
  }

  async drop(e: DragEvent, node: HTMLElement, info: DropInfo) {
    e.preventDefault()
    e.stopPropagation()
    const z = this.zone(e, node, info)
    const d = this.drag
    this.end()
    if (d && z) await this.apply(d, z)
  }

  /** 同一層、同一種項目（依順序），不含 exclude。 */
  private siblings(kind: 'folder' | 'song', parent: string, exclude: string[]) {
    const list = kind === 'folder' ? store.childFolders(parent).map((f) => f.id) : store.songsIn(parent).map((s) => s.id)
    return list.filter((x) => !exclude.includes(x))
  }

  private async apply(d: Drag, z: Zone) {
    const ids = d.group ?? [d.id]
    const parent = z.mode === 'into' ? z.folder : z.parent
    let before = ''
    if (z.mode !== 'into') {
      const sib = this.siblings(d.type, z.parent, ids)
      const i = sib.indexOf(z.ref)
      before = z.mode === 'before' ? z.ref : (sib[i + 1] ?? '')
    }
    const ok = await store.attempt(() =>
      api('/library/place', { method: 'POST', body: { kind: d.type, ids, parent, before, keep_present: z.mode === 'into' } }),
    )
    if (!ok) return
    if (parent) store.collapsed.delete(parent)
    store.remember()
    store.notify(z.mode === 'into' ? `已移到 ${store.pathLabel(parent)}` : '已調整順序')
  }
}

export const dnd = new DragState()

/** 批次「移動到…」：多首一起放進資料夾，彼此的先後保留。 */
export async function moveSongs(ids: string[], folder: string) {
  const before = ids.filter((id) => store.songs.get(id)?.folder === folder).length
  const ok = await store.attempt(() =>
    api('/library/place', { method: 'POST', body: { kind: 'song', ids, parent: folder, before: '', keep_present: true } }),
  )
  if (!ok) return
  if (folder) store.collapsed.delete(folder)
  store.remember()
  const moved = ids.length - before
  store.notify(moved ? `已把 ${moved} 首移到 ${store.pathLabel(folder)}` + (before ? `（${before} 首原本就在這裡）` : '') : `選取的歌都已經在 ${store.pathLabel(folder)}`)
}
