package pipeline

import (
	"context"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	kara "github.com/zoosewu/kara-creator"
	"github.com/zoosewu/kara-creator/nas/internal/fakeworker"
	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/media"
	"github.com/zoosewu/kara-creator/nas/internal/planner"
	"github.com/zoosewu/kara-creator/nas/internal/scheduler"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// countingAI 記下交出的任務種類。
type countingAI struct {
	inner AI
	mu    sync.Mutex
	kinds []string
	specs []scheduler.Spec
}

func (c *countingAI) Submit(ctx context.Context, spec scheduler.Spec) (*scheduler.Result, error) {
	c.mu.Lock()
	c.kinds = append(c.kinds, spec.Kind)
	c.specs = append(c.specs, spec)
	c.mu.Unlock()
	return c.inner.Submit(ctx, spec)
}

func (c *countingAI) take() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := strings.Join(c.kinds, ",")
	c.kinds = nil
	return out
}

type env struct {
	t  *testing.T
	st *store.Store
	p  *Pipeline
	ai *countingAI
	mu sync.Mutex
	l  []string
}

func newEnv(t *testing.T) *env {
	t.Helper()
	tools := media.DefaultTools("")
	if _, err := exec.LookPath(tools.FFmpeg); err != nil {
		t.Skip("沒有 ffmpeg")
	}
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "library"), true)
	if err != nil {
		t.Fatal(err)
	}
	sch := scheduler.New(scheduler.Options{Versions: kara.Current, WorkDir: st.Path(store.WorkDir), LeaseWait: 200 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	go sch.Run(ctx)
	srv := httptest.NewServer(sch.Handler())
	done := make(chan struct{})
	go func() {
		_ = fakeworker.Run(ctx, fakeworker.Options{NAS: srv.URL, Name: "fake", Logf: t.Logf})
		close(done)
	}()
	t.Cleanup(func() { cancel(); <-done; srv.Close() })

	ai := &countingAI{inner: sch}
	fonts := func(lang string) (wp.Font, bool) {
		return wp.Font{SHA256: "font-" + lang, Family: "Noto Sans CJK"}, true
	}
	e := &env{t: t, st: st, ai: ai}
	e.p = New(Deps{Store: st, Media: tools, AI: ai, Fonts: fonts, Versions: kara.Current})

	// 一首 3 秒的測試影片
	if err := st.AddSong(song.New("abc", song.Source{Kind: song.KindLocal, Title: "虛構歌手 - 自己編的歌", Mode: "video",
		Width: 320, Height: 240}), library.Root); err != nil {
		t.Fatal(err)
	}
	src := st.SongPath("abc", "source.mp4")
	out, err := exec.Command(tools.FFmpeg, "-y", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=10:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", src).CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, out)
	}
	ref, _ := store.Refresh(src, song.FileRef{})
	_ = st.EditSong("abc", func(s *song.Song) error { s.Source.File = ref; return nil })
	return e
}

func (e *env) run() Run {
	return Run{Log: func(s string) { e.mu.Lock(); e.l = append(e.l, s); e.mu.Unlock() }}
}

func (e *env) status() planner.Result {
	e.t.Helper()
	_, r, err := e.p.evaluate("abc")
	if err != nil {
		e.t.Fatal(err)
	}
	return r
}

func (e *env) karaoke(want string) {
	e.t.Helper()
	if err := e.p.Karaoke(context.Background(), "abc", KaraokeOptions{}, e.run()); err != nil {
		e.t.Fatal(err)
	}
	if got := e.ai.take(); got != want {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.t.Fatalf("交給 AI 的任務 %q，應該 %q\n紀錄：\n%s", got, want, strings.Join(e.l, "\n"))
	}
	if r := e.status(); r.Status.Karaoke != planner.Done || r.Status.QA == nil {
		e.t.Fatalf("做完應該是最新：%+v", r.Status)
	}
}

func (e *env) writeLyrics(text string) {
	if err := os.WriteFile(e.st.SongPath("abc", song.FileLyrics), []byte(text), 0o644); err != nil {
		e.t.Fatal(err)
	}
}

func TestKaraokeFlow(t *testing.T) {
	e := newEnv(t)
	ctx := context.Background()

	// 沒有歌詞：只記錄，不算失敗
	if err := e.p.Karaoke(ctx, "abc", KaraokeOptions{}, e.run()); err != nil {
		t.Fatal(err)
	}
	if e.ai.take() != "" {
		t.Fatal("沒有歌詞不該做任何事")
	}

	e.writeLyrics("[男] 自己編的第一句\n> 翻譯\n[女] 第二句\n")
	e.karaoke("separate,align,render,qa")
	sg, _ := e.st.Song("abc")
	for _, name := range []string{"instrumental.mp4", "vocals.flac", "alignment.json", "qa.json", "karaoke.ass", "karaoke.mp4"} {
		if _, err := os.Stat(e.st.SongPath("abc", name)); err != nil {
			t.Errorf("應該有 %s：%v", name, err)
		}
	}
	if info, err := e.p.d.Media.Probe(ctx, e.st.SongPath("abc", "instrumental.mp4")); err != nil || !info.HasVideo {
		t.Errorf("伴奏要保留影像軌：%+v %v", info, err)
	}
	if sg.Stages.Separate.Worker != "fake" || sg.Stages.Render[song.TargetInstrumental].ASS.Manual {
		t.Errorf("%+v", sg.Stages)
	}
	// render 的參數：演唱者、翻譯、標題畫面
	var render wp.RenderParams
	for _, s := range e.ai.specs {
		if s.Kind == wp.KindRender {
			render = s.Params.(wp.RenderParams)
		}
	}
	if len(render.Singers) != 2 || *render.Singers[0] != "男" || render.Translations[0] != "翻譯" ||
		*render.TitleCard[0] != "自己編的歌" || render.Font.SHA256 != "font-zh" {
		t.Errorf("render 參數：%+v", render)
	}

	// 再做一次：全部已是最新
	e.karaoke("")

	// 字幕大小改了：只重燒
	_ = e.st.EditLibrary(func(l *library.Library) error { l.Settings.SubtitleScale = 1.2; return nil })
	e.karaoke("render")

	// 使用者手改 ASS：用手改的那份重燒，不重新產生
	ass := e.st.SongPath("abc", song.FileASS)
	if err := os.WriteFile(ass, []byte("[Script Info]\n; 使用者用 Aegisub 改過\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	_ = os.Chtimes(ass, later, later)
	e.karaoke("render")
	last := e.ai.specs[len(e.ai.specs)-1]
	if _, ok := last.Inputs[wp.InputASS]; !ok || len(last.Outputs) != 1 {
		t.Fatalf("手改 ASS 時要把它送給 AI、只上傳影片：%+v", last)
	}
	if data, _ := os.ReadFile(ass); !strings.Contains(string(data), "Aegisub") {
		t.Fatal("手改的 ASS 不該被覆蓋")
	}

	// 只改演唱者：重燒（ASS 內容變了，手改的作廢），不重新對時
	e.writeLyrics("[女] 自己編的第一句\n> 翻譯\n[女] 第二句\n")
	e.karaoke("render")
	if data, _ := os.ReadFile(ass); strings.Contains(string(data), "Aegisub") {
		t.Fatal("內容改了，手改的 ASS 要重新產生")
	}

	// AI 只重對第 2 句（假的 worker 會給不同的時間）：只改對時；之後只重燒、重新檢查
	if err := e.p.Retime(ctx, "abc", 1, "line", e.run()); err != nil {
		t.Fatal(err)
	}
	if got := e.ai.take(); got != "align_line" {
		t.Fatal(got)
	}
	if r := e.status(); r.Status.Karaoke != planner.NeedsRender || !r.AlignOK {
		t.Fatalf("%+v", r.Status)
	}
	e.karaoke("render,qa")

	// 改歌詞文字：整首重新對時
	e.writeLyrics("[女] 自己編的第一句改了\n[女] 第二句\n")
	if err := e.p.Retime(ctx, "abc", 0, "line", e.run()); err == nil || !strings.Contains(err.Error(), "歌詞改過") {
		t.Fatalf("歌詞改過不能 AI 重對：%v", err)
	}
	e.karaoke("align,render,qa")

	// 單獨檢查：沒變就略過，force 才重做
	if err := e.p.Check(ctx, "abc", false, e.run()); err != nil || e.ai.take() != "" {
		t.Fatal(err)
	}
	if err := e.p.Check(ctx, "abc", true, e.run()); err != nil || e.ai.take() != "qa" {
		t.Fatal(err)
	}

	// 原曲＋字幕（Q13）：多一個成品；拿掉之後舊檔刪除
	_ = e.st.EditSong("abc", func(s *song.Song) error { s.Info.Targets = []string{"instrumental", "original"}; return nil })
	e.karaoke("render")
	if _, err := os.Stat(e.st.SongPath("abc", "original.mp4")); err != nil {
		t.Fatal(err)
	}
	_ = e.st.EditSong("abc", func(s *song.Song) error { s.Info.Targets = []string{"instrumental"}; return nil })
	e.karaoke("")
	if _, err := os.Stat(e.st.SongPath("abc", "original.mp4")); !os.IsNotExist(err) {
		t.Fatal("拿掉的成品要刪掉")
	}

	// AI 重對這句及之後全部
	if err := e.p.Retime(ctx, "abc", 0, "from", e.run()); err != nil || e.ai.take() != "align_from" {
		t.Fatal(err)
	}

	// 全部重做
	if err := e.p.Karaoke(ctx, "abc", KaraokeOptions{Force: true}, e.run()); err != nil {
		t.Fatal(err)
	}
	if got := e.ai.take(); got != "align,render,qa" {
		t.Fatalf("全部重做：%s", got)
	}

	// 暫存資料夾只留下可以重複用的 speech.wav
	entries, _ := os.ReadDir(e.st.Path(store.WorkDir))
	for _, ent := range entries {
		if ent.Name() == "tasks" {
			if sub, _ := os.ReadDir(e.st.Path(store.WorkDir, "tasks")); len(sub) != 0 {
				t.Errorf("上傳的暫存檔沒有清掉：%v", sub)
			}
			continue
		}
		if !strings.HasPrefix(ent.Name(), "speech-") {
			t.Errorf("暫存資料夾留下 %s", ent.Name())
		}
	}
}

func TestCancelled(t *testing.T) {
	e := newEnv(t)
	e.writeLyrics("一\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.p.Karaoke(ctx, "abc", KaraokeOptions{}, e.run()); err == nil {
		t.Fatal("取消了應該回傳錯誤")
	}
	if r := e.status(); r.Status.Separate != planner.Pending {
		t.Fatal("取消時不該留下紀錄")
	}
}
