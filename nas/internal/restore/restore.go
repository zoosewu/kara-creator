// Package restore 從資料備份（data/）重建曲庫（v1 scripts/restore.py；docs/v2/data.md「資料備份」）：
// 重新下載有連結的歌、放回歌詞與對時、套回歌曲資訊與資料夾。支援 v2 與 v1 格式的備份。
//
// 對時只在「歌詞、語言、方法都和對時當下相同」時沿用（寫成 alignment.json 的 restored），
// 製作伴唱帶時直接用、不重新對時；去人聲和成品照常重做（啟動伺服器後在 UI 批次「製作伴唱帶」）。
//
// 影片來源：
//   - 已經在曲庫裡的歌：直接用
//   - --sources 指定 v1 的下載資料夾（output/downloads）：用裡面已經下載好的檔案（依 download.json 對上），不重新下載
//   - 用網址下載的歌、手動放入但補了連結的歌：用連結下載（--replace 指定的替代網址優先）
//   - 手動放入、沒有連結的歌：列出原始檔名與大小；把同一個檔案放回 inbox/、伺服器匯入後再執行一次
//   - 換了來源（替代網址）時影片前奏可能不同，不沿用對時；歌詞與歌曲資訊照樣接上
package restore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zoosewu/kara-creator/nas/internal/backup"
	"github.com/zoosewu/kara-creator/nas/internal/download"
	"github.com/zoosewu/kara-creator/nas/internal/export"
	"github.com/zoosewu/kara-creator/nas/internal/fingerprint"
	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/lyrics"
	"github.com/zoosewu/kara-creator/nas/internal/media"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
	"github.com/zoosewu/kara-creator/nas/internal/timing"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// Options 是還原的選項。
type Options struct {
	Data      string            // data/ 的路徑
	Sources   string            // v1 的下載資料夾（output/downloads）；有的話優先用裡面的檔案，不重新下載
	Media     media.Tools       // 讀影片資訊（用 --sources 時需要）
	Overwrite bool              // 已經有的歌詞與對時也用備份覆蓋
	Replace   map[string]string // 歌曲 id → 替代影片的網址（原本的連結失效時）
	AlignVer  int               // 目前的 versions.align
	Log       func(string)
}

// Entry 是備份裡的一首歌（v1 與 v2 格式讀成同一個樣子）。
type Entry struct {
	ID      string
	Folder  string
	Order   int
	Info    song.Info
	Display backup.Display
	Source  backup.SourceJSON
	Lyrics  string // data/ 底下的相對路徑
	Timing  string
	V1      bool // v1 格式（確認紀錄沒辦法換算，不還原）
}

// Report 是還原的結果。
type Report struct {
	Folders      int
	Songs        int
	Downloaded   []string
	Imported     []string // 從 --sources 搬進來的
	Lyrics       int
	Timing       int
	Realign      []string  // 備份的對時不能沿用（方法改了、歌詞對不上、換了來源）
	Failed       []Problem // 下載失敗（連結可能失效）
	MissingLocal []Problem // 手動放入、沒有連結的歌
}

// Problem 是一首沒能還原的歌。
type Problem struct {
	Entry Entry
	URL   string
	Error string
}

// Describe 給人找替代影片用：歌名、演唱者、原始影片標題、長度。
func (p Problem) Describe() string {
	e := p.Entry
	name := e.Display.Title
	if e.Display.Artist != "" {
		name = e.Display.Artist + " - " + name
	}
	length := "長度不明"
	if d := int(e.Source.Duration); d > 0 {
		length = fmt.Sprintf("%d:%02d", d/60, d%60)
	}
	return fmt.Sprintf("[%s] %s｜原始標題：%s｜%s", e.ID, name, e.Source.Title, length)
}

// Run 還原。st 是已經開啟的曲庫；d 用來下載（nil 時不下載，只處理已經在曲庫裡的歌）。
func Run(ctx context.Context, st *store.Store, d *download.Downloader, opt Options) (Report, error) {
	logf := func(format string, a ...any) {
		if opt.Log != nil {
			opt.Log(fmt.Sprintf(format, a...))
		}
	}
	folders, entries, settings, err := Read(opt.Data)
	if err != nil {
		return Report{}, err
	}
	for id := range opt.Replace {
		if !hasEntry(entries, id) {
			return Report{}, fmt.Errorf("備份裡沒有這首歌：%s", id)
		}
	}
	var rep Report
	sources := map[string]v1Download{}
	if opt.Sources != "" {
		if sources, err = scanSources(opt.Sources); err != nil {
			return Report{}, err
		}
		logf("舊的下載資料夾：找到 %d 首", len(sources))
	}

	// 0. 全域設定、1. 資料夾（沿用備份裡的 id，結構與順序都一樣）
	err = st.EditLibrary(func(l *library.Library) error {
		if settings != nil {
			l.Settings.SubtitleScale = settings.SubtitleScale
			for k, v := range settings.Fonts {
				l.Settings.Fonts[k] = v
			}
		}
		for _, f := range folders {
			if _, ok := l.Folders[f.ID]; !ok {
				l.Folders[f.ID] = &library.Folder{ID: f.ID, Name: f.Name, Parent: val(f.Parent), Order: max(f.Order, 1)}
				rep.Folders++
			}
		}
		for _, f := range l.Folders { // 上層不見了的資料夾放到最上層
			if _, ok := l.Folders[f.Parent]; f.Parent != library.Root && !ok {
				f.Parent = library.Root
			}
		}
		return nil
	})
	if err != nil {
		return rep, err
	}
	logf("曲庫：%d 個資料夾、%d 首歌", len(folders), len(entries))

	// 2. 每首歌對應的影片：已經有的直接用，沒有的用連結下載（--replace > 原本的連結 > 補上的連結）
	for _, e := range entries {
		if ctx.Err() != nil {
			return rep, ctx.Err()
		}
		id, substituted := e.ID, false
		replace, replaced := opt.Replace[e.ID]
		if src, ok := sources[e.ID]; ok && !replaced {
			id = src.songID()
			if _, ok := st.Song(id); !ok {
				if err := adopt(ctx, st, opt.Media, id, src); err != nil {
					logf("[x] 搬移失敗：%s（%v）", src.path, err)
					rep.Failed = append(rep.Failed, Problem{Entry: e, URL: src.path, Error: err.Error()})
					continue
				}
				logf("[v] 用舊的檔案：%s", filepath.Base(src.path))
				rep.Imported = append(rep.Imported, id)
			}
		} else if _, ok := st.Song(id); !ok || replaced {
			url := replace
			if url == "" {
				url = first(e.Source.URL, e.Info.Link)
			}
			if url == "" {
				rep.MissingLocal = append(rep.MissingLocal, Problem{Entry: e})
				continue
			}
			if d == nil {
				rep.Failed = append(rep.Failed, Problem{Entry: e, URL: url, Error: "沒有下載工具"})
				continue
			}
			res, err := d.Download(ctx, download.Request{URL: url, AudioOnly: e.Source.Mode == "audio", Log: opt.Log})
			if err != nil {
				logf("[x] 下載失敗：%s（%v）", url, err)
				rep.Failed = append(rep.Failed, Problem{Entry: e, URL: url, Error: err.Error()})
				continue
			}
			logf("[v] %s：%s", map[bool]string{true: "已下載過", false: "下載完成"}[res.Skipped], res.Title)
			rep.Downloaded = append(rep.Downloaded, res.ID)
			substituted = res.ID != e.ID
			id = res.ID
		}
		if err := apply(st, id, e, substituted, opt, &rep, logf); err != nil {
			return rep, err
		}
		rep.Songs++
	}
	return rep, nil
}

// apply 把備份裡的歌曲資訊、資料夾、歌詞與對時套到曲庫裡的歌（id）上。
func apply(st *store.Store, id string, e Entry, substituted bool, opt Options, rep *Report, logf func(string, ...any)) error {
	err := st.EditSong(id, func(s *song.Song) error {
		info := e.Info
		if info.Targets == nil {
			info.Targets = []string{song.TargetInstrumental}
		}
		if substituted {
			info.Link, info.Approved = "", nil // 新影片本身就有連結；換了影片要重新確認
		}
		s.Info = info
		return nil
	})
	if err != nil {
		return err
	}
	if err := st.EditLibrary(func(l *library.Library) error {
		l.EnsureSong(id, library.Root)
		folder := e.Folder
		if _, ok := l.Folders[folder]; !ok {
			folder = library.Root
		}
		l.Songs[id].Folder, l.Songs[id].Order = folder, max(e.Order, 1)
		return nil
	}); err != nil {
		return err
	}

	// 歌詞
	var doc *lyrics.Document
	if e.Lyrics != "" {
		raw, err := os.ReadFile(filepath.Join(opt.Data, e.Lyrics))
		if err != nil {
			return err
		}
		parsed := lyrics.Parse(string(raw))
		doc = &parsed
		path := st.SongPath(id, song.FileLyrics)
		if _, err := os.Stat(path); opt.Overwrite || errors.Is(err, os.ErrNotExist) {
			if err := store.WriteFile(path, []byte(lyrics.Serialize(parsed))); err != nil {
				return err
			}
			rep.Lyrics++
		}
	}

	// 對時（換了來源的歌不沿用時間）
	if e.Timing == "" || doc == nil {
		return nil
	}
	if substituted {
		logf("  . %s：影片來源換過，製作時會重新對時（不沿用備份的時間）", e.Display.Title)
		rep.Realign = append(rep.Realign, e.ID)
		return nil
	}
	path := st.SongPath(id, song.FileAlignment)
	if _, err := os.Stat(path); !opt.Overwrite && err == nil {
		return nil
	}
	al, ok, err := readTiming(filepath.Join(opt.Data, e.Timing), *doc, e.V1)
	if err != nil {
		return err
	}
	if !ok || al.Restored.Method != opt.AlignVer {
		logf("  [!] %s：備份的對時方法較舊或歌詞對不上，製作時會重新對時", e.Display.Title)
		rep.Realign = append(rep.Realign, e.ID)
	}
	data, err := json.MarshalIndent(al, "", "  ")
	if err != nil {
		return err
	}
	if err := store.WriteFile(path, append(data, '\n')); err != nil {
		return err
	}
	rep.Timing++
	return nil
}

// readTiming 讀備份的對時，寫成「從資料備份還原」的 alignment.json。
// ok 為 false 代表歌詞和對時當下不同（製作時會重新對時）。
func readTiming(path string, doc lyrics.Document, v1 bool) (*timing.Alignment, bool, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, false, err
	}
	var t struct {
		backup.TimingJSON
		LyricsSHA1 string `json:"lyrics_sha1"` // v1
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, false, fmt.Errorf("%s：%w", path, err)
	}
	texts := doc.Texts()
	var rubies [][]lyrics.Ruby
	for _, ln := range doc.LyricLines() {
		rubies = append(rubies, ln.Rubies)
	}
	fp := fingerprint.Lyrics(texts, rubies)
	ok := t.LyricsFingerprint == fp
	if v1 {
		ok = t.LyricsSHA1 != "" && t.LyricsSHA1 == lyrics.V1AlignSHA1(doc)
	}
	restored := &timing.Restored{Language: t.Language, Method: t.Method}
	if ok {
		restored.Lyrics = fp // 對不上時留空：planner 不會沿用
	}
	var adj []timing.Adjustment
	if len(t.Adjustments) > 0 {
		_ = json.Unmarshal(t.Adjustments, &adj) // v1 的紀錄欄位相同；讀不懂就不留
	}
	lines := t.Lines
	if lines == nil {
		lines = []wp.Line{}
	}
	return &timing.Alignment{Lines: lines, Adjustments: adj, Restored: restored, Run: t.Run, Language: t.Language,
		Method: t.Method, Model: t.Model, Lyrics: restored.Lyrics}, ok, nil
}

// Read 讀備份（v2 或 v1 格式）。
func Read(dir string) ([]backup.FolderJSON, []Entry, *library.Settings, error) {
	raw, err := os.ReadFile(filepath.Join(dir, backup.SongsFile))
	if err != nil {
		return nil, nil, nil, fmt.Errorf("找不到備份：%w（先把資料 repo clone 到 %s）", err, dir)
	}
	var head struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return nil, nil, nil, err
	}
	var settings *library.Settings
	if s, err := os.ReadFile(filepath.Join(dir, backup.SettingsFile)); err == nil {
		settings = &library.Settings{SubtitleScale: 1, Fonts: map[string]string{}}
		_ = json.Unmarshal(s, settings) // v1 只有 subtitle_scale，欄位名稱相同
		if settings.SubtitleScale == 0 {
			settings.SubtitleScale = 1
		}
		if settings.Fonts == nil {
			settings.Fonts = map[string]string{}
		}
	}
	switch head.Version {
	case backup.Version:
		var doc backup.SongsJSON
		if err := json.Unmarshal(raw, &doc); err != nil {
			return nil, nil, nil, err
		}
		var entries []Entry
		for _, s := range doc.Songs {
			entries = append(entries, Entry{ID: s.ID, Folder: val(s.Folder), Order: s.Order, Info: s.Info, Display: s.Display,
				Source: s.Source, Lyrics: deref(s.Lyrics), Timing: deref(s.Timing)})
		}
		return doc.Folders, entries, settings, nil
	case 1:
		return readV1(raw, settings)
	}
	return nil, nil, nil, fmt.Errorf("不認得的備份格式版本：%d", head.Version)
}

func readV1(raw []byte, settings *library.Settings) ([]backup.FolderJSON, []Entry, *library.Settings, error) {
	var doc struct {
		Folders []backup.FolderJSON `json:"folders"`
		Songs   []struct {
			ID          string  `json:"id"`
			Folder      *string `json:"folder"`
			Order       int     `json:"order"`
			Title       string  `json:"title"`
			Artist      string  `json:"artist"`
			Language    string  `json:"language"`
			Link        string  `json:"link"`
			Note        string  `json:"note"`
			Translation *bool   `json:"translation"`
			Display     backup.Display
			Source      struct {
				URL       string  `json:"url"`
				Extractor string  `json:"extractor"`
				VideoID   string  `json:"video_id"`
				Title     string  `json:"title"`
				Uploader  string  `json:"uploader"`
				Duration  float64 `json:"duration"`
				Mode      string  `json:"mode"`
				File      string  `json:"file"`
				Size      int64   `json:"size"`
			} `json:"source"`
			Lyrics *string `json:"lyrics"`
			Timing *string `json:"timing"`
		} `json:"songs"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, nil, nil, err
	}
	var entries []Entry
	for _, s := range doc.Songs {
		translation := s.Translation == nil || *s.Translation
		kind := song.KindURL
		if s.Source.URL == "" {
			kind = song.KindLocal
		}
		entries = append(entries, Entry{ID: s.ID, Folder: val(s.Folder), Order: s.Order, V1: true, Display: s.Display,
			Info: song.Info{Title: s.Title, Artist: s.Artist, Language: s.Language, Note: s.Note, Link: s.Link,
				Translation: translation, Targets: []string{song.TargetInstrumental}},
			Source: backup.SourceJSON{Kind: kind, URL: s.Source.URL, Extractor: s.Source.Extractor, VideoID: s.Source.VideoID,
				Title: s.Source.Title, Uploader: s.Source.Uploader, Duration: s.Source.Duration, Mode: s.Source.Mode,
				OriginalName: s.Source.File, Size: s.Source.Size},
			Lyrics: deref(s.Lyrics), Timing: deref(s.Timing)})
	}
	sort.SliceStable(entries, func(a, b int) bool { return entries[a].ID < entries[b].ID })
	return doc.Folders, entries, settings, nil
}

func hasEntry(entries []Entry, id string) bool {
	for _, e := range entries {
		if e.ID == id {
			return true
		}
	}
	return false
}

func val(p *string) string {
	if p == nil {
		return library.Root
	}
	return *p
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// v1Download 是 v1 下載資料夾裡的一首（download.json）。
type v1Download struct {
	Extractor string   `json:"extractor"`
	ID        string   `json:"id"`
	URL       string   `json:"url"`
	Title     string   `json:"title"`
	Uploader  string   `json:"uploader"`
	Channel   string   `json:"channel"`
	Track     string   `json:"track"`
	Artists   []string `json:"artists"`
	Duration  float64  `json:"duration"`
	Mode      string   `json:"mode"`
	File      string   `json:"file"`
	path      string   // 影片檔的完整路徑
}

func (v v1Download) local() bool { return v.Extractor == "local" }

// songID 是 v2 曲庫裡的 id（YouTube 與手動放入的和 v1 相同；其他網站是「網站-影片 id」）。
func (v v1Download) songID() string {
	if v.local() {
		return v.ID
	}
	return download.SongID(v.Extractor, v.ID)
}

// scanSources 找出 dir 底下（一層子資料夾）每個 download.json，依 v1 的歌曲 id（影片 id / local-xxxxxxxx）索引。
func scanSources(dir string) (map[string]v1Download, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := map[string]v1Download{}
	for _, d := range entries {
		if !d.IsDir() {
			continue
		}
		folder := filepath.Join(dir, d.Name())
		raw, err := os.ReadFile(filepath.Join(folder, "download.json"))
		if err != nil {
			continue // 沒下載完的資料夾
		}
		var v v1Download
		if json.Unmarshal(raw, &v) != nil || v.ID == "" || v.File == "" {
			continue
		}
		v.path = filepath.Join(folder, v.File)
		if _, err := os.Stat(v.path); err != nil {
			continue
		}
		out[v.ID] = v
	}
	return out, nil
}

// adopt 把 v1 下載好的檔案放進 songs/<id>/source.<副檔名>（同一顆硬碟時 clone / 硬連結，不佔空間），寫 song.json。
func adopt(ctx context.Context, st *store.Store, tools media.Tools, id string, v v1Download) error {
	if err := os.MkdirAll(st.SongPath(id), 0o755); err != nil {
		return err
	}
	dst := st.SongPath(id, "source"+strings.ToLower(filepath.Ext(v.File)))
	if _, err := export.Place(v.path, dst); err != nil {
		return err
	}
	ref, err := store.Refresh(dst, song.FileRef{})
	if err != nil {
		return err
	}
	probe, err := tools.Probe(ctx, dst)
	if err != nil {
		return err
	}
	src := song.Source{Kind: song.KindURL, URL: v.URL, Extractor: v.Extractor, VideoID: v.ID, Title: v.Title,
		Uploader: v.Uploader, Channel: v.Channel, Track: v.Track, Artists: v.Artists,
		Duration: max(probe.Duration, v.Duration), Mode: "audio", File: ref, AddedAt: time.Now().Format(time.RFC3339),
		MetaVersion: download.MetaVersion}
	if v.local() {
		src = song.Source{Kind: song.KindLocal, Title: strings.TrimSuffix(v.File, filepath.Ext(v.File)), OriginalName: v.File,
			Duration: probe.Duration, Mode: "audio", File: ref, AddedAt: src.AddedAt}
	}
	if probe.HasVideo {
		src.Mode, src.Width, src.Height = "video", probe.Width, probe.Height
	}
	return st.AddSong(song.New(id, src), library.Root)
}

