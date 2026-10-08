// Package backup 是可重做的資料備份（docs/data.md「資料備份」）：
// 把歌單、影片連結、歌詞與對時寫到 data/（另一個私人 git repo），commit 並 push。
//
//	data/songs.json        曲庫資料夾、每首歌的資訊、影片連結與來源資訊
//	data/lyrics/<id>.txt   歌詞（含演唱者標籤與手動讀音）
//	data/timing/<id>.json  對時結果（含手動調整與 AI 重對），還原時直接沿用、不必重新對時
//	data/settings.json     全域設定
//
// 影片、伴奏、伴唱帶都不備份（有版權、也太大），還原時重新下載與製作。
package backup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/planner"
	"github.com/zoosewu/kara-creator/nas/internal/proc"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// 檔案與資料夾。
const (
	SongsFile    = "songs.json"
	SettingsFile = "settings.json"
	LyricsDir    = "lyrics"
	TimingDir    = "timing"
	Version      = 2
)

// SongsJSON 是 songs.json。
type SongsJSON struct {
	Version int          `json:"version"`
	Folders []FolderJSON `json:"folders"`
	Songs   []SongJSON   `json:"songs"`
}

// FolderJSON 是一個資料夾。
type FolderJSON struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Parent *string `json:"parent"`
	Order  int     `json:"order"`
}

// SongJSON 是一首歌的備份。
type SongJSON struct {
	ID      string     `json:"id"`
	Folder  *string    `json:"folder"`
	Order   int        `json:"order"`
	Info    song.Info  `json:"info"`
	Display Display    `json:"display"` // 實際使用的歌名與演唱者（連結失效時找替代影片用）
	Source  SourceJSON `json:"source"`
	Lyrics  *string    `json:"lyrics"` // lyrics/<id>.txt；沒有歌詞時為 null
	Timing  *string    `json:"timing"` // timing/<id>.json；還沒對時為 null
}

// Display 是實際使用的歌名與演唱者。
type Display struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
}

// SourceJSON 是來源資訊（去掉本機才有意義的欄位；手動放入的記原始檔名與大小，放回同一個檔案就是同一個 id）。
type SourceJSON struct {
	Kind         string   `json:"kind"`
	URL          string   `json:"url,omitempty"`
	Extractor    string   `json:"extractor,omitempty"`
	VideoID      string   `json:"video_id,omitempty"`
	Title        string   `json:"title"`
	Uploader     string   `json:"uploader,omitempty"`
	Channel      string   `json:"channel,omitempty"`
	Track        string   `json:"track,omitempty"`
	Artists      []string `json:"artists,omitempty"`
	Duration     float64  `json:"duration"`
	Mode         string   `json:"mode"`
	OriginalName string   `json:"original_name,omitempty"`
	Size         int64    `json:"size,omitempty"`
}

// TimingJSON 是 timing/<id>.json。
type TimingJSON struct {
	LyricsFingerprint string          `json:"lyrics_fingerprint"` // 對時當下的歌詞；還原時歌詞一樣才沿用
	Language          string          `json:"language"`
	Model             string          `json:"model"`
	Method            int             `json:"method"` // versions.align
	Run               string          `json:"run"`    // 整首對時的編號（還原後「已確認」才能延續）
	Lines             []wp.Line       `json:"lines"`
	Adjustments       json.RawMessage `json:"adjustments"`
}

// Backup 寫資料備份。
type Backup struct {
	Store *store.Store
	Dir   string // data/
}

// Stats 是一次備份的統計。
type Stats struct {
	Songs, Lyrics, Timing, Changed int
}

// Snapshot 把目前的歌單、歌詞與對時寫到 data/（內容沒變的檔案不重寫）。
func (b *Backup) Snapshot() (Stats, error) {
	var st Stats
	for _, d := range []string{LyricsDir, TimingDir} {
		if err := os.MkdirAll(filepath.Join(b.Dir, d), 0o755); err != nil {
			return st, err
		}
	}
	lib := b.Store.Library()
	doc := SongsJSON{Version: Version, Folders: []FolderJSON{}, Songs: []SongJSON{}}
	wanted := map[string]bool{}
	for _, id := range b.Store.SongIDs() {
		sg, ok := b.Store.Song(id)
		if !ok {
			continue
		}
		place := lib.Songs[id]
		entry := SongJSON{ID: id, Info: sg.Info, Source: sourceOf(sg.Source)}
		if entry.Info.Approved != nil {
			entry.Info.Approved = &song.Approval{Fingerprint: entry.Info.Approved.Fingerprint, At: entry.Info.Approved.At}
		}
		if place != nil {
			entry.Folder, entry.Order = ptr(place.Folder), place.Order
		}
		lyr, err := planner.ReadLyrics(b.Store, id)
		if err != nil {
			return st, err
		}
		var meta map[string]string
		if lyr != nil {
			meta = lyr.Meta
			raw, err := os.ReadFile(b.Store.SongPath(id, song.FileLyrics))
			if err != nil {
				return st, err
			}
			rel := LyricsDir + "/" + id + ".txt"
			st.Changed += write(filepath.Join(b.Dir, rel), bytes.TrimPrefix(raw, []byte("\xef\xbb\xbf")))
			entry.Lyrics, wanted[rel] = &rel, true
			st.Lyrics++
		}
		entry.Display.Title, entry.Display.Artist, _ = planner.Display(sg, meta)
		al, err := planner.ReadAlignment(b.Store, id)
		if err != nil {
			return st, err
		}
		if al != nil && len(al.Lines) > 0 {
			adj, _ := json.Marshal(al.Adjustments)
			if al.Adjustments == nil {
				adj = []byte("[]")
			}
			t := TimingJSON{LyricsFingerprint: al.Lyrics, Language: al.Language, Model: al.Model, Method: al.Method,
				Run: al.Run, Lines: al.Lines, Adjustments: adj}
			rel := TimingDir + "/" + id + ".json"
			st.Changed += write(filepath.Join(b.Dir, rel), dumps(t))
			entry.Timing, wanted[rel] = &rel, true
			st.Timing++
		}
		doc.Songs = append(doc.Songs, entry)
	}
	for _, f := range lib.Folders {
		doc.Folders = append(doc.Folders, FolderJSON{ID: f.ID, Name: f.Name, Parent: ptr(f.Parent), Order: f.Order})
	}
	sort.Slice(doc.Folders, func(a, c int) bool { return doc.Folders[a].ID < doc.Folders[c].ID })
	st.Songs = len(doc.Songs)
	st.Changed += write(filepath.Join(b.Dir, SongsFile), dumps(doc))
	st.Changed += write(filepath.Join(b.Dir, SettingsFile), dumps(lib.Settings))

	// 歌被刪掉、歌詞或對時不見了：備份裡也拿掉（只動 lyrics/ 與 timing/ 裡我們管理的檔案）
	for _, d := range []string{LyricsDir, TimingDir} {
		entries, err := os.ReadDir(filepath.Join(b.Dir, d))
		if err != nil {
			return st, err
		}
		for _, e := range entries {
			if rel := d + "/" + e.Name(); !e.IsDir() && !wanted[rel] {
				if os.Remove(filepath.Join(b.Dir, rel)) == nil {
					st.Changed++
				}
			}
		}
	}
	return st, nil
}

func sourceOf(s song.Source) SourceJSON {
	out := SourceJSON{Kind: s.Kind, URL: s.URL, Extractor: s.Extractor, VideoID: s.VideoID, Title: s.Title,
		Uploader: s.Uploader, Channel: s.Channel, Track: s.Track, Artists: s.Artists, Duration: s.Duration, Mode: s.Mode}
	if s.Kind == song.KindLocal {
		out.OriginalName, out.Size = s.OriginalName, s.File.Size
	}
	return out
}

func ptr(s string) *string {
	if s == library.Root {
		return nil
	}
	return &s
}

func dumps(v any) []byte {
	data, err := json.MarshalIndent(v, "", " ")
	if err != nil {
		panic(err)
	}
	return append(data, '\n')
}

// write 內容不同才寫入，回傳 1 / 0（有沒有變動）。
func write(path string, data []byte) int {
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
		return 0
	}
	if err := store.WriteFile(path, data); err != nil {
		return 0
	}
	return 1
}

// Publish 在 data/ 是 git repo 時 commit 並 push；push 失敗只記錄，不影響其他處理。回傳是否有新的 commit。
func (b *Backup) Publish(ctx context.Context, message string, log func(string)) (bool, error) {
	if _, err := os.Stat(filepath.Join(b.Dir, ".git")); errors.Is(err, fs.ErrNotExist) {
		log("  . data/ 不是 git repo，略過 commit")
		return false, nil
	}
	if _, err := b.git(ctx, time.Minute, "add", "-A"); err != nil {
		return false, err
	}
	status, err := b.git(ctx, time.Minute, "status", "--porcelain")
	if err != nil {
		return false, err
	}
	if strings.TrimSpace(status) == "" {
		return false, nil
	}
	if _, err := b.git(ctx, time.Minute, "commit", "-q", "-m", message); err != nil {
		log("  [!] 資料備份 commit 失敗：" + err.Error())
		return false, nil
	}
	log("  . 資料備份已 commit：" + message)
	if remotes, _ := b.git(ctx, time.Minute, "remote"); strings.TrimSpace(remotes) != "" {
		if _, err := b.git(ctx, 3*time.Minute, "push", "-q", "origin", "HEAD"); err != nil {
			log("  [!] 資料備份 push 失敗（下次會再試）：" + err.Error())
		} else {
			log("  . 資料備份已 push")
		}
	}
	return true, nil
}

func (b *Backup) git(ctx context.Context, timeout time.Duration, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := proc.Command(ctx, "git", args...)
	cmd.Dir = b.Dir
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("%s", strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

// Run 是收尾時的完整備份：寫出、commit、push。
func (b *Backup) Run(ctx context.Context, log func(string)) error {
	st, err := b.Snapshot()
	if err != nil {
		return err
	}
	changed := "沒有變動"
	if st.Changed > 0 {
		changed = "有變動"
	}
	log(fmt.Sprintf("  . 資料備份：%d 首、歌詞 %d、對時 %d（%s）", st.Songs, st.Lyrics, st.Timing, changed))
	_, err = b.Publish(ctx, "自動備份 "+time.Now().Format("2006-01-02 15:04"), log)
	return err
}
