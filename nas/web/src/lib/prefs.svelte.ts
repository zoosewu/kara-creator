// 只記在這台瀏覽器的偏好（localStorage）：不影響伴唱帶與匯出，所以不存在 NAS 上。
import type { Mode, Track } from './playback'
import { load, save } from './util'

const KEYS = {
  newSongFolder: 'folder', // 新歌預設資料夾（設定）
  studioMode: 'studioMode', // 播放畫面預設字幕：模擬 / 燒錄（設定）
  studioTrack: 'studioTrack', // 播放畫面預設聲音：伴唱 / 人聲 / 原始影片（設定）
  studioFollow: 'studioFollow', // 播放畫面「選取跟著播放走」的預設（設定）
  volume: 'volume', // 以下記住上次的狀態
  muted: 'muted',
  rate: 'rate',
  audioOnly: 'audioOnly',
  editorMode: 'editorMode',
} as const

class Prefs {
  newSongFolder = $state<string>(load(KEYS.newSongFolder, ''))
  studioMode = $state<Mode>(load(KEYS.studioMode, 'burned'))
  studioTrack = $state<Track>(load(KEYS.studioTrack, 'instrumental'))
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
