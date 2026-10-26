// 哪個對話框開著（一次一個主要畫面：資訊、資料夾、設定、歌詞、播放）。
// 播放畫面開著時，歌詞編輯器和資訊可以疊在上面（影片在背景繼續播），關掉後回到播放畫面。
import type { Mode, Track } from './playback'
import type { SettingKey } from './prefs.svelte'

export type StudioOpts = { mode?: Mode; track?: Track; line?: number | null; jumpToQa?: boolean; at?: number }

class UI {
  song = $state<string | null>(null) // 資訊對話框
  folder = $state<{ id: string | null; parent: string } | null>(null) // 資料夾對話框（id null = 新增）
  settings = $state(false)
  onboarding = $state<SettingKey[] | null>(null) // 第一次使用的設定（還沒設定過的項目）
  editor = $state<string | null>(null) // 歌詞編輯器
  studio = $state<{ id: string; opts: StudioOpts } | null>(null) // 播放畫面

  openStudio(id: string, opts: StudioOpts = {}) {
    this.editor = null
    this.studio = { id, opts }
  }

  /** 開歌詞編輯器；over 時疊在播放畫面上面（不關掉播放）。 */
  openEditor(id: string, { over = false } = {}) {
    if (!over) this.studio = null
    this.editor = id
  }

  /** 播放畫面登記的暫停：疊在上面的編輯器要試聽時先把背景的影片停下來。 */
  pauseStudio: (() => void) | null = null
}

export const ui = new UI()
