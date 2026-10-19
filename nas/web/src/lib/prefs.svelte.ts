// 只記在這台瀏覽器的偏好（localStorage）：不影響伴唱帶與匯出，所以不存在 NAS 上。
import { load, save } from './util'

const KEYS = {
  newSongFolder: 'folder', // 新歌預設資料夾（設定）
  studioKind: 'studioKind', // 播放畫面預設版本（設定；'' = 最完整的版本）
  studioFollow: 'studioFollow', // 播放畫面「選取跟著播放走」的預設（設定）
  volume: 'volume', // 以下記住上次的狀態
  muted: 'muted',
  rate: 'rate',
  audioOnly: 'audioOnly',
  editorMode: 'editorMode',
} as const

class Prefs {
  newSongFolder = $state<string>(load(KEYS.newSongFolder, ''))
  studioKind = $state<string>(load(KEYS.studioKind, ''))
  studioFollow = $state<boolean>(load(KEYS.studioFollow, true))
  volume = $state<number>(load(KEYS.volume, 1))
  muted = $state<boolean>(load(KEYS.muted, false))
  rate = $state<number>(load(KEYS.rate, 1))
  audioOnly = $state<boolean>(load(KEYS.audioOnly, false))
  editorMode = $state<'visual' | 'annotated' | 'plain'>(load(KEYS.editorMode, 'visual'))

  set<K extends keyof typeof KEYS>(key: K, value: Prefs[K]) {
    ;(this as Record<K, Prefs[K]>)[key] = value
    save(KEYS[key], value)
  }
}

export const prefs = new Prefs()
