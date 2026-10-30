// 播放畫面的版本 = 字幕（模擬 / 燒錄）× 聲音（伴唱 / 人聲 / 原始影片），共六種。
// 模擬：沒有字幕的畫面 + 網頁即時畫的字幕（調整後立刻看得到）；燒錄：伴唱帶裡燒好的字幕。
// 沒有現成檔案的組合（例如燒錄 + 人聲）：畫面靜音，另外播放那個聲音，以畫面的時間為準同步。
// 時間一律以來源（沒剪的影片）為準：剪過開頭的伴唱帶，影片的 0 秒是來源的第 offset 秒；其他檔案的 offset 都是 0。
import type { SongView } from './api'

export type Mode = 'live' | 'burned'
export type Track = 'instrumental' | 'vocals' | 'original'

export const MODES: [Mode, string, string][] = [
  ['live', '模擬', '網頁即時畫的字幕，調整後立刻看得到'],
  ['burned', '燒錄', '伴唱帶裡燒好的字幕（調整後要更新伴唱帶才會反映）'],
]
export const TRACKS: [Track, string][] = [
  ['instrumental', '伴唱'],
  ['vocals', '人聲'],
  ['original', '原始影片'],
]

/**
 * 畫面用哪個檔案、聲音另外用哪個檔案（null = 畫面本身的聲音）；沒有需要的檔案時為 null。
 * offset：畫面的 0 秒是來源的第幾秒（另外播放的聲音都是沒剪過的檔案，offset 為 0）。
 */
export type Plan = { picture: string; audio: string | null; offset: number }

export function plan(media: SongView['media'], mode: Mode, track: Track): Plan | null {
  const find = (kind: string) => media.find((m) => m.kind === kind) ?? null
  const url = (kind: string) => find(kind)?.url ?? null
  const source = url('source')
  const show = (kind: string, audio: string | null = null): Plan | null => {
    const m = find(kind)
    return m ? { picture: m.url, audio, offset: m.offset ?? 0 } : null
  }
  if (mode === 'burned') {
    if (!find('karaoke')) return null
    if (track === 'instrumental') return show('karaoke')
    if (track === 'original') return show('karaoke_original') ?? (source ? show('karaoke', source) : null)
    const vocals = url('vocals')
    return vocals ? show('karaoke', vocals) : null
  }
  if (track === 'instrumental') return show('instrumental')
  if (!source) return null
  if (track === 'original') return show('source')
  const vocals = url('vocals')
  return vocals ? show('source', vocals) : null
}

/** 想要的組合不能播時，找最接近的：先保留字幕方式換聲音，再換字幕方式。 */
export function choose(media: SongView['media'], mode: Mode, track: Track): [Mode, Track] | null {
  const modes: Mode[] = mode === 'burned' ? ['burned', 'live'] : ['live', 'burned']
  const tracks: Track[] = [track, ...TRACKS.map(([t]) => t).filter((t) => t !== track)]
  for (const m of modes) for (const t of tracks) if (plan(media, m, t)) return [m, t]
  return null
}

export const label = (mode: Mode, track: Track) =>
  `${MODES.find(([m]) => m === mode)![1]}・${TRACKS.find(([t]) => t === track)![1]}`
