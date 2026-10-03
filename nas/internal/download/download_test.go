package download

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/media"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
)

// 假的 yt-dlp：-J 印出 $FAKE_INFO；下載時把 $FAKE_MEDIA 複製到 -o 指定的位置。
const fakeYTDLP = `#!/bin/sh
mode=download; out=""; prev=""
for a in "$@"; do
  [ "$a" = "-J" ] && mode=info
  [ "$prev" = "-o" ] && out="$a"
  prev="$a"
done
if [ -n "$FAKE_FAIL" ]; then echo "ERROR: [youtube] abc: Video unavailable" >&2; exit 1; fi
if [ "$mode" = info ]; then cat "$FAKE_INFO"; exit 0; fi
[ -n "$FAKE_SLOW" ] && sleep 30
dst="${out%%.%(ext)s}.mp4"
echo "[download] Destination: $dst"
echo "KARA-PROGRESS 50 NA 100"
cp "$FAKE_MEDIA" "$dst"
echo "KARA-PROGRESS 100 100 NA"
`

type env struct {
	t  *testing.T
	st *store.Store
	d  *Downloader
}

func newEnv(t *testing.T) *env {
	t.Helper()
	tools := media.DefaultTools("")
	if _, err := exec.LookPath(tools.FFmpeg); err != nil {
		t.Skip("沒有 ffmpeg")
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "yt-dlp")
	if err := os.WriteFile(bin, []byte(fakeYTDLP), 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "fixture.mp4")
	if out, err := exec.Command(tools.FFmpeg, "-y", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=10:duration=1",
		"-f", "lavfi", "-i", "sine=duration=1", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", video).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	infoPath := filepath.Join(dir, "info.json")
	_ = os.WriteFile(infoPath, []byte(`{"id":"dQw4w9WgXcQ","extractor_key":"Youtube","webpage_url":"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"title":"虛構歌手『自己編的歌』","channel":"虛構歌手 Official","artist":"虛構歌手","duration":1}`), 0o644)
	t.Setenv("FAKE_INFO", infoPath)
	t.Setenv("FAKE_MEDIA", video)
	st, err := store.Open(filepath.Join(dir, "library"), true)
	if err != nil {
		t.Fatal(err)
	}
	return &env{t: t, st: st, d: &Downloader{Store: st, Media: tools, YTDLP: bin}}
}

func TestSongID(t *testing.T) {
	cases := map[[2]string]string{
		{"Youtube", "dQw4w9WgXcQ"}:  "dQw4w9WgXcQ",
		{"Youtube", "a-b_c"}:        "a-b_c",
		{"BiliBili", "BV1x/y?z"}:    "bilibili-BV1x_y_z",
		{"Vimeo", "12345"}:          "vimeo-12345",
		{"SoundCloud", "artist/歌名"}: "soundcloud-artist___",
	}
	for in, want := range cases {
		if got := SongID(in[0], in[1]); got != want {
			t.Errorf("SongID(%q, %q) = %q，應該 %q", in[0], in[1], got, want)
		}
	}
}

func TestDownload(t *testing.T) {
	e := newEnv(t)
	var folder string
	_ = e.st.EditLibrary(func(l *library.Library) error {
		f, err := l.AddFolder("", "日文", library.Root, 0)
		folder = f.ID
		return err
	})
	var mu sync.Mutex
	var logs []string
	var progress []float64
	req := Request{URL: "https://youtu.be/dQw4w9WgXcQ", Folder: folder, Lyrics: "第一句\n第二句",
		Log:      func(s string) { mu.Lock(); logs = append(logs, s); mu.Unlock() },
		Progress: func(p float64) { mu.Lock(); progress = append(progress, p); mu.Unlock() }}
	res, err := e.d.Download(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "dQw4w9WgXcQ" || res.Skipped {
		t.Fatalf("%+v", res)
	}
	sg, ok := e.st.Song(res.ID)
	if !ok || sg.Source.File.Name != "source.mp4" || sg.Source.Width != 320 || sg.Source.Mode != "video" ||
		sg.Source.Artists[0] != "虛構歌手" || sg.Source.URL != "https://www.youtube.com/watch?v=dQw4w9WgXcQ" || sg.Source.File.SHA256 == "" {
		t.Fatalf("%+v", sg.Source)
	}
	if e.st.Library().Songs[res.ID].Folder != folder {
		t.Fatal("新歌要放進指定的資料夾")
	}
	if data, _ := os.ReadFile(e.st.SongPath(res.ID, song.FileLyrics)); string(data) != "第一句\n第二句\n" {
		t.Fatalf("歌詞：%q", data)
	}
	if _, err := os.Stat(e.st.SongPath(res.ID, ".downloading")); !os.IsNotExist(err) {
		t.Fatal("暫存資料夾要刪掉")
	}
	if len(progress) != 2 || progress[0] != 0.5 || progress[1] != 1 {
		t.Fatalf("進度：%v", progress)
	}

	// 再下載一次：略過
	res, err = e.d.Download(context.Background(), req)
	if err != nil || !res.Skipped {
		t.Fatalf("%+v %v", res, err)
	}
	// 來源檔不見了：重新下載
	_ = os.Remove(e.st.SongPath(res.ID, "source.mp4"))
	if res, err = e.d.Download(context.Background(), req); err != nil || res.Skipped {
		t.Fatalf("%+v %v", res, err)
	}
	if !strings.Contains(strings.Join(logs, "\n"), "重新下載") {
		t.Fatal(logs)
	}
}

func TestDownloadErrors(t *testing.T) {
	e := newEnv(t)
	t.Setenv("FAKE_FAIL", "1")
	_, err := e.d.Download(context.Background(), Request{URL: "https://youtu.be/x"})
	if err == nil || err.Error() != "下載失敗：[youtube] abc: Video unavailable" {
		t.Fatalf("錯誤訊息要給人看：%v", err)
	}
	t.Setenv("FAKE_FAIL", "")
	t.Setenv("FAKE_SLOW", "1")
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = e.d.Download(ctx, Request{URL: "https://youtu.be/x"})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(start) > 5*time.Second {
		t.Fatalf("取消時要終止 yt-dlp：%v", err)
	}
}

// 假的 yt-dlp（版本 1）：-U 時把自己換成版本 2。
const fakeV1 = `#!/bin/sh
case "$1" in
  --version) echo 2026.01.01 ;;
  -U) printf '#!/bin/sh\necho 2026.10.01\n' > "$0"; echo "Updated yt-dlp to 2026.10.01" ;;
esac
`

func TestUpdateAndRollback(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "yt-dlp")
	if err := os.WriteFile(bin, []byte(fakeV1), 0o755); err != nil {
		t.Fatal(err)
	}
	d := &Downloader{YTDLP: bin}
	ctx := context.Background()
	before, after, err := d.Update(ctx)
	if err != nil || before != "2026.01.01" || after != "2026.10.01" {
		t.Fatalf("%q → %q %v", before, after, err)
	}
	if v := d.PrevVersion(ctx); v != "2026.01.01" {
		t.Fatalf("上一版 %q", v)
	}
	if err := d.Rollback(); err != nil {
		t.Fatal(err)
	}
	if v, _ := d.Version(ctx); v != "2026.01.01" {
		t.Fatalf("退回後 %q", v)
	}
	if v := d.PrevVersion(ctx); v != "2026.10.01" {
		t.Fatalf("退回後還可以再換回新版：%q", v)
	}
	// 已經是最新版
	_ = d.Rollback()
	if before, after, err := d.Update(ctx); err != nil || before != after {
		t.Fatalf("%q %q %v", before, after, err)
	}
}
