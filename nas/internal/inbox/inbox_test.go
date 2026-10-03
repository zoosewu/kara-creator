package inbox

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zoosewu/kara-creator/nas/internal/media"
	"github.com/zoosewu/kara-creator/nas/internal/store"
)

func TestLocalID(t *testing.T) {
	// 和 v1 相同：sha1("檔名:大小")[:8]
	for in, want := range map[string]string{"歌手 - 歌名.mp4": "local-53681c87", "a.mp4": "local-cb2419a0"} {
		size := int64(1234)
		if in == "a.mp4" {
			size = 1
		}
		if got := LocalID(in, size); got != want {
			t.Errorf("LocalID(%q) = %q，應該 %q（v1 的值）", in, got, want)
		}
	}
	if LocalID("a.mp4", 1) == LocalID("a.mp4", 2) || LocalID("a.mp4", 1) != LocalID("a.mp4", 1) {
		t.Fatal("同一個檔案同一個 id，大小不同就不同")
	}
}

type env struct {
	t     *testing.T
	st    *store.Store
	b     *Inbox
	now   time.Time
	audio []byte
	mu    sync.Mutex
	logs  []string
}

func newEnv(t *testing.T) *env {
	tools := media.DefaultTools("")
	if _, err := exec.LookPath(tools.FFmpeg); err != nil {
		t.Skip("沒有 ffmpeg")
	}
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "library"), true)
	if err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(dir, "fixture.m4a")
	if out, err := exec.Command(tools.FFmpeg, "-y", "-v", "error", "-f", "lavfi", "-i", "sine=duration=1", "-c:a", "aac", fixture).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	audio, _ := os.ReadFile(fixture)
	e := &env{t: t, st: st, now: time.Now(), audio: audio}
	e.b = &Inbox{Store: st, Media: tools, Now: func() time.Time { return e.now },
		Log: func(s string) { e.mu.Lock(); e.logs = append(e.logs, s); e.mu.Unlock() }}
	return e
}

func (e *env) put(rel string, data []byte) string {
	path := e.st.Path(store.InboxDir, rel)
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		e.t.Fatal(err)
	}
	return path
}

func TestScan(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()
	e.put("虛構歌手 - 自己編的歌.m4a", e.audio)
	e.put("下載中.mp4.part", []byte("x"))
	e.put("說明.txt", []byte("x"))
	e.put("子資料夾/第二首.m4a", e.audio[:len(e.audio)-1])

	// 剛放進來：等大小穩定
	added, waiting := e.b.Scan(ctx)
	if len(added) != 0 || !waiting {
		t.Fatalf("剛放進來的檔案要等 %v：%v %v", Stable, added, waiting)
	}
	e.now = e.now.Add(Stable)
	added, waiting = e.b.Scan(ctx)
	if len(added) != 2 || waiting {
		t.Fatalf("%v %v", added, waiting)
	}
	id := LocalID("虛構歌手 - 自己編的歌.m4a", int64(len(e.audio)))
	sg, ok := e.st.Song(id)
	if !ok || sg.Source.Title != "虛構歌手 - 自己編的歌" || sg.Source.Mode != "audio" || sg.Source.File.Name != "source.m4a" ||
		sg.Source.OriginalName != "虛構歌手 - 自己編的歌.m4a" || sg.Source.Duration == 0 {
		t.Fatalf("%+v", sg.Source)
	}
	if _, err := os.Stat(e.st.Path(store.InboxDir, "子資料夾")); !os.IsNotExist(err) {
		t.Error("子資料夾變空要刪掉")
	}
	if _, err := os.Stat(e.st.Path(store.InboxDir, "下載中.mp4.part")); err != nil {
		t.Error("暫存檔不要動")
	}

	// 同一個檔案再放一次：略過並記錄（只記一次）
	e.put("虛構歌手 - 自己編的歌.m4a", e.audio)
	e.now = e.now.Add(time.Minute)
	e.b.Scan(ctx)
	e.now = e.now.Add(time.Minute)
	e.b.Scan(ctx)
	count := 0
	for _, l := range e.logs {
		if strings.Contains(l, "略過重複") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("重複的檔案記一次：%v", e.logs)
	}
}

func TestWatch(t *testing.T) {
	e := newEnv(t)
	e.b.Now = nil // 用真的時間
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	got := make(chan []string, 4)
	go func() { _ = e.b.Watch(ctx, func(ids []string) { got <- ids }) }()
	time.Sleep(100 * time.Millisecond)
	path := e.put("新歌.m4a", e.audio)
	old := time.Now().Add(-time.Minute)
	_ = os.Chtimes(path, old, old) // 早就寫好的檔案：不必等 5 秒
	select {
	case ids := <-got:
		if len(ids) != 1 {
			t.Fatal(ids)
		}
	case <-time.After(Stable + 5*time.Second):
		t.Fatal("監看到新檔案後應該匯入")
	}
}
