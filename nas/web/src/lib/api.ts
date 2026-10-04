// REST API（/api/v1）的呼叫。型別由 docs/openapi.json 產生（npm run types）。
import type { components } from './api-types'

export type S = components['schemas']
export type SongView = S['SongView']
export type FolderView = S['FolderView']
export type JobSummary = S['Summary']
export type Worker = S['Worker']
export type Settings = S['Settings']
export type LyricsViews = S['LyricsViews']
export type Views = S['Views']
export type TimingView = S['TimingView']
export type QAView = S['QAView']
export type Doc = S['Document']
export type EditorLine = S['EditorLine']
export type Segment = S['Segment']
export type Font = S['Font']
export type System = S['System']

export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

/** 呼叫 API；失敗時丟出 ApiError，訊息是伺服器給的繁中說明（detail）。 */
export async function api<T = unknown>(path: string, init: { method?: string; body?: unknown } = {}): Promise<T> {
  const opts: RequestInit = { method: init.method ?? 'GET', headers: {} }
  if (init.body !== undefined) {
    ;(opts.headers as Record<string, string>)['Content-Type'] = 'application/json'
    opts.body = JSON.stringify(init.body)
  }
  let res: Response
  try {
    res = await fetch('/api/v1' + path, opts)
  } catch {
    throw new ApiError('連不到 NAS 伺服器', 0)
  }
  if (res.status === 204) return undefined as T
  const data = await res.json().catch(() => ({}))
  if (!res.ok) {
    const detail = typeof data.detail === 'string' && data.detail ? data.detail : `HTTP ${res.status}`
    const extra = Array.isArray(data.errors) && data.errors[0]?.message ? `（${data.errors[0].message}）` : ''
    throw new ApiError(detail + extra, res.status)
  }
  return data as T
}

export const enc = encodeURIComponent
