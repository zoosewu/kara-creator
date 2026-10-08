package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	kara "github.com/zoosewu/kara-creator"
	"github.com/zoosewu/kara-creator/nas/internal/download"
	"github.com/zoosewu/kara-creator/nas/internal/jobs"
	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/lyrics"
	"github.com/zoosewu/kara-creator/nas/internal/planner"
	"github.com/zoosewu/kara-creator/nas/internal/pystr"
	"github.com/zoosewu/kara-creator/nas/internal/scheduler"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
	"github.com/zoosewu/kara-creator/nas/internal/timing"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// 錯誤種類（API 依種類決定 HTTP 狀態碼；訊息直接給使用者看）。
var (
	ErrNotFound = errors.New("")
	ErrInvalid  = errors.New("")
	ErrConflict = errors.New("")
)

// errNotFound 是找不到歌。
var errNotFound = fmt.Errorf("%w找不到這首歌", ErrNotFound)

func notFound(format string, a ...any) error {
	return fmt.Errorf("%w%s", ErrNotFound, fmt.Sprintf(format, a...))
}

func invalid(format string, a ...any) error {
	return fmt.Errorf("%w%s", ErrInvalid, fmt.Sprintf(format, a...))
}
func conflict(format string, a ...any) error {
	return fmt.Errorf("%w%s", ErrConflict, fmt.Sprintf(format, a...))
}

// libraryError 把曲庫的錯誤轉成 API 的錯誤種類。
func libraryError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, library.ErrNotFound):
		return notFound("%s", library.Message(err))
	case errors.Is(err, library.ErrInvalid):
		return invalid("%s", library.Message(err))
	}
	return err
}

// ---- 歌曲 ----------------------------------------------------------------------

// AddRequest 是新增歌曲（貼網址）。
type AddRequest struct {
	URL       string `json:"url" minLength:"1" doc:"影片網址"`
	Folder    string `json:"folder,omitempty" doc:"放進哪個資料夾（空字串 = 最上層）"`
	AudioOnly bool   `json:"audio_only,omitempty" doc:"只要音訊"`
	Lyrics    string `json:"lyrics,omitempty" doc:"一起附上的歌詞（選填）"`
	Make      bool   `json:"make,omitempty" doc:"一路做到伴唱帶（下載 → 去人聲 → 有歌詞就製作伴唱帶）；false = 僅下載"`
}

// AddSong 排入下載工作。
func (a *App) AddSong(req AddRequest) (jobs.Summary, error) {
	url := strings.TrimSpace(req.URL)
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return jobs.Summary{}, invalid("請貼 http:// 或 https:// 開頭的網址")
	}
	if req.Folder != "" {
		if _, ok := a.Store.Library().Folders[req.Folder]; !ok {
			return jobs.Summary{}, notFound("找不到資料夾")
		}
	}
	// YouTube 的網址不用連網就知道歌曲 id：按下去的當下就把歌詞存進曲庫（其他網站在下載時取得資訊後存）
	if id, ok := download.YouTubeID(url); ok {
		if err := a.Down.KeepLyrics(id, req.Lyrics, func(string, ...any) {}); err != nil {
			return jobs.Summary{}, err
		}
	}
	steps := []string{jobs.StepDownload}
	if req.Make {
		steps = append(steps, jobs.StepSeparate, jobs.StepKaraoke)
	}
	return a.Jobs.Submit(jobs.Request{Steps: steps, URL: url,
		Options: jobs.Options{AudioOnly: req.AudioOnly, Folder: req.Folder, Lyrics: req.Lyrics}})
}

// UpdateInfo 修改歌曲資訊（PATCH：只改有給的欄位）。
func (a *App) UpdateInfo(id string, u song.InfoUpdate) (*SongView, error) {
	err := a.Store.EditSong(id, func(s *song.Song) error {
		if err := s.ApplyInfo(u); err != nil {
			return invalid("%s", err.Error())
		}
		return nil
	})
	if errors.Is(err, store.ErrNoSong) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	a.touched()
	return a.Song(id)
}

// SetApproval 標記（或取消）「已確認成品沒問題」。只能確認已經做好、而且是最新的伴唱帶。
func (a *App) SetApproval(id string, approved bool) (*SongView, error) {
	var rec song.Approval
	if approved {
		in, r, err := a.Evaluate(id)
		if err != nil {
			return nil, errNotFound
		}
		if r.Status.Karaoke != planner.Done {
			return nil, conflict("伴唱帶還沒做好或需要更新，請先製作完成再確認")
		}
		rec = song.Approval{Fingerprint: r.ApproveFP, At: a.Now().Format(time.RFC3339), Texts: in.Lyrics.Texts()}
	}
	err := a.Store.EditSong(id, func(s *song.Song) error {
		if !approved {
			s.Info.Approved = nil // 取消確認：紀錄留著
			return nil
		}
		s.Info.Approved = &rec
		s.Info.History = append(s.Info.History, rec)
		if n := len(s.Info.History); n > 20 {
			s.Info.History = s.Info.History[n-20:] // 只留最近 20 次
		}
		return nil
	})
	if errors.Is(err, store.ErrNoSong) {
		return nil, errNotFound
	}
	if err != nil {
		return nil, err
	}
	a.touched()
	return a.Song(id)
}

// ---- 資料夾與排序 --------------------------------------------------------------

// AddFolder 新增資料夾（order 為 0 時排到最後）。
func (a *App) AddFolder(name, parent string, order int) (FolderView, error) {
	var f *library.Folder
	err := a.Store.EditLibrary(func(l *library.Library) error {
		var err error
		f, err = l.AddFolder("", name, parent, order)
		return err
	})
	if err != nil {
		return FolderView{}, libraryError(err)
	}
	a.touched()
	return FolderView{f.ID, f.Name, f.Parent, f.Order}, nil
}

// UpdateFolder 改名、改順序或移動資料夾。
func (a *App) UpdateFolder(id string, u library.FolderUpdate) (FolderView, error) {
	var f library.Folder
	err := a.Store.EditLibrary(func(l *library.Library) error {
		got, err := l.UpdateFolder(id, u)
		if err == nil {
			f = *got
		}
		return err
	})
	if err != nil {
		return FolderView{}, libraryError(err)
	}
	a.afterLibraryChange()
	return FolderView{f.ID, f.Name, f.Parent, f.Order}, nil
}

// DeleteFolder 刪除資料夾；裡面的子資料夾與歌曲移到上一層（不刪任何檔案）。
func (a *App) DeleteFolder(id string) error {
	if err := libraryError(a.Store.EditLibrary(func(l *library.Library) error { return l.DeleteFolder(id) })); err != nil {
		return err
	}
	a.afterLibraryChange()
	return nil
}

// PlaceRequest 是拖曳：把歌或資料夾放到某個資料夾、排在某個項目前面。
type PlaceRequest struct {
	Kind   string   `json:"kind" enum:"folder,song" doc:"拖曳的是資料夾還是歌"`
	IDs    []string `json:"ids" minItems:"1" doc:"要移動的項目；好幾首歌時依這個順序放，彼此的先後保留"`
	Parent string   `json:"parent" doc:"放進哪個資料夾（空字串 = 最上層）"`
	Before string   `json:"before,omitempty" doc:"排在這個項目前面；空字串 = 排到最後"`
	// KeepPresent：已經在目標資料夾的歌不動（拖進資料夾時）；false 時一起重新排位置（拖曳排序時）。
	KeepPresent bool `json:"keep_present,omitempty"`
}

// Place 拖曳排序與移動。
func (a *App) Place(req PlaceRequest) error {
	if req.Kind != library.KindFolder && req.Kind != library.KindSong {
		return invalid("kind 必須是 folder 或 song")
	}
	if contains(req.IDs, req.Before) {
		return invalid("插入位置不能是要移動的項目")
	}
	err := a.Store.EditLibrary(func(l *library.Library) error {
		for _, id := range req.IDs {
			if req.Kind == library.KindSong && req.KeepPresent {
				if p, ok := l.Songs[id]; ok && p.Folder == req.Parent {
					continue
				}
			}
			if err := l.Place(req.Kind, id, req.Parent, req.Before); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return libraryError(err)
	}
	a.afterLibraryChange()
	return nil
}

// afterLibraryChange：資料夾或順序變了，所有歌的摘要（所在資料夾、匯出路徑）都要更新。
func (a *App) afterLibraryChange() {
	a.invalidateAll()
	a.touched()
}

// ---- 時間 ----------------------------------------------------------------------

// TimingView 是每句目前的時間。
type TimingView struct {
	Lines       []TimingLine        `json:"lines" doc:"還沒對時時為空"`
	Adjustments []timing.Adjustment `json:"adjustments"`
	Pushed      []int               `json:"pushed,omitempty" doc:"這次一起被推動的句子（從 0 起算）"`
}

// TimingLine 是一句的時間。
type TimingLine struct {
	Index int       `json:"index"`
	Text  string    `json:"text"`
	Start float64   `json:"start"`
	End   float64   `json:"end"`
	Words []wp.Word `json:"words"`
}

// Timing 回傳每句目前的時間。
func (a *App) Timing(id string) (TimingView, error) {
	if _, ok := a.Store.Song(id); !ok {
		return TimingView{}, errNotFound
	}
	al, err := planner.ReadAlignment(a.Store, id)
	if err != nil {
		return TimingView{}, err
	}
	v := TimingView{Lines: []TimingLine{}, Adjustments: []timing.Adjustment{}}
	if al == nil {
		return v, nil
	}
	for i, ln := range al.Lines {
		words := ln.Words
		if words == nil {
			words = []wp.Word{}
		}
		v.Lines = append(v.Lines, TimingLine{Index: i, Text: timing.LineText(ln), Start: ln.Start, End: ln.End, Words: words})
	}
	if al.Adjustments != nil {
		v.Adjustments = al.Adjustments
	}
	return v, nil
}

// ShiftRequest 是移動一句。
type ShiftRequest struct {
	Delta     *float64 `json:"delta,omitempty" doc:"移動量（秒）；正數往後、負數往前"`
	Start     *float64 `json:"start,omitempty" doc:"新的開始時間（秒）；和 delta 擇一"`
	Following bool     `json:"following,omitempty" doc:"之後的句子一起移（UI 只移單句；整段偏掉交給 AI 重對）"`
}

// Shift 移動第 line 句（連鎖推動前後句）。只改對時，之後伴唱帶顯示需更新（只重燒）。
func (a *App) Shift(id string, line int, req ShiftRequest) (TimingView, error) {
	if err := a.notBusy(id); err != nil {
		return TimingView{}, err
	}
	al, err := planner.ReadAlignment(a.Store, id)
	if err != nil {
		return TimingView{}, err
	}
	if al == nil {
		return TimingView{}, invalid("還沒有對時結果，請先製作伴唱帶")
	}
	var delta float64
	switch {
	case req.Delta != nil:
		delta = *req.Delta
	case req.Start != nil && line >= 0 && line < len(al.Lines):
		delta = *req.Start - al.Lines[line].Start
	case req.Start == nil:
		return TimingView{}, invalid("要給 delta 或 start")
	}
	if math.IsNaN(delta) || math.IsInf(delta, 0) {
		return TimingView{}, invalid("移動量不對")
	}
	pushed, err := al.Shift(line, delta, req.Following, a.Now().Format(time.RFC3339))
	if err != nil {
		return TimingView{}, invalid("%s", err.Error())
	}
	if err := writeJSON(a.Store.SongPath(id, song.FileAlignment), al); err != nil {
		return TimingView{}, err
	}
	a.invalidate(id)
	a.touched()
	v, err := a.Timing(id)
	v.Pushed = pushed
	return v, err
}

// notBusy：這首歌正在處理時不能改時間或歌詞（處理完會蓋掉）。
func (a *App) notBusy(id string) error {
	if j, ok := a.Jobs.Busy()[id]; ok && j.Status == jobs.Running {
		return conflict("這首歌正在處理中（%s），請等處理完再修改", j.Stage)
	}
	return nil
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFile(path, append(data, '\n'))
}

// LineEdit 是播放畫面直接改一句。
type LineEdit struct {
	Text   *string `json:"text,omitempty" doc:"新的歌詞文字（改了之後要重新對時）"`
	Singer *string `json:"singer,omitempty" doc:"演唱者：男、女、合；空字串 = 不標"`
}

// EditLine 改第 n 句（要唱的句子，從 0 起算）的文字或演唱者，立刻存檔。
// 文字改了的話原字沒變的讀音保留（同「原始歌詞」轉回結構的規則）。
func (a *App) EditLine(id string, n int, e LineEdit) (LyricsViews, error) {
	if err := a.notBusy(id); err != nil {
		return LyricsViews{}, err
	}
	doc, err := planner.ReadLyrics(a.Store, id)
	if err != nil {
		return LyricsViews{}, err
	}
	if doc == nil {
		return LyricsViews{}, invalid("還沒有歌詞")
	}
	k, count := -1, 0
	for i, ln := range doc.Lines {
		if ln.Kind != lyrics.KindLyric {
			continue
		}
		if count == n {
			k = i
			break
		}
		count++
	}
	if k < 0 {
		return LyricsViews{}, invalid("沒有這一句")
	}
	ln := &doc.Lines[k]
	if e.Singer != nil {
		switch *e.Singer {
		case "":
			ln.Singer = nil
		case "男", "女", "合":
			s := *e.Singer
			ln.Singer = &s
		default:
			return LyricsViews{}, invalid("演唱者只能是 男、女、合")
		}
	}
	if e.Text != nil {
		text := pystr.JoinFields(*e.Text)
		if text == "" {
			return LyricsViews{}, invalid("歌詞不能是空的")
		}
		if strings.ContainsAny(text, "{}") {
			return LyricsViews{}, invalid("這裡只能改文字；讀音請到歌詞編輯器標註")
		}
		old, cur := []rune(ln.Text), []rune(text)
		kept := []lyrics.Ruby{}
		for _, r := range ln.Rubies {
			if r.Start >= 0 && r.Start < r.End && r.End <= len(old) && r.End <= len(cur) &&
				string(old[r.Start:r.End]) == string(cur[r.Start:r.End]) {
				kept = append(kept, r)
			}
		}
		ln.Text, ln.Rubies = text, kept
	}
	return a.SaveLyrics(id, lyrics.Serialize(*doc))
}

// QAView 是對時檢查結果（只列有疑慮的句子）。
type QAView struct {
	Checked   bool        `json:"checked" doc:"檢查過，而且對時之後沒改過"`
	CheckedAt string      `json:"checked_at,omitempty"`
	Lines     []wp.QALine `json:"lines"`
}

// QA 回傳對時檢查結果。
func (a *App) QA(id string) (QAView, error) {
	_, r, err := a.Evaluate(id)
	if err != nil {
		return QAView{}, errNotFound
	}
	v := QAView{Lines: []wp.QALine{}}
	if r.Status.QA == nil {
		return v, nil
	}
	doc, err := planner.ReadQA(a.Store, id)
	if err != nil || doc == nil {
		return v, err
	}
	v.Checked, v.CheckedAt = true, doc.CheckedAt
	for _, ln := range doc.Lines {
		if ln.Status != "ok" {
			v.Lines = append(v.Lines, ln)
		}
	}
	return v, nil
}

// ---- 工作 ----------------------------------------------------------------------

// JobRequest 是建立工作（一首或批次）。
type JobRequest struct {
	Songs       []string `json:"songs" minItems:"1" doc:"歌曲 id；好幾首時每首一件工作"`
	Steps       []string `json:"steps" minItems:"1" doc:"步驟：separate、karaoke、check、retime"`
	Force       bool     `json:"force,omitempty" doc:"全部重做（含覆蓋手改的字幕）"`
	Realign     bool     `json:"realign,omitempty" doc:"重新對時"`
	Line        int      `json:"line,omitempty" doc:"retime：第幾句（從 0 起算）"`
	Mode        string   `json:"mode,omitempty" enum:"from,line," doc:"retime：from = 這句及之後全部、line = 只重對這句"`
	OnlyNeeded  bool     `json:"only_needed,omitempty" doc:"批次：只做還沒做完的（已完成、缺歌詞、處理中的略過）"`
	Interactive bool     `json:"interactive,omitempty" doc:"使用者在畫面上等結果（AI 優先處理）；AI 重對一律是"`
}

// JobResult 是建立工作的結果。
type JobResult struct {
	Created []jobs.Summary `json:"created"`
	Skipped map[string]int `json:"skipped" doc:"略過的原因 → 首數"`
	Errors  []string       `json:"errors,omitempty"`
}

// SubmitJobs 建立工作。批次時略過不需要做的歌。
func (a *App) SubmitJobs(req JobRequest) (JobResult, error) {
	for _, s := range req.Steps {
		if s == jobs.StepDownload || !contains(jobs.Steps, s) {
			return JobResult{}, invalid("不認得的步驟：%s", s)
		}
	}
	if contains(req.Steps, jobs.StepRetime) {
		if _, ok := timing.RetimeModes[req.Mode]; !ok {
			return JobResult{}, invalid("AI 重對要指定 mode：from 或 line")
		}
		if len(req.Songs) != 1 {
			return JobResult{}, invalid("AI 重對一次只能一首")
		}
	}
	res := JobResult{Created: []jobs.Summary{}, Skipped: map[string]int{}}
	busy := a.Jobs.Busy()
	priority := wp.PriorityBatch
	if req.Interactive || contains(req.Steps, jobs.StepRetime) {
		priority = wp.PriorityInteractive
	}
	for _, id := range req.Songs {
		v, err := a.Song(id)
		if err != nil {
			res.Errors = append(res.Errors, id+"：找不到這首歌")
			continue
		}
		if req.OnlyNeeded {
			if reason := skipReason(v, req, busy); reason != "" {
				res.Skipped[reason]++
				continue
			}
		}
		s, err := a.Jobs.Submit(jobs.Request{Steps: req.Steps, Song: id, Title: v.Title, Priority: priority,
			Options: jobs.Options{Force: req.Force, Realign: req.Realign, Line: req.Line, Mode: req.Mode}})
		if err != nil {
			res.Errors = append(res.Errors, v.Title+"："+err.Error())
			continue
		}
		res.Created = append(res.Created, s)
	}
	return res, nil
}

func skipReason(v *SongView, req JobRequest, busy map[string]jobs.Summary) string {
	st := v.Status
	hasTiming := st.Karaoke == planner.Done || st.Karaoke == planner.NeedsRender || st.Karaoke == planner.NeedsAlign
	switch {
	case busy[v.ID].ID != "":
		return "處理中"
	case contains(req.Steps, jobs.StepKaraoke) && st.Lyrics != planner.Done:
		return "缺歌詞"
	case contains(req.Steps, jobs.StepCheck) && !hasTiming:
		return "還沒有伴唱帶"
	case req.Force || req.Realign:
		return ""
	case contains(req.Steps, jobs.StepKaraoke) && st.Karaoke == planner.Done,
		len(req.Steps) == 1 && req.Steps[0] == jobs.StepSeparate && st.Separate == planner.Done,
		len(req.Steps) == 1 && req.Steps[0] == jobs.StepCheck && st.QA != nil:
		return "已完成"
	}
	return ""
}

// ---- 設定與系統 ----------------------------------------------------------------

// SettingsUpdate 是修改全域設定（PATCH）。
type SettingsUpdate struct {
	SubtitleScale  *float64           `json:"subtitle_scale,omitempty" minimum:"0.6" maximum:"1.6" doc:"字幕大小（1 = 預設）"`
	Fonts          map[string]*string `json:"fonts,omitempty" doc:"語言 → 字型 id；null = 改回預設字型"`
	ExportOriginal *bool              `json:"export_original,omitempty" doc:"匯出時也匯出原曲音訊（m4a，檔名加 _original）"`
}

// UpdateSettings 修改全域設定。改了會影響成品的設定之後，做好的伴唱帶顯示需更新（只重燒）。
func (a *App) UpdateSettings(u SettingsUpdate) (library.Settings, error) {
	var out library.Settings
	err := a.Store.EditLibrary(func(l *library.Library) error {
		if u.SubtitleScale != nil {
			scale := math.Round(*u.SubtitleScale*100) / 100
			if scale < 0.6 || scale > 1.6 {
				return invalid("字幕大小要在 60%%–160%% 之間")
			}
			l.Settings.SubtitleScale = scale
		}
		if u.ExportOriginal != nil {
			l.Settings.ExportOriginal = *u.ExportOriginal
		}
		for lang, id := range u.Fonts {
			if _, ok := song.Languages[lang]; !ok && lang != "ko" {
				return invalid("不認得的語言：%s", lang)
			}
			if id == nil || *id == "" {
				delete(l.Settings.Fonts, lang)
				continue
			}
			if _, ok := a.Fonts.Get(*id); !ok {
				return invalid("找不到這個字型")
			}
			l.Settings.Fonts[lang] = *id
		}
		out = l.Settings
		return nil
	})
	if err != nil {
		return library.Settings{}, err
	}
	a.Events.Publish("settings", out)
	a.invalidateAll()
	a.touched()
	return out, nil
}

// System 是系統資訊。
type System struct {
	Versions  kara.Versions      `json:"versions" doc:"演算法 / 協定版本（versions.json）"`
	YTDLP     string             `json:"ytdlp" doc:"yt-dlp 的版本"`
	YTDLPPrev string             `json:"ytdlp_prev,omitempty" doc:"可以退回的上一版"`
	Library   string             `json:"library" doc:"曲庫的路徑"`
	Data      string             `json:"data" doc:"資料備份的路徑"`
	Disk      *Disk              `json:"disk" doc:"曲庫所在硬碟的用量"`
	Songs     int                `json:"songs"`
	Workers   []scheduler.Worker `json:"workers"`
	StartedAt string             `json:"started_at"`
}

// Disk 是硬碟用量（位元組）。
type Disk struct {
	Total uint64 `json:"total"`
	Free  uint64 `json:"free"`
}

// SystemInfo 回傳系統資訊。
func (a *App) SystemInfo() System {
	a.mu.Lock()
	ytdlp := a.ytdlp
	a.mu.Unlock()
	s := System{Versions: a.Versions, YTDLP: ytdlp, Library: a.Store.Root(), Data: a.Cfg.Data,
		Songs: len(a.Store.SongIDs()), Workers: a.Sched.Workers(), StartedAt: a.started.Format(time.RFC3339)}
	s.Disk = diskUsage(a.Store.Root())
	s.YTDLPPrev = a.Down.PrevVersion(context.Background())
	return s
}

// RollbackYTDLP 換回上一版 yt-dlp（新版下載失敗時用）。
func (a *App) RollbackYTDLP() (string, error) {
	if err := a.Down.Rollback(); err != nil {
		return "", invalid("%s", err.Error())
	}
	v, err := a.Down.Version(context.Background())
	if err == nil {
		a.setYTDLP(v)
	}
	return v, err
}
