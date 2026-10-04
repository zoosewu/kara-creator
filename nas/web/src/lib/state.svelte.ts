// 全域狀態：曲庫、工作、AI 伺服器、設定，以及畫面的選取與收合。
// 初次載入 GET /library，之後靠 SSE（/api/v1/events）更新，不輪詢。
import { SvelteMap, SvelteSet } from 'svelte/reactivity'
import { api, type FolderView, type JobSummary, type Settings, type SongView, type Worker } from './api'
import { load, save } from './util'

type LibraryOut = { folders: FolderView[]; songs: SongView[]; settings: Settings; workers: Worker[]; jobs: JobSummary[] }

class Store {
  loaded = $state(false)
  connected = $state(true)
  folders = $state<FolderView[]>([])
  songs = new SvelteMap<string, SongView>()
  settings = $state<Settings>({ subtitle_scale: 1, fonts: {} } as Settings)
  jobs = $state<JobSummary[]>([])
  workers = $state<Worker[]>([])

  /** 選取的資料夾（新歌存入這裡）；空字串 = 最上層 */
  folder = $state<string>(load('folder', ''))
  collapsed = new SvelteSet<string>(load<string[]>('collapsed', []))
  selected = new SvelteSet<string>()
  lastPick: string | null = null
  selectedJob = $state<string | null>(null)
  /** 拖曳或原位編輯途中：先不要依伺服器的更新重排（會打斷操作） */
  busyUI = $state(false)

  toast = $state<{ text: string; error: boolean; id: number } | null>(null)
  private toastTimer = 0

  /** 其他元件想知道的事件（例如歌詞編輯器等假名補上） */
  private listeners = new Map<string, Set<(data: any) => void>>()

  // ---- 載入與即時更新 ----

  async reload() {
    const data = await api<LibraryOut>('/library')
    this.folders = data.folders
    this.songs.clear()
    for (const s of data.songs) this.songs.set(s.id, s)
    this.settings = data.settings
    this.workers = data.workers
    this.jobs = data.jobs
    if (this.folder && !this.folders.some((f) => f.id === this.folder)) this.selectFolder('')
    for (const id of [...this.selected]) if (!this.songs.has(id)) this.selected.delete(id)
    if (!this.selectedJob && this.jobs.length) this.selectedJob = this.jobs[0].id
    this.loaded = true
  }

  connect() {
    const es = new EventSource('/api/v1/events')
    let first = true
    es.addEventListener('hello', () => {
      this.connected = true
      if (!first) this.reload().catch(() => {}) // 斷線重連：重抓整個曲庫
      first = false
    })
    es.addEventListener('song', (e) => {
      const s = JSON.parse((e as MessageEvent).data) as SongView
      this.songs.set(s.id, s)
      this.emit('song', s)
    })
    es.addEventListener('library', () => this.reload().catch(() => {}))
    es.addEventListener('job', (e) => {
      const j = JSON.parse((e as MessageEvent).data) as JobSummary
      const i = this.jobs.findIndex((x) => x.id === j.id)
      if (i >= 0) this.jobs[i] = j
      else this.jobs = [j, ...this.jobs]
      this.emit('job', j)
    })
    es.addEventListener('job.log', (e) => this.emit('job.log', JSON.parse((e as MessageEvent).data)))
    es.addEventListener('worker', () => {
      api<Worker[]>('/workers').then((w) => (this.workers = w)).catch(() => {})
    })
    es.addEventListener('readings', (e) => this.emit('readings', JSON.parse((e as MessageEvent).data)))
    es.addEventListener('settings', (e) => (this.settings = JSON.parse((e as MessageEvent).data)))
    es.onerror = () => {
      this.connected = false // EventSource 會自己重連；連上時收到 hello
    }
  }

  on(kind: string, fn: (data: any) => void): () => void {
    if (!this.listeners.has(kind)) this.listeners.set(kind, new Set())
    this.listeners.get(kind)!.add(fn)
    return () => this.listeners.get(kind)!.delete(fn)
  }

  private emit(kind: string, data: unknown) {
    for (const fn of this.listeners.get(kind) ?? []) fn(data)
  }

  // ---- 提示 ----

  notify(text: string, error = false) {
    clearTimeout(this.toastTimer)
    this.toast = { text, error, id: Date.now() }
    this.toastTimer = window.setTimeout(() => (this.toast = null), error ? 5000 : 2500)
  }

  /** 執行一個不需要結果的動作；失敗時顯示錯誤，回傳是否成功。 */
  async attempt(fn: () => Promise<unknown>, ok?: string): Promise<boolean> {
    try {
      await fn()
      if (ok) this.notify(ok)
      return true
    } catch (e) {
      this.notify((e as Error).message, true)
      return false
    }
  }

  /** 執行一個動作並取得結果；失敗時顯示錯誤並回傳 undefined（只給有回傳值的動作用，沒有回傳值的用 attempt）。 */
  async run<T>(fn: () => Promise<T>, ok?: string): Promise<T | undefined> {
    try {
      const r = await fn()
      if (ok) this.notify(ok)
      return r
    } catch (e) {
      this.notify((e as Error).message, true)
      return undefined
    }
  }

  // ---- 曲庫的結構 ----

  folderById(id: string) {
    return this.folders.find((f) => f.id === id)
  }

  childFolders(parent: string): FolderView[] {
    return this.folders
      .filter((f) => f.parent === parent)
      .sort((a, b) => a.order - b.order || a.name.localeCompare(b.name) || a.id.localeCompare(b.id))
  }

  songsIn(folder: string): SongView[] {
    return [...this.songs.values()]
      .filter((s) => s.folder === folder)
      .sort((a, b) => a.order - b.order || a.id.localeCompare(b.id))
  }

  /** 由最上層到這個資料夾的路徑（資料夾 id）。 */
  pathOf(id: string): string[] {
    const out: string[] = []
    const seen = new Set<string>()
    for (let f = this.folderById(id); f && !seen.has(f.id); f = this.folderById(f.parent)) {
      seen.add(f.id)
      out.unshift(f.id)
    }
    return out
  }

  pathLabel(id: string): string {
    return id ? this.pathOf(id).map((x) => this.folderById(x)?.name).join(' › ') : '曲庫最上層'
  }

  /** 依畫面上的順序列出所有歌（含收合資料夾裡的）。 */
  songsInOrder(parent = ''): SongView[] {
    const out: SongView[] = []
    for (const f of this.childFolders(parent)) out.push(...this.songsInOrder(f.id))
    out.push(...this.songsIn(parent))
    return out
  }

  /** 資料夾裡（含所有子資料夾）的歌。 */
  songsUnder(folder: string): SongView[] {
    return this.songsIn(folder).concat(...this.childFolders(folder).map((f) => this.songsUnder(f.id)))
  }

  selectFolder(id: string) {
    this.folder = id
    if (id) this.collapsed.delete(id)
    this.remember()
  }

  toggleFolder(id: string) {
    if (this.collapsed.has(id)) this.collapsed.delete(id)
    else this.collapsed.add(id)
    this.remember()
  }

  remember() {
    save('folder', this.folder)
    save('collapsed', [...this.collapsed])
  }

  // ---- 勾選 ----

  pick(songs: SongView[], on: boolean, shift = false) {
    let targets = songs
    if (shift && songs.length === 1 && this.lastPick) {
      const order = this.songsInOrder()
      const a = order.findIndex((s) => s.id === this.lastPick)
      const b = order.findIndex((s) => s.id === songs[0].id)
      if (a >= 0 && b >= 0) targets = order.slice(Math.min(a, b), Math.max(a, b) + 1)
    }
    for (const s of targets) {
      if (on) this.selected.add(s.id)
      else this.selected.delete(s.id)
    }
    if (songs.length === 1) this.lastPick = songs[0].id
  }

  pickedSongs(): SongView[] {
    return this.songsInOrder().filter((s) => this.selected.has(s.id))
  }

  // ---- 工作 ----

  async runJob(songs: string[], steps: string[], extra: Record<string, unknown> = {}) {
    const res = await this.run(() =>
      api<{ created: JobSummary[]; skipped: Record<string, number>; errors?: string[] }>('/jobs', {
        method: 'POST',
        body: { songs, steps, ...extra },
      }),
    )
    if (res?.created[0]) this.selectedJob = res.created[0].id
    return res
  }
}

export const store = new Store()
