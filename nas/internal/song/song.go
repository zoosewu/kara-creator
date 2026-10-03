// Package song 是每首歌的紀錄（songs/<id>/song.json，docs/v2/data.md「song.json」）。
package song

import (
	"fmt"
	"slices"
	"strings"

	"github.com/zoosewu/kara-creator/nas/internal/pystr"
)

// Version 是 song.json 的格式版本。
const Version = 2

// 檔名（都在 songs/<id>/ 底下）。
const (
	FileRecord    = "song.json"
	FileLyrics    = "lyrics.txt"
	FileVocals    = "vocals.flac"
	FileAlignment = "alignment.json"
	FileQA        = "qa.json"
	FileASS       = "karaoke.ass"
	SourceBase    = "source"       // source.<副檔名>
	InstrBase     = "instrumental" // instrumental.<來源副檔名>
)

// 來源種類。
const (
	KindURL   = "url"
	KindLocal = "local"
)

// 成品種類（Q13）。
const (
	TargetInstrumental = "instrumental" // 伴唱帶：伴奏 + 字幕
	TargetOriginal     = "original"     // 原曲 + 字幕（UI 沒有入口，只有 API）
)

// Targets 是所有成品種類。
var Targets = []string{TargetInstrumental, TargetOriginal}

// VideoFile 是成品影片的檔名。
func VideoFile(target string) string {
	if target == TargetOriginal {
		return "original.mp4"
	}
	return "karaoke.mp4"
}

// Languages 是可以手動指定的演唱語言。台語、粵語的歌詞文字和國語分不出來，只能手動指定。
var Languages = map[string]string{"zh": "國語", "nan": "台語", "yue": "粵語", "ja": "日文", "en": "英文"}

// Song 是 song.json 的內容。
type Song struct {
	Version int    `json:"version"`
	ID      string `json:"id"`
	Source  Source `json:"source"`
	Info    Info   `json:"info"`
	Stages  Stages `json:"stages"`
}

// Source 是來源影片或音訊。
type Source struct {
	Kind         string   `json:"kind"` // url | local
	URL          string   `json:"url,omitempty"`
	Extractor    string   `json:"extractor,omitempty"`
	VideoID      string   `json:"video_id,omitempty"`
	Title        string   `json:"title"` // 影片原始標題（手動放入時是檔名）
	Uploader     string   `json:"uploader,omitempty"`
	Channel      string   `json:"channel,omitempty"`
	Track        string   `json:"track,omitempty"`
	Artists      []string `json:"artists,omitempty"`
	Duration     float64  `json:"duration"`
	Mode         string   `json:"mode"` // video | audio
	OriginalName string   `json:"original_name,omitempty"`
	File         FileRef  `json:"file"`
	Width        int      `json:"width,omitempty"` // 純音訊為 0
	Height       int      `json:"height,omitempty"`
	AddedAt      string   `json:"added_at"`
	MetaVersion  int      `json:"meta_version,omitempty"`
}

// FileRef 記一個檔案的內容雜湊。MTimeNs 只用來決定要不要重算 sha256（大小或修改時間變了才重算），不進指紋。
type FileRef struct {
	Name    string `json:"name"` // songs/<id>/ 底下的檔名
	Size    int64  `json:"size"`
	MTimeNs int64  `json:"mtime_ns"`
	SHA256  string `json:"sha256"`
}

// Info 是使用者設定的歌曲資訊。
type Info struct {
	Title       string    `json:"title"`    // 空字串 = 自動（歌詞檔的 # title > 標題辨識）
	Artist      string    `json:"artist"`   // 同上
	Language    string    `json:"language"` // 空字串 = 依歌詞文字判斷
	Note        string    `json:"note"`     // 標題畫面第三行
	Link        string    `json:"link"`     // 手動放入的影片補上的原始連結
	Translation bool      `json:"translation"`
	Targets     []string  `json:"targets"`
	Approved    *Approval `json:"approved"` // 目前的確認；沒確認過或取消確認時為 null
	// History 是曾經確認過的紀錄（新的在後）。需要重新對時之後確認會失效，但紀錄留著，
	// 用來告訴使用者「上次確認之後歌詞改了哪幾句」，小改動不必整首重看。
	History []Approval `json:"approval_history,omitempty"`
}

// Approval 是一次「已確認」：綁對時（fingerprint.Approve），只需重燒的更新不影響確認。
type Approval struct {
	Fingerprint string   `json:"fingerprint"`
	At          string   `json:"at"`
	Texts       []string `json:"texts,omitempty"` // 確認當時每句的歌詞（之後比對改了哪幾句）
}

// Stages 是各階段的紀錄；nil 代表還沒做過。
type Stages struct {
	Separate *SeparateStage          `json:"separate,omitempty"`
	Align    *Stage                  `json:"align,omitempty"` // 對時本身在 alignment.json
	Render   map[string]*RenderStage `json:"render,omitempty"`
	QA       *Stage                  `json:"qa,omitempty"` // 檢查結果在 qa.json
}

// Stage 是一個階段完成時的紀錄。
type Stage struct {
	Key    string `json:"key"` // 當時的指紋
	Worker string `json:"worker,omitempty"`
	DoneAt string `json:"done_at"`
}

// SeparateStage 是去人聲的紀錄。
type SeparateStage struct {
	Stage
	Instrumental FileRef `json:"instrumental"`
	Vocals       FileRef `json:"vocals"`
}

// RenderStage 是一個成品的紀錄。Key 是燒錄時的成品指紋；Content 是當時「不看手動 ASS」的指紋：
// 使用者手改 ASS 之後，只要 Content 和現在的相同（對時、歌詞、標題畫面、樣式、字型都沒變）就沿用手改的 ASS，
// 否則重新產生（手改的內容會被取代，同 v1）。
type RenderStage struct {
	Stage
	Content string  `json:"content"`
	ASS     ASSRef  `json:"ass"`
	Video   FileRef `json:"video"`
}

// ASSRef 是成品的字幕。Manual = 使用者用 Aegisub 改過（Q15），之後燒錄改用這份、不重新產生。
type ASSRef struct {
	FileRef
	Manual bool `json:"manual"`
}

// New 建立新歌的紀錄（info 用預設值）。
func New(id string, src Source) *Song {
	return &Song{Version: Version, ID: id, Source: src,
		Info: Info{Translation: true, Targets: []string{TargetInstrumental}}}
}

// InfoUpdate 是要改的歌曲資訊；nil 代表不改。
type InfoUpdate struct {
	Title       *string   `json:"title,omitempty"`
	Artist      *string   `json:"artist,omitempty"`
	Language    *string   `json:"language,omitempty"`
	Note        *string   `json:"note,omitempty"`
	Link        *string   `json:"link,omitempty"`
	Translation *bool     `json:"translation,omitempty"`
	Targets     *[]string `json:"targets,omitempty"`
}

// ApplyInfo 驗證並套用歌曲資訊（v1 catalog.update_song 的規則）。出錯時不做任何修改。
func (s *Song) ApplyInfo(u InfoUpdate) error {
	next := s.Info
	if u.Title != nil {
		next.Title = pystr.JoinFields(*u.Title)
	}
	if u.Artist != nil {
		next.Artist = pystr.JoinFields(*u.Artist)
	}
	if u.Language != nil {
		if _, ok := Languages[*u.Language]; *u.Language != "" && !ok {
			return fmt.Errorf("不支援的語言：%s", *u.Language)
		}
		next.Language = *u.Language
	}
	if u.Link != nil {
		link := pystr.Strip(*u.Link)
		if link != "" && !strings.HasPrefix(link, "http://") && !strings.HasPrefix(link, "https://") {
			return fmt.Errorf("影片連結要是 http:// 或 https:// 開頭的網址")
		}
		next.Link = link
	}
	if u.Note != nil {
		next.Note = pystr.JoinFields(*u.Note) // 標題畫面只有一行，換行與多餘空白收成一個空白
	}
	if u.Translation != nil {
		next.Translation = *u.Translation
	}
	if u.Targets != nil {
		var targets []string
		for _, t := range Targets { // 依固定順序、去掉重複
			if slices.Contains(*u.Targets, t) {
				targets = append(targets, t)
			}
		}
		for _, t := range *u.Targets {
			if !slices.Contains(Targets, t) {
				return fmt.Errorf("不支援的成品種類：%s", t)
			}
		}
		if len(targets) == 0 {
			return fmt.Errorf("至少要做一種成品")
		}
		next.Targets = targets
	}
	s.Info = next
	return nil
}
