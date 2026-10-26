package migrate

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
	"github.com/zoosewu/kara-creator/nas/internal/pipeline"
	"github.com/zoosewu/kara-creator/nas/internal/planner"
	"github.com/zoosewu/kara-creator/nas/internal/readings"
	"github.com/zoosewu/kara-creator/nas/internal/scheduler"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// counting 記下交給 AI 的任務種類。
type counting struct {
	inner pipeline.AI
	mu    sync.Mutex
	kinds []string
}

func (c *counting) Submit(ctx context.Context, spec scheduler.Spec) (*scheduler.Result, error) {
	c.mu.Lock()
	c.kinds = append(c.kinds, spec.Kind)
	c.mu.Unlock()
	return c.inner.Submit(ctx, spec)
}

func (c *counting) take() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := strings.Join(c.kinds, ",")
	c.kinds = nil
	return out
}

type fixture struct {
	t          *testing.T
	st         *store.Store
	ai         *counting
	fonts      func(string) (wp.Font, bool)
	oldV, newV kara.Versions
	ctx        context.Context
	tools      media.Tools
}

// done 做出一首用舊的讀音版本（v2）從頭做完、已確認的歌。
func done(t *testing.T, lyricsText string) *fixture {
	tools := media.DefaultTools("")
	if _, err := exec.LookPath(tools.FFmpeg); err != nil {
		t.Skip("沒有 ffmpeg")
	}
	st, err := store.Open(filepath.Join(t.TempDir(), "library"), true)
	if err != nil {
		t.Fatal(err)
	}
	f := &fixture{t: t, st: st, oldV: kara.Current, newV: kara.Current, tools: tools,
		fonts: func(lang string) (wp.Font, bool) {
			return wp.Font{SHA256: "font-" + lang, Family: "Noto Sans CJK"}, true
		}}
	f.oldV.Reading, f.newV.Reading = FromReading, FromReading+1
	// 排程器的版本只用來比對 worker；pipeline 用舊 / 新版本判斷狀態
	sch := scheduler.New(scheduler.Options{Versions: kara.Current, WorkDir: st.Path(store.WorkDir), LeaseWait: 200 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	f.ctx = ctx
	go sch.Run(ctx)
	srv := httptest.NewServer(sch.Handler())
	stopped := make(chan struct{})
	go func() {
		_ = fakeworker.Run(ctx, fakeworker.Options{NAS: srv.URL, Name: "fake", Logf: t.Logf})
		close(stopped)
	}()
	t.Cleanup(func() { cancel(); <-stopped; srv.Close() })
	f.ai = &counting{inner: sch}

	if err := st.AddSong(song.New("abc", song.Source{Kind: song.KindLocal, Title: "虛構歌手 - 自己編的歌", Mode: "video", Width: 320, Height: 240}), library.Root); err != nil {
		t.Fatal(err)
	}
	src := st.SongPath("abc", "source.mp4")
	if out, err := exec.Command(tools.FFmpeg, "-y", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=10:duration=3",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=3", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", src).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	ref, _ := store.Refresh(src, song.FileRef{})
	_ = st.EditSong("abc", func(s *song.Song) error { s.Source.File = ref; return nil })
	f.writeLyrics(lyricsText)
	f.karaoke(f.oldV)
	r := f.evaluate(f.oldV)
	_ = st.EditSong("abc", func(s *song.Song) error {
		s.Info.Approved = &song.Approval{Fingerprint: r.ApproveFP, At: "t"}
		return nil
	})
	if b := f.evaluate(f.oldV); b.Status.Karaoke != planner.Done || b.Status.Approval != planner.Approved || b.Status.QA == nil {
		t.Fatalf("遷移前要全部最新：%+v", b.Status)
	}
	f.ai.take()
	return f
}

func (f *fixture) writeLyrics(text string) {
	if err := os.WriteFile(f.st.SongPath("abc", song.FileLyrics), []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) karaoke(v kara.Versions) {
	p := pipeline.New(pipeline.Deps{Store: f.st, Media: f.tools, AI: f.ai, Fonts: f.fonts, Versions: v})
	if err := p.Karaoke(f.ctx, "abc", pipeline.KaraokeOptions{}, pipeline.Run{Log: func(string) {}}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) evaluate(v kara.Versions) planner.Result {
	in, err := planner.Inspect(f.st, "abc", f.st.Library().Settings, f.fonts, v)
	if err != nil {
		f.t.Fatal(err)
	}
	return planner.Evaluate(in)
}

func (f *fixture) migrate(old map[string][]readings.Span) []string {
	var logs []string
	PinReadings(Env{Store: f.st, Font: f.fonts, Versions: f.newV, Old: old, Backup: filepath.Join(f.t.TempDir(), "backup"),
		Log: func(s string) { logs = append(logs, s) }})
	return logs
}

// 遷移到 v3 之後讀音照舊、狀態全部不變，也不需要交給 AI 做任何事。
func TestPinReadings(t *testing.T) {
	// 第二句的「何」使用者已經手動標過なに（不能動）；第三句沒有舊快取（用舊規則一定給的讀音）
	f := done(t, "私は何も知らない\n{何|なに}かが明日\n何時か私立の学校で\n")
	logs := f.migrate(map[string][]readings.Span{
		readings.Key("ja", "私は何も知らない", FromReading): {{Start: 0, End: 1, Ruby: "わたくし"}, {Start: 2, End: 3, Ruby: "なん"}, {Start: 5, End: 6, Ruby: "し"}},
		readings.Key("ja", "何かが明日", FromReading):    {{Start: 0, End: 1, Ruby: "なん"}, {Start: 3, End: 5, Ruby: "あす"}},
	})
	data, _ := os.ReadFile(f.st.SongPath("abc", song.FileLyrics))
	for _, want := range []string{"私{わたくし}は何{なん}も知らない", "何{なに}かが明日{あす}", "何{なん}{時|じ}か私立の学校で"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("要釘住舊讀音（手動標的不動、私立不動）：%q\n%s", want, data)
		}
	}
	if a := f.evaluate(f.newV); a.Status.Karaoke != planner.Done || a.Status.Approval != planner.Approved || a.Status.QA == nil {
		t.Fatalf("遷移後狀態要和之前一樣：%+v\n%s", a.Status, strings.Join(logs, "\n"))
	}
	f.karaoke(f.newV) // 用新版本「只做需要的」：什麼都不用做
	if got := f.ai.take(); got != "" {
		t.Fatalf("遷移後不需要交給 AI：%s", got)
	}
}

// 歌詞改過、還沒更新伴唱帶：遷移後還是只重對改到的那一句（只是被釘了讀音的句子不算改過）。
func TestPinReadingsPendingEdit(t *testing.T) {
	f := done(t, "私は何も知らない\n明日の約束\n")
	f.writeLyrics("私は何も知らない\n明日の約束を\n")
	b := f.evaluate(f.oldV)
	if b.AlignPatch == nil || b.AlignPatch.Realign() != 1 {
		t.Fatalf("遷移前只重對第 2 句：%+v", b.AlignPatch)
	}
	f.migrate(nil)
	a := f.evaluate(f.newV)
	if a.AlignPatch == nil || a.AlignPatch.Realign() != 1 || a.Status.Karaoke != b.Status.Karaoke {
		t.Fatalf("遷移後也只重對第 2 句：%+v %+v", a.AlignPatch, a.Status)
	}
}

func TestPinsWithoutCache(t *testing.T) {
	got := pins("私達の何処へ何気ない明日", nil, nil, false)
	want := map[string]string{"私": "わたくし", "何": "なん", "明日": "あす"}
	rs := []rune("私達の何処へ何気ない明日")
	if len(got) != 3 {
		t.Fatalf("%+v", got)
	}
	for _, r := range got {
		if want[string(rs[r.Start:r.End])] != r.Reading {
			t.Fatalf("%+v", got)
		}
	}
}
