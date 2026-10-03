package backup

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
	"github.com/zoosewu/kara-creator/nas/internal/timing"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

func TestSnapshotAndPublish(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("沒有 git")
	}
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "library"), true)
	if err != nil {
		t.Fatal(err)
	}
	var folder string
	_ = st.EditLibrary(func(l *library.Library) error {
		f, err := l.AddFolder("", "日文", library.Root, 0)
		folder = f.ID
		return err
	})
	_ = st.AddSong(song.New("abc", song.Source{Kind: song.KindURL, URL: "https://youtu.be/abc", Title: "虛構歌手『自己編的歌』",
		File: song.FileRef{Name: "source.mp4", Size: 9, SHA256: "x"}}), folder)
	_ = st.AddSong(song.New("local-12345678", song.Source{Kind: song.KindLocal, Title: "檔名", OriginalName: "檔名.mp4",
		File: song.FileRef{Name: "source.mp4", Size: 1234}}), library.Root)
	_ = os.WriteFile(st.SongPath("abc", song.FileLyrics), []byte("\xef\xbb\xbf第一句\n"), 0o644)
	al := timing.Alignment{Key: "k", Lyrics: "lfp", Language: "ja", Method: 7, Model: "large-v3",
		Lines: []wp.Line{{Text: "第一句", Start: 1, End: 2, Words: []wp.Word{{Text: "第一句", Start: 1, End: 2}}}}}
	data, _ := json.Marshal(al)
	_ = os.WriteFile(st.SongPath("abc", song.FileAlignment), data, 0o644)

	b := &Backup{Store: st, Dir: filepath.Join(dir, "data")}
	stats, err := b.Snapshot()
	if err != nil {
		t.Fatal(err)
	}
	if stats.Songs != 2 || stats.Lyrics != 1 || stats.Timing != 1 || stats.Changed != 4 {
		t.Fatalf("%+v", stats)
	}
	var doc SongsJSON
	raw, _ := os.ReadFile(filepath.Join(b.Dir, SongsFile))
	_ = json.Unmarshal(raw, &doc)
	if doc.Version != 2 || len(doc.Folders) != 1 || doc.Songs[0].Display.Title != "自己編的歌" || *doc.Songs[0].Folder != folder ||
		doc.Songs[1].Source.Size != 1234 || doc.Songs[1].Source.OriginalName != "檔名.mp4" || doc.Songs[1].Lyrics != nil {
		t.Fatalf("%s", raw)
	}
	if lyr, _ := os.ReadFile(filepath.Join(b.Dir, "lyrics", "abc.txt")); string(lyr) != "第一句\n" {
		t.Fatalf("歌詞備份不要帶 BOM：%q", lyr)
	}
	var tm TimingJSON
	raw, _ = os.ReadFile(filepath.Join(b.Dir, "timing", "abc.json"))
	_ = json.Unmarshal(raw, &tm)
	if tm.LyricsFingerprint != "lfp" || tm.Language != "ja" || tm.Method != 7 || len(tm.Lines) != 1 || string(tm.Adjustments) != "[]" {
		t.Fatalf("%s", raw)
	}
	if stats, _ := b.Snapshot(); stats.Changed != 0 {
		t.Fatalf("沒變動時不重寫：%+v", stats)
	}

	// 不是 git repo：略過 commit
	var logs []string
	logf := func(s string) { logs = append(logs, s) }
	if ok, err := b.Publish(context.Background(), "測試", logf); ok || err != nil || !strings.Contains(logs[0], "不是 git repo") {
		t.Fatal(logs, err)
	}
	// git repo：commit（沒有 remote 不 push）
	for _, args := range [][]string{{"init", "-q"}, {"config", "user.email", "test@example.com"}, {"config", "user.name", "test"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = b.Dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%v %s", err, out)
		}
	}
	if ok, err := b.Publish(context.Background(), "測試備份", logf); !ok || err != nil {
		t.Fatal(logs, err)
	}
	if ok, _ := b.Publish(context.Background(), "沒有變動", logf); ok {
		t.Fatal("沒有變動不該 commit")
	}
	// 歌詞刪掉：備份裡也拿掉
	_ = os.Remove(st.SongPath("abc", song.FileLyrics))
	if stats, _ := b.Snapshot(); stats.Lyrics != 0 {
		t.Fatal(stats)
	}
	if _, err := os.Stat(filepath.Join(b.Dir, "lyrics", "abc.txt")); !os.IsNotExist(err) {
		t.Fatal("備份裡的歌詞也要刪掉")
	}
}
