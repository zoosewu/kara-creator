// 哪個對話框開著（一次一個主要畫面：資訊、資料夾、設定、歌詞、播放）。
export type StudioOpts = { kind?: string; line?: number | null; jumpToQa?: boolean; at?: number }

class UI {
  song = $state<string | null>(null) // 資訊對話框
  folder = $state<{ id: string | null; parent: string } | null>(null) // 資料夾對話框（id null = 新增）
  settings = $state(false)
  editor = $state<string | null>(null) // 歌詞編輯器
  studio = $state<{ id: string; opts: StudioOpts } | null>(null) // 播放畫面

  openStudio(id: string, opts: StudioOpts = {}) {
    this.editor = null
    this.studio = { id, opts }
  }

  openEditor(id: string) {
    this.studio = null
    this.editor = id
  }
}

export const ui = new UI()
