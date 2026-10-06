package restore

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	kara "github.com/zoosewu/kara-creator"
	"github.com/zoosewu/kara-creator/nas/internal/backup"
	"github.com/zoosewu/kara-creator/nas/internal/fingerprint"
	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/lyrics"
	"github.com/zoosewu/kara-creator/nas/internal/media"
	"github.com/zoosewu/kara-creator/nas/internal/planner"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
	"github.com/zoosewu/kara-creator/nas/internal/timing"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

const lyricText = "# title: 自己編的歌\n[男] 第一の句\n第二句{だい}\n"

func lines() []wp.Line {
	return []wp.Line{
		{Text: "第一の句", Start: 1, End: 2, Words: []wp.Word{{Text: "第一の句", Start: 1, End: 2}}},
		{Text: "第二句", Start: 3, End: 4, Words: []wp.Word{{Text: "第二句", Start: 3, End: 4}}},
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	data, _ := json.Marshal(v)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := store.WriteFile(path, data); err != nil {
		t.Fatal(err)
	}
}

// 新的曲庫裡已經有重新下載的影片（id 相同），還原後跑 planner 看狀態。
func newLibrary(t *testing.T) *store.Store {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "new"), true)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(st.Close)
	_ = st.AddSong(song.New("abc", song.Source{Kind: song.KindURL, URL: "https://youtu.be/abc", Title: "影片標題",
		File: song.FileRef{Name: "source.mp4", SHA256: "src"}}), library.Root)
	return st
}

func evaluate(t *testing.T, st *store.Store) planner.Result {
	t.Helper()
	// 假裝重新去人聲完成（人聲和當初不同）
	_ = st.EditSong("abc", func(s *song.Song) error {
		s.Stages.Separate = &song.SeparateStage{Stage: song.Stage{Key: fingerprint.Separate("src", planner.SeparateModel, planner.SeparateStems, kara.Current.Separate)},
			Instrumental: song.FileRef{Name: "instrumental.mp4", SHA256: "i"}, Vocals: song.FileRef{Name: "vocals.flac", SHA256: "new-vocals"}}
		return nil
	})
	in, err := planner.Inspect(st, "abc", st.Library().Settings, nil, kara.Current)
	if err != nil {
		t.Fatal(err)
	}
	in.SeparateFilesOK = true
	return planner.Evaluate(in)
}

func TestRestoreV2(t *testing.T) {
	// 1. 舊曲庫：一首有歌詞、對時、已確認的歌，一首手動放入、沒有連結的歌；做資料備份
	old, err := store.Open(filepath.Join(t.TempDir(), "old"), true)
	if err != nil {
		t.Fatal(err)
	}
	var folder string
	_ = old.EditLibrary(func(l *library.Library) error {
		f, _ := l.AddFolder("", "日文", library.Root, 0)
		folder = f.ID
		l.Settings.SubtitleScale = 1.2
		return nil
	})
	_ = old.AddSong(song.New("abc", song.Source{Kind: song.KindURL, URL: "https://youtu.be/abc", Title: "影片標題"}), folder)
	_ = old.AddSong(song.New("local-12345678", song.Source{Kind: song.KindLocal, Title: "檔名", OriginalName: "檔名.mp4",
		File: song.FileRef{Name: "source.mp4", Size: 1234}}), library.Root)
	_ = os.WriteFile(old.SongPath("abc", song.FileLyrics), []byte(lyricText), 0o644)
	doc := lyrics.Parse(lyricText)
	var rubies [][]lyrics.Ruby
	for _, ln := range doc.LyricLines() {
		rubies = append(rubies, ln.Rubies)
	}
	lfp := fingerprint.Lyrics(doc.Texts(), rubies)
	writeJSON(t, old.SongPath("abc", song.FileAlignment), timing.Alignment{Key: "old-key", Lyrics: lfp, Language: "ja",
		Method: kara.Current.Align, Model: "large-v3", Run: "run-1", Lines: lines()})
	approved := song.Approval{Fingerprint: fingerprint.Approve(lfp, "ja", kara.Current.Align, "run-1"), At: "2026-10-01T00:00:00Z"}
	_ = old.EditSong("abc", func(s *song.Song) error {
		s.Info.Note, s.Info.Approved, s.Info.History = "備註", &approved, []song.Approval{approved}
		return nil
	})
	data := filepath.Join(t.TempDir(), "data")
	if _, err := (&backup.Backup{Store: old, Dir: data}).Snapshot(); err != nil {
		t.Fatal(err)
	}
	old.Close()

	// 2. 新曲庫：還原
	st := newLibrary(t)
	rep, err := Run(context.Background(), st, nil, Options{Data: data, AlignVer: kara.Current.Align})
	if err != nil {
		t.Fatal(err)
	}
	if rep.Folders != 1 || rep.Songs != 1 || rep.Lyrics != 1 || rep.Timing != 1 || len(rep.Realign) != 0 {
		t.Fatalf("%+v", rep)
	}
	if len(rep.MissingLocal) != 1 || rep.MissingLocal[0].Entry.Source.OriginalName != "檔名.mp4" || rep.MissingLocal[0].Entry.Source.Size != 1234 {
		t.Fatalf("手動放入的歌要列出原始檔名與大小：%+v", rep.MissingLocal)
	}
	lib := st.Library()
	if lib.Songs["abc"].Folder != folder || lib.Settings.SubtitleScale != 1.2 {
		t.Fatalf("%+v %+v", lib.Songs["abc"], lib.Settings)
	}

	// 3. 人聲重新分離過也沿用對時（只需重燒），確認延續
	r := evaluate(t, st)
	if !r.RestoredUsable || r.Status.Karaoke != planner.Pending && r.Status.Karaoke != planner.NeedsRender {
		t.Fatalf("RestoredUsable=%v karaoke=%s", r.RestoredUsable, r.Status.Karaoke)
	}
	if r.Status.Approval != planner.Approved {
		t.Fatalf("還原後確認要延續：%q", r.Status.Approval)
	}
}

func TestRestoreV1(t *testing.T) {
	data := filepath.Join(t.TempDir(), "data")
	doc := lyrics.Parse(lyricText)
	v1sha := lyrics.V1AlignSHA1(doc)
	writeJSON(t, filepath.Join(data, "songs.json"), map[string]any{"version": 1,
		"folders": []map[string]any{{"id": "f1", "name": "日文", "parent": nil, "order": 2}},
		"songs": []map[string]any{{"id": "abc", "key": "Youtube:abc", "folder": "f1", "order": 3, "title": "手動歌名", "artist": "",
			"language": "ja", "link": "", "note": "", "translation": false, "approved": "舊的字幕雜湊", "approved_at": "x",
			"display": map[string]string{"title": "手動歌名", "artist": "歌手"},
			"source":  map[string]any{"url": "https://youtu.be/abc", "extractor": "Youtube", "video_id": "abc", "title": "影片標題", "duration": 200, "mode": "video"},
			"lyrics":  "lyrics/abc.txt", "timing": "timing/abc.json"}}})
	_ = os.MkdirAll(filepath.Join(data, "lyrics"), 0o755)
	_ = os.WriteFile(filepath.Join(data, "lyrics", "abc.txt"), []byte(lyricText), 0o644)
	writeJSON(t, filepath.Join(data, "timing", "abc.json"), map[string]any{"lyrics_sha1": v1sha, "language": "ja",
		"model": "large-v3", "method": kara.Current.Align, "lines": lines(), "adjustments": []any{}})
	writeJSON(t, filepath.Join(data, "settings.json"), map[string]any{"subtitle_scale": 0.8})

	st := newLibrary(t)
	rep, err := Run(context.Background(), st, nil, Options{Data: data, AlignVer: kara.Current.Align})
	if err != nil || rep.Timing != 1 || len(rep.Realign) != 0 {
		t.Fatalf("%+v %v", rep, err)
	}
	sg, _ := st.Song("abc")
	if sg.Info.Title != "手動歌名" || sg.Info.Translation || sg.Info.Approved != nil {
		t.Fatalf("v1 的確認沒辦法換算，不還原：%+v", sg.Info)
	}
	if lib := st.Library(); lib.Songs["abc"].Folder != "f1" || lib.Songs["abc"].Order != 3 || lib.Settings.SubtitleScale != 0.8 {
		t.Fatalf("%+v", lib)
	}
	if r := evaluate(t, st); !r.RestoredUsable {
		t.Fatal("v1 的對時：歌詞的 v1 雜湊相符就沿用")
	}

	// 歌詞和對時當下不同：不沿用（製作時重新對時）
	writeJSON(t, filepath.Join(data, "timing", "abc.json"), map[string]any{"lyrics_sha1": "不一樣", "language": "ja",
		"method": kara.Current.Align, "lines": lines()})
	rep, _ = Run(context.Background(), st, nil, Options{Data: data, AlignVer: kara.Current.Align, Overwrite: true})
	if len(rep.Realign) != 1 {
		t.Fatalf("%+v", rep)
	}
	if r := evaluate(t, st); r.RestoredUsable {
		t.Fatal("歌詞對不上不能沿用")
	}
}

func TestRestoreV1Sources(t *testing.T) {
	tools := media.DefaultTools("")
	if _, err := exec.LookPath(tools.FFmpeg); err != nil {
		t.Skip("沒有 ffmpeg")
	}
	tmp := t.TempDir()
	data, downloads := filepath.Join(tmp, "data"), filepath.Join(tmp, "downloads")
	doc := lyrics.Parse(lyricText)
	entry := func(id, extractor, url string) map[string]any {
		return map[string]any{"id": id, "folder": "f1", "order": 1, "title": "", "artist": "", "language": "ja",
			"display": map[string]string{"title": "歌"}, "lyrics": "lyrics/" + id + ".txt", "timing": "timing/" + id + ".json",
			"source": map[string]any{"url": url, "extractor": extractor, "video_id": id, "title": "影片標題", "duration": 1, "mode": "audio"}}
	}
	ids := []string{"abc", "xyz123", "local-1234abcd"}
	writeJSON(t, filepath.Join(data, "songs.json"), map[string]any{"version": 1,
		"folders": []map[string]any{{"id": "f1", "name": "日文", "parent": nil, "order": 1}},
		"songs": []map[string]any{entry(ids[0], "Youtube", "https://youtu.be/abc"), entry(ids[1], "BiliBili", "https://b23.tv/xyz123"),
			entry(ids[2], "local", "")}})
	// v1 的下載資料夾：每首一個「標題 (id)」資料夾，裡面是影片與 download.json
	for i, id := range ids {
		_ = os.MkdirAll(filepath.Join(data, "lyrics"), 0o755)
		_ = os.WriteFile(filepath.Join(data, "lyrics", id+".txt"), []byte(lyricText), 0o644)
		writeJSON(t, filepath.Join(data, "timing", id+".json"), map[string]any{"lyrics_sha1": lyrics.V1AlignSHA1(doc), "language": "ja",
			"method": kara.Current.Align, "lines": lines()})
		folder := filepath.Join(downloads, "歌 ("+id+")")
		_ = os.MkdirAll(folder, 0o755)
		file := "自己的檔案.m4a"
		if out, err := exec.Command(tools.FFmpeg, "-v", "error", "-f", "lavfi", "-i", "sine=duration=1", filepath.Join(folder, file)).CombinedOutput(); err != nil {
			t.Fatalf("%s %v", out, err)
		}
		extractor := []string{"Youtube", "BiliBili", "local"}[i]
		writeJSON(t, filepath.Join(folder, "download.json"), map[string]any{"extractor": extractor, "id": id, "url": "https://example.com/" + id,
			"title": "影片標題", "channel": "頻道", "duration": 1, "mode": "audio", "file": file})
	}
	_ = os.MkdirAll(filepath.Join(downloads, "沒下載完 (zzz)"), 0o755) // 沒有 download.json：略過

	st, err := store.Open(filepath.Join(tmp, "new"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	opt := Options{Data: data, Sources: downloads, Media: tools, AlignVer: kara.Current.Align}
	rep, err := Run(context.Background(), st, nil, opt) // 沒有下載工具：只能用舊檔案
	if err != nil || len(rep.Imported) != 3 || len(rep.Failed)+len(rep.MissingLocal)+len(rep.Realign) != 0 || rep.Timing != 3 {
		t.Fatalf("%+v %v", rep, err)
	}
	for id, kind := range map[string]string{"abc": song.KindURL, "bilibili-xyz123": song.KindURL, "local-1234abcd": song.KindLocal} {
		sg, ok := st.Song(id)
		if !ok || sg.Source.Kind != kind || sg.Source.File.Name != "source.m4a" || sg.Source.Mode != "audio" || sg.Source.File.SHA256 == "" {
			t.Fatalf("%s：%+v", id, sg.Source)
		}
		if st.Library().Songs[id].Folder != "f1" {
			t.Fatalf("%s 的資料夾", id)
		}
		if _, err := os.Stat(st.SongPath(id, song.FileAlignment)); err != nil {
			t.Fatalf("%s 的對時沿用：%v", id, err)
		}
	}
	if sg, _ := st.Song("local-1234abcd"); sg.Source.OriginalName != "自己的檔案.m4a" {
		t.Fatalf("%+v", sg.Source)
	}
	if sg, _ := st.Song("abc"); sg.Source.Channel != "頻道" || sg.Source.URL != "https://example.com/abc" {
		t.Fatalf("%+v", sg.Source)
	}
	// 舊檔案不動
	if _, err := os.Stat(filepath.Join(downloads, "歌 (abc)", "自己的檔案.m4a")); err != nil {
		t.Fatal("v1 的檔案要留著")
	}
	// 再執行一次：已經搬過的不再搬
	if rep, err := Run(context.Background(), st, nil, opt); err != nil || len(rep.Imported) != 0 || rep.Songs != 3 {
		t.Fatalf("%+v %v", rep, err)
	}
}
