// 只記在這台瀏覽器的偏好（localStorage）：不影響伴唱帶與匯出，所以不存在 NAS 上。
import type { Mode, Track } from './playback'
import { load, save } from './util'

const KEYS = {
  newSongFolder: 'folder', // 新歌預設資料夾（設定）
  studioMode: 'studioMode', // 播放畫面預設字幕：模擬 / 燒錄（設定）
  studioTrack: 'studioTrack', // 播放畫面預設聲音：伴唱 / 人聲 / 原始影片（設定）
  studioFollow: 'studioFollow', // 播放畫面「歌詞跟著播放捲動」的預設（設定）
  studioRate: 'studioRate', // 播放畫面的預設速度（設定；播放畫面裡調整只影響那一次）
  volume: 'volume', // 以下記住上次的狀態
  muted: 'muted',
  editorMode: 'editorMode',
} as const

class Prefs {
  newSongFolder = $state<string>(load(KEYS.newSongFolder, ''))
  studioMode = $state<Mode>(load(KEYS.studioMode, 'burned'))
  studioTrack = $state<Track>(load(KEYS.studioTrack, 'instrumental'))
  studioFollow = $state<boolean>(load(KEYS.studioFollow, true))
  studioRate = $state<number>(load(KEYS.studioRate, 1))
  volume = $state<number>(load(KEYS.volume, 1))
  muted = $state<boolean>(load(KEYS.muted, false))
  editorMode = $state<'visual' | 'annotated' | 'plain'>(load(KEYS.editorMode, 'visual'))

  set<K extends keyof typeof KEYS>(key: K, value: Prefs[K]) {
    ;(this as Record<K, Prefs[K]>)[key] = value
    save(KEYS[key], value)
  }
}

export const prefs = new Prefs()

/** 要使用者設定的項目（設定對話框的「這台瀏覽器」與第一次使用的對話框）；新增項目時加在這裡，使用者下次打開就會被問到。 */
export const SETTING_KEYS = ['newSongFolder', 'studioMode', 'studioTrack', 'studioFollow', 'studioRate'] as const
export type SettingKey = (typeof SETTING_KEYS)[number]
export type SettingDraft = { [K in SettingKey]: Prefs[K] }

export function settingDraft(): SettingDraft {
  return { newSongFolder: prefs.newSongFolder, studioMode: prefs.studioMode, studioTrack: prefs.studioTrack, studioFollow: prefs.studioFollow, studioRate: prefs.studioRate }
}

export function saveSettings(draft: SettingDraft) {
  for (const k of SETTING_KEYS) prefs.set(k, draft[k] as never)
}

/** 還沒設定過的項目（瀏覽器裡沒有存這個值）。瀏覽器不能存資料（例如無痕模式）時當作都設定過，免得每次都問。 */
export function unsetSettings(): SettingKey[] {
  try {
    return SETTING_KEYS.filter((k) => localStorage.getItem(KEYS[k]) === null)
  } catch {
    return []
  }
}
