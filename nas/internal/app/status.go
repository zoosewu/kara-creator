package app

import (
	"context"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/zoosewu/kara-creator/nas/internal/export"
	"github.com/zoosewu/kara-creator/nas/internal/jobs"
	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/planner"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
	"github.com/zoosewu/kara-creator/nas/internal/titles"
)

// SongView 是一首歌的摘要（曲庫畫面、/songs）。
type SongView struct {
	ID           string         `json:"id" doc:"歌曲 id：YouTube 影片 id、其他網站「網站-影片 id」、手動放入 local-xxxxxxxx"`
	Title        string         `json:"title" doc:"實際使用的歌名（手動 > 歌詞檔的 # title > 自動辨識）"`
	Artist       string         `json:"artist" doc:"實際使用的演唱者"`
	Guess        titles.Guess   `json:"guess" doc:"自動辨識的歌名與演唱者（資訊對話框的提示）"`
	Info         song.Info      `json:"info" doc:"使用者設定的歌曲資訊（空字串 = 自動）"`
	Language     string         `json:"language" doc:"實際的演唱語言（手動指定 > 依歌詞文字判斷）；判斷不出來時為空"`
	Source       SourceView     `json:"source"`
	Folder       string         `json:"folder" doc:"所在資料夾 id；最上層為空字串"`
	Order        int            `json:"order" doc:"同一層內的順序"`
	Status       planner.Status `json:"status"`
	Translations bool           `json:"has_translation" doc:"歌詞裡有翻譯"`
	Job          *jobs.Summary  `json:"job" doc:"還沒結束的工作（沒有時為 null）"`
	Approval     ApprovalView   `json:"approval"`
	Export       string         `json:"export" doc:"在匯出資料夾裡的相對路徑（不論伴唱帶做好了沒）"`
	Exported     string         `json:"exported" enum:"exported,pending," doc:"exported = 已匯出最新的成品；pending = 已確認但還沒匯出（或成品更新了）；空字串 = 沒確認，不匯出"`
	Media        []MediaView    `json:"media" doc:"可以播放的版本，第一個是預設（最終成品優先）"`
	Path         string         `json:"path" doc:"這首歌的資料夾（NAS 上的路徑，可以複製）"`
}

// ApprovalView 是「已確認」的詳細狀態。
type ApprovalView struct {
	Status  string `json:"status" enum:"approved,stale," doc:"approved = 已確認；stale = 曾經確認過，但之後重新對時了；空字串 = 沒確認過"`
	At      string `json:"at,omitempty" doc:"最近一次確認的時間（stale 時是失效前那次）"`
	Count   int    `json:"count" doc:"總共確認過幾次"`
	Changed []int  `json:"changed,omitempty" doc:"stale 時：和上次確認時相比，歌詞不同的句子（從 0 起算）；沒改歌詞（例如重新去人聲）時為空陣列"`
}

// SourceView 是來源資訊。
type SourceView struct {
	Kind     string  `json:"kind" enum:"url,local"`
	URL      string  `json:"url,omitempty"`
	Title    string  `json:"title" doc:"影片原始標題（手動放入時是檔名）"`
	Channel  string  `json:"channel,omitempty"`
	Duration float64 `json:"duration"`
	Mode     string  `json:"mode" enum:"video,audio"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	AddedAt  string  `json:"added_at"`
}

// MediaView 是一個可以播放的版本。
type MediaView struct {
	Kind  string `json:"kind" enum:"karaoke,karaoke_original,instrumental,vocals,source"`
	Label string `json:"label"`
	Hint  string `json:"hint"`
	URL   string `json:"url"`
}

var mediaLabels = map[string][2]string{
	"karaoke":          {"伴唱帶", "伴奏＋字幕（最終成品）"},
	"karaoke_original": {"原曲＋字幕", "原版＋字幕"},
	"instrumental":     {"伴奏", "去人聲後的伴奏"},
	"vocals":           {"人聲", "分離出來的人聲"},
	"source":           {"原始影片", "下載的原版"},
}

// invalidate 標記一首歌的狀態要重算，稍後送出 song 事件（短時間內的多次變動合併成一次）。
func (a *App) invalidate(id string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	delete(a.views, id)
	a.dirty[id] = true
	if a.flush == nil {
		a.flush = time.AfterFunc(200*time.Millisecond, a.flushDirty)
	}
}

func (a *App) invalidateAll() {
	a.mu.Lock()
	ids := make([]string, 0, len(a.views))
	for id := range a.views {
		ids = append(ids, id)
	}
	a.mu.Unlock()
	for _, id := range ids {
		a.invalidate(id)
	}
}

func (a *App) flushDirty() {
	a.mu.Lock()
	ids := make([]string, 0, len(a.dirty))
	for id := range a.dirty {
		ids = append(ids, id)
	}
	a.dirty, a.flush = map[string]bool{}, nil
	a.mu.Unlock()
	sort.Strings(ids)
	for _, id := range ids {
		if v, err := a.Song(id); err == nil {
			a.Events.Publish("song", v)
		}
	}
}

// Song 回傳一首歌的摘要（有快取，有變動時才重算）。
func (a *App) Song(id string) (*SongView, error) {
	a.mu.Lock()
	v, ok := a.views[id]
	a.mu.Unlock()
	if ok {
		return a.withJob(v), nil
	}
	v, err := a.compute(id)
	if err != nil {
		return nil, err
	}
	a.mu.Lock()
	if !a.dirty[id] {
		a.views[id] = v
	}
	a.mu.Unlock()
	return a.withJob(v), nil
}

// withJob 補上目前的工作（變化很快，不放進快取）與匯出狀態。
func (a *App) withJob(v *SongView) *SongView {
	cp := *v
	if s, ok := a.Jobs.Busy()[v.ID]; ok {
		cp.Job = &s
	}
	a.mu.Lock()
	cp.Export = a.names[v.ID]
	a.mu.Unlock()
	cp.Exported = ""
	if cp.Approval.Status == planner.Approved && cp.Export != "" {
		cp.Exported = "pending"
		if src := a.exportVideo(v.ID); src != "" && a.Exporter.Exported(cp.Export, src) {
			cp.Exported = "exported"
		}
	}
	return &cp
}

// Evaluate 讀檔並判斷一首歌的狀態（不經過快取）。
func (a *App) Evaluate(id string) (planner.Input, planner.Result, error) {
	in, err := planner.Inspect(a.Store, id, a.Store.Library().Settings, a.fontFor, a.Versions)
	if err != nil {
		return planner.Input{}, planner.Result{}, err
	}
	return in, planner.Evaluate(in), nil
}

func (a *App) compute(id string) (*SongView, error) {
	in, r, err := a.Evaluate(id)
	if err != nil {
		return nil, errNotFound
	}
	sg := in.Song
	v := &SongView{ID: id, Title: r.Title, Artist: r.Artist, Guess: r.Guess, Info: sg.Info, Language: r.Language,
		Status: r.Status, Path: a.Store.SongPath(id), Media: []MediaView{}}
	v.Approval = ApprovalView{Status: r.Status.Approval, Count: len(sg.Info.History), Changed: r.ChangedSinceApproval}
	if r.LastApproval != nil {
		v.Approval.At = r.LastApproval.At
	}
	if v.Info.Targets == nil {
		v.Info.Targets = []string{song.TargetInstrumental}
	}
	v.Source = SourceView{Kind: sg.Source.Kind, URL: sg.Source.URL, Title: sg.Source.Title, Channel: sg.Source.Channel,
		Duration: sg.Source.Duration, Mode: sg.Source.Mode, Width: sg.Source.Width, Height: sg.Source.Height, AddedAt: sg.Source.AddedAt}
	if place, ok := a.Store.Library().Songs[id]; ok {
		v.Folder, v.Order = place.Folder, place.Order
	}
	if in.Lyrics != nil {
		for _, ln := range in.Lyrics.LyricLines() {
			v.Translations = v.Translations || ln.Translation != ""
		}
	}
	// 可以播放的版本，依重要性排序（最終成品優先）
	add := func(kind, file string) {
		if file == "" {
			return
		}
		if _, err := os.Stat(a.Store.SongPath(id, file)); err != nil {
			return
		}
		l := mediaLabels[kind]
		v.Media = append(v.Media, MediaView{Kind: kind, Label: l[0], Hint: l[1], URL: "/media/" + id + "/" + file})
	}
	if rec := sg.Stages.Render[song.TargetInstrumental]; rec != nil {
		add("karaoke", rec.Video.Name)
	}
	if rec := sg.Stages.Render[song.TargetOriginal]; rec != nil {
		add("karaoke_original", rec.Video.Name)
	}
	if sep := sg.Stages.Separate; sep != nil && r.Status.Separate != planner.Pending {
		add("instrumental", sep.Instrumental.Name)
		add("vocals", sep.Vocals.Name)
	}
	add("source", sg.Source.File.Name)
	return v, nil
}

// Library 是曲庫畫面初次載入的資料。
type Library struct {
	Folders  []FolderView     `json:"folders"`
	Songs    []*SongView      `json:"songs"`
	Settings library.Settings `json:"settings"`
}

// FolderView 是一個資料夾。
type FolderView struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Parent string `json:"parent" doc:"上一層資料夾 id；最上層為空字串"`
	Order  int    `json:"order"`
}

// Library 回傳整個曲庫（資料夾、所有歌的摘要、設定）。
func (a *App) Library() Library {
	lib := a.Store.Library()
	out := Library{Folders: []FolderView{}, Songs: []*SongView{}, Settings: lib.Settings}
	for _, f := range lib.Folders {
		out.Folders = append(out.Folders, FolderView{f.ID, f.Name, f.Parent, f.Order})
	}
	sort.Slice(out.Folders, func(i, j int) bool { return out.Folders[i].ID < out.Folders[j].ID })
	var exp []export.Song
	for _, id := range a.Store.SongIDs() {
		v, err := a.Song(id)
		if err != nil {
			continue
		}
		out.Songs = append(out.Songs, v)
		exp = append(exp, export.Song{ID: id, Title: v.Title, Artist: v.Artist})
	}
	names := export.Names(lib, exp)
	a.mu.Lock()
	a.names = names
	a.mu.Unlock()
	for i, v := range out.Songs {
		out.Songs[i] = a.withJob(v)
	}
	return out
}

// ---- 檔案監看：使用者直接改了 songs/<id>/ 裡的檔案（例如用 Aegisub 改字幕、透過 SMB 改歌詞）--------

func (a *App) watchSongs(ctx context.Context) {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return
	}
	defer w.Close()
	root := a.Store.Path(store.SongsDir)
	_ = w.Add(root)
	for _, id := range a.Store.SongIDs() {
		_ = w.Add(a.Store.SongPath(id))
	}
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-w.Events:
			if !ok {
				return
			}
			rel, err := filepath.Rel(root, ev.Name)
			if err != nil || strings.HasPrefix(rel, "..") {
				continue
			}
			parts := strings.Split(filepath.ToSlash(rel), "/")
			if len(parts) == 1 && ev.Has(fsnotify.Create) {
				_ = w.Add(ev.Name) // 新的歌
				continue
			}
			name := parts[len(parts)-1]
			if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".tmp") || name == song.FileRecord {
				continue // 暫存檔；song.json 的變動 store 已經通知過
			}
			a.invalidate(parts[0])
		case <-w.Errors:
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
