// Package workerproto 是 NAS ↔ AI worker 協定（/worker/v1）的資料結構，NAS 端與假的 worker 共用。
// 規格見 docs/v2/worker-protocol.md；Python 的 worker 用同樣的 JSON 欄位名稱。
package workerproto

import (
	"encoding/json"

	kara "github.com/zoosewu/kara-creator"
)

// Prefix 是所有 worker 端點的路徑前綴。
const Prefix = "/worker/v1"

// 通道。
const (
	ChannelHeavy       = "heavy"       // GPU，一次一件
	ChannelInteractive = "interactive" // CPU（假名、讀音），可以和 heavy 同時跑
)

// 任務種類。
const (
	KindSeparate  = "separate"
	KindAlign     = "align"
	KindAlignFrom = "align_from"
	KindAlignLine = "align_line"
	KindQA        = "qa"
	KindReading   = "reading"
	KindRender    = "render"
)

// ChannelOf 回傳任務種類所屬的通道。
func ChannelOf(kind string) string {
	if kind == KindReading {
		return ChannelInteractive
	}
	return ChannelHeavy
}

// 優先順序（Q3），由高到低。
const (
	PriorityRealtime    = "realtime"    // 即時：reading
	PriorityInteractive = "interactive" // 互動：播放畫面的 AI 重對、單獨的檢查、使用者在等的重新燒錄
	PriorityBatch       = "batch"       // 批次：製作伴唱帶、批次重新處理
)

// ---- 連線 ----------------------------------------------------------------------

// Hardware 是 worker 自報的硬體資訊（只用來顯示）。
type Hardware struct {
	GPU   string `json:"gpu,omitempty"`     // 例如 "NVIDIA GeForce RTX 4070 Ti SUPER"；沒有 GPU 時為空
	VRAM  int64  `json:"vram_mb,omitempty"` // MB
	NVENC bool   `json:"nvenc"`
}

// Hello 是 POST /hello 的 body。
type Hello struct {
	Name     string        `json:"name"`
	Instance string        `json:"instance"` // 每次啟動不同的隨機 id
	Versions kara.Versions `json:"versions"`
	Hardware Hardware      `json:"hardware"`
	Kinds    []string      `json:"kinds"`    // 支援的任務種類
	Channels []string      `json:"channels"` // 開了哪些通道
	Cached   []string      `json:"cached"`   // 快取裡已有的輸入檔 sha256（最近的最多 500 個）
}

// HelloResponse 是 /hello 的回應。
type HelloResponse struct {
	Versions         kara.Versions `json:"versions"`
	HeartbeatSeconds int           `json:"heartbeat_seconds"` // worker 至少多久送一次 progress
	LeaseSeconds     int           `json:"lease_seconds"`     // 多久沒心跳就收回任務
}

// VersionMismatch 是版本不同時（HTTP 409）的回應。
type VersionMismatch struct {
	Detail string        `json:"detail"`
	Diff   []string      `json:"diff"` // 例如 ["align 7 ≠ 6"]（NAS 的版本 ≠ worker 的版本）
	NAS    kara.Versions `json:"nas"`
}

// LeaseRequest 是 POST /lease 的 body（long-poll，最多等 LeaseWaitSeconds 秒）。
type LeaseRequest struct {
	Instance string   `json:"instance"`
	Channel  string   `json:"channel"`
	Cached   []string `json:"cached,omitempty"` // 上次之後新增到快取的 sha256
}

// LeaseWaitSeconds 是 /lease 沒有任務時最多等多久才回 204。
const LeaseWaitSeconds = 25

// Blob 是一個輸入檔。
type Blob struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Task 是派給 worker 的任務。
type Task struct {
	ID       string          `json:"id"`
	Kind     string          `json:"kind"`
	Channel  string          `json:"channel"`
	Priority string          `json:"priority"`
	Song     string          `json:"song"` // 只用來寫紀錄
	Inputs   map[string]Blob `json:"inputs"`
	Params   json.RawMessage `json:"params"`
	Outputs  []string        `json:"outputs,omitempty"` // 允許上傳的檔名（separate、render）
}

// Progress 是 POST /tasks/{id}/progress 的 body（兼心跳）。
type Progress struct {
	Instance string   `json:"instance"`
	Progress *float64 `json:"progress"` // 0–1；不知道時為 null
	Logs     []string `json:"logs,omitempty"`
}

// ProgressResponse 是 /progress 的回應。
type ProgressResponse struct {
	Cancel bool `json:"cancel"`
}

// Uploaded 是 PUT /tasks/{id}/files/{name} 的回應。
type Uploaded struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Complete 是 POST /tasks/{id}/complete 的 body。
type Complete struct {
	Instance string            `json:"instance"`
	Result   json.RawMessage   `json:"result"`
	Files    map[string]string `json:"files,omitempty"` // 檔名 → sha256
}

// Fail 是 POST /tasks/{id}/fail 的 body。
type Fail struct {
	Instance  string `json:"instance"`
	Error     string `json:"error"` // 給使用者看的繁中說明
	Retryable bool   `json:"retryable"`
}

// Bye 是 POST /bye 的 body。
type Bye struct {
	Instance string `json:"instance"`
}

// ---- 歌詞與對時 ----------------------------------------------------------------

// Ruby 是一句裡手動標的讀音；Start、End 是以 Unicode code point 計的位置 [Start, End)（同 Python 的字串索引）。
type Ruby struct {
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Reading string `json:"reading"`
}

// Word 是對時結果的一個變色單位。
type Word struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
}

// Line 是對時結果的一句（alignment.json 的 lines）。
type Line struct {
	Text  string  `json:"text"`
	Start float64 `json:"start"`
	End   float64 `json:"end"`
	Words []Word  `json:"words"`
}

// ---- 各種任務的參數與結果 ------------------------------------------------------

// SeparateParams：inputs.audio 是 44.1kHz 立體聲 wav；上傳 vocals.wav、no_vocals.wav；結果 {}。
type SeparateParams struct {
	Model string `json:"model"` // htdemucs
	Stems int    `json:"stems"` // 2
}

// AlignParams：inputs.audio 是 speech.wav（16kHz 單聲道）。
type AlignParams struct {
	Texts    []string `json:"texts"`
	Rubies   [][]Ruby `json:"rubies"`
	Language string   `json:"language"`
	Model    string   `json:"model"`
}

// AlignResult 是 align、align_from 的結果（align_from 只含第 first 句之後，時間是整首的絕對時間）。
type AlignResult struct {
	Lines []Line `json:"lines"`
}

// AlignFromParams：從第 First 句（從 0 起算）開始重新對時，第 First 句從 Anchor 秒附近開始。
type AlignFromParams struct {
	AlignParams
	First  int     `json:"first"`
	Anchor float64 `json:"anchor"`
}

// AlignLineParams：只重對一句，範圍 [T0, T1)；T1 為 null 代表到結尾。
type AlignLineParams struct {
	Text     string   `json:"text"`
	Rubies   []Ruby   `json:"rubies"`
	T0       float64  `json:"t0"`
	T1       *float64 `json:"t1"`
	Language string   `json:"language"`
}

// AlignLineResult 是 align_line 的結果。
type AlignLineResult struct {
	Line Line `json:"line"`
}

// QAParams：inputs.audio 是 speech.wav。
type QAParams struct {
	Lines    []Line   `json:"lines"`
	Texts    []string `json:"texts"`
	Rubies   [][]Ruby `json:"rubies"`
	Language string   `json:"language"`
	Model    string   `json:"model"`
}

// QALine 是一句的檢查結果（v1 qa.check）。
type QALine struct {
	Index   int      `json:"index"`
	Text    string   `json:"text"`
	Start   float64  `json:"start"`
	End     float64  `json:"end"`
	Status  string   `json:"status"` // ok | suspect | wrong
	Reasons []string `json:"reasons"`
	Offset  *float64 `json:"offset"`
	Match   float64  `json:"match"`
}

// QAResult 是 qa 的結果。
type QAResult struct {
	Doc struct {
		Lines []QALine `json:"lines"`
	} `json:"doc"`
}

// ReadingParams：算自動讀音（假名）。只看句子文字，不看手動讀音——
// 結果才能只依「語言 + 句子 + 版本」快取；手動讀音由 NAS 合併（v1 reading.furigana 的合併規則）。
type ReadingParams struct {
	Language string   `json:"language"`
	Texts    []string `json:"texts"`
}

// Span 是自動判斷的一段讀音；Start、End 是 code point 位置。
type Span struct {
	Start int    `json:"start"`
	End   int    `json:"end"`
	Ruby  string `json:"ruby"`
}

// ReadingResult 是 reading 的結果：每句的自動讀音（v1 reading._auto_furigana）。
type ReadingResult struct {
	Lines [][]Span `json:"lines"`
}

// Font 是 render 用的字型。
type Font struct {
	SHA256 string `json:"sha256"`
	Family string `json:"family"` // ASS 的 Fontname
	Index  int    `json:"index"`  // .ttc 裡第幾個
}

// Encode 是燒錄的編碼器偏好。
type Encode struct {
	Prefer []string `json:"prefer"` // 例如 ["h264_nvenc", "libx264"]
}

// RenderParams：inputs.media 是伴奏影片（或原曲），inputs.ass（選填）是使用者手動改過的 ASS。
// 上傳 karaoke.ass（沒有手動 ASS 時）與 video.mp4。
type RenderParams struct {
	Target       string          `json:"target"` // instrumental | original
	Lines        []Line          `json:"lines"`
	Texts        []string        `json:"texts"`
	Rubies       [][]Ruby        `json:"rubies"`
	Language     string          `json:"language"`
	Singers      []*string       `json:"singers"`      // 句數和對時不符時為 []
	Translations []string        `json:"translations"` // 不燒時為 []
	TitleCard    []*string       `json:"title_card"`   // [歌名, 演唱者(, 備註)]；不顯示時 [null, null]
	Style        json.RawMessage `json:"style"`        // ass.Style 的欄位
	Font         Font            `json:"font"`
	Encode       Encode          `json:"encode"`
}

// RenderResult 是 render 的結果。
type RenderResult struct {
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	AssSHA256 string `json:"ass_sha256"`
}

// 上傳檔名。
const (
	FileVocals   = "vocals.wav"
	FileNoVocals = "no_vocals.wav"
	FileASS      = "karaoke.ass"
	FileVideo    = "video.mp4"
	InputAudio   = "audio"
	InputMedia   = "media"
	InputASS     = "ass"
	BearerPrefix = "Bearer "
)
