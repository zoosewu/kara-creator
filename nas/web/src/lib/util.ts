// 小工具：時間格式、輸出檔名預覽、資料夾路徑。

export function fmtDuration(seconds?: number | null): string {
  if (!seconds) return ''
  const s = Math.round(seconds)
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, '0')}`
}

export function fmtClock(sec: number): string {
  const m = Math.floor(sec / 60)
  return `${m}:${(sec - m * 60).toFixed(2).padStart(5, '0')}`
}

export function fmtTime(iso?: string | null): string {
  if (!iso) return ''
  return new Date(iso).toLocaleTimeString('zh-TW', { hour12: false, hour: '2-digit', minute: '2-digit' })
}

export function fmtDateTime(iso?: string | null): string {
  if (!iso) return ''
  const d = new Date(iso)
  return d.toLocaleString('zh-TW', { hour12: false, month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' })
}

/** 和後端 export.FileName 相同的規則，用來即時預覽輸出檔名。 */
export function exportName(title: string, artist: string): string {
  const raw = artist?.trim() ? `${artist.trim()} - ${title.trim()}` : title.trim()
  const name = raw.replace(/[<>:"/\\|?*]/g, '＿').replace(/[\x00-\x1f]/g, '').replace(/[ .]+$/, '')
  return `${Array.from(name).slice(0, 150).join('')}.mp4`
}

/** 比較時忽略空白（對時結果的文字和歌詞可能只差空白）。 */
export const sameText = (a: string, b: string) => a.replace(/\s/g, '') === b.replace(/\s/g, '')

export const LANGUAGE_NAMES: Record<string, string> = { zh: '國語', nan: '台語', yue: '粵語', ja: '日文', en: '英文' }
export const STEP_NAMES: Record<string, string> = {
  download: '下載', separate: '去人聲', karaoke: '製作伴唱帶', check: '檢查對時', retime: 'AI 重新對時', wrapup: '收尾',
}
export const SINGER_ORDER: (string | null)[] = [null, '男', '女', '合']

export function nextSinger(cur?: string | null): string | null {
  return SINGER_ORDER[(SINGER_ORDER.indexOf(cur ?? null) + 1) % SINGER_ORDER.length]
}

/** 本機儲存（無痕模式等存不了就算了）。 */
export function load<T>(key: string, fallback: T): T {
  try {
    const raw = localStorage.getItem(key)
    return raw === null ? fallback : (JSON.parse(raw) as T)
  } catch {
    return fallback
  }
}

export function save(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value))
  } catch {
    /* ignore */
  }
}

/** 複製文字到剪貼簿（區域網路的 http 頁面不能用 clipboard API，退回舊做法）。 */
export async function copyText(text: string) {
  try {
    await navigator.clipboard.writeText(text)
  } catch {
    const area = document.createElement('textarea')
    area.value = text
    document.body.append(area)
    area.select()
    document.execCommand('copy')
    area.remove()
  }
}
