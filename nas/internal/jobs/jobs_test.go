package jobs

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// fakeRunner：每件工作等 release 通知才結束，可以觀察同時在跑的工作。
type fakeRunner struct {
	mu       sync.Mutex
	running  map[string]int // 歌曲 → 正在跑幾件
	maxSame  int
	started  []string
	release  chan string // 送歌曲 id 讓那件工作結束
	fail     map[string]error
	finished []string
	downErr  error    // 下載失敗
	lyrics   []string // 每次下載收到的歌詞
}

func newRunner() *fakeRunner {
	return &fakeRunner{running: map[string]int{}, release: make(chan string, 100), fail: map[string]error{}}
}

func (r *fakeRunner) Download(ctx context.Context, job Job, env Env) (string, string, error) {
	r.mu.Lock()
	r.lyrics = append(r.lyrics, job.Options.Lyrics)
	err := r.downErr
	r.mu.Unlock()
	if err != nil {
		return "", "", err
	}
	env.Log("下載 " + job.URL)
	env.Progress(0.5)
	return "song-" + strings.TrimPrefix(job.URL, "https://x/"), "下載的歌", nil
}

func (r *fakeRunner) Process(ctx context.Context, job Job, env Env) error {
	r.mu.Lock()
	r.running[job.Song]++
	r.maxSame = max(r.maxSame, r.running[job.Song])
	r.started = append(r.started, job.Song)
	r.mu.Unlock()
	defer func() { r.mu.Lock(); r.running[job.Song]--; r.mu.Unlock() }()
	env.Stage("處理中")
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case id := <-r.release:
			if id != job.Song {
				r.release <- id // 不是這件的，放回去
				time.Sleep(time.Millisecond)
				continue
			}
			return r.fail[job.Song]
		}
	}
}

func (r *fakeRunner) Finished(job Job, env Env) {
	r.mu.Lock()
	r.finished = append(r.finished, job.Song)
	r.mu.Unlock()
}

func (r *fakeRunner) startedCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.started)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("等不到：" + what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func status(m *Manager, id string) string {
	s, _ := m.Get(id)
	return s.Status
}

func TestLimitsAndSameSong(t *testing.T) {
	r := newRunner()
	m := New(Config{Runner: r, ProcessLimit: func() int { return 2 }})
	m.Start()
	var ids []string
	for _, song := range []string{"a", "a", "b", "c"} {
		s, err := m.Submit(Request{Steps: []string{StepKaraoke}, Song: song})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, s.ID)
	}
	// 上限 2，而且同一首歌（a）一次只做一件：先跑 a、b
	waitFor(t, "兩件開始", func() bool { return r.startedCount() == 2 })
	time.Sleep(50 * time.Millisecond)
	if r.startedCount() != 2 || status(m, ids[1]) != Queued || status(m, ids[3]) != Queued {
		t.Fatalf("started=%v", r.started)
	}
	// 互動工作不受上限限制
	inter, _ := m.Submit(Request{Steps: []string{StepRetime}, Song: "d", Priority: wp.PriorityInteractive})
	waitFor(t, "互動工作開始", func() bool { return status(m, inter.ID) == Running })

	r.release <- "a"
	waitFor(t, "第一件 a 結束", func() bool { return status(m, ids[0]) == Done })
	waitFor(t, "第二件 a 開始", func() bool { return status(m, ids[1]) == Running })
	for _, s := range []string{"a", "b", "c", "d"} {
		r.release <- s
	}
	waitFor(t, "全部結束", m.Idle)
	if r.maxSame != 1 {
		t.Fatalf("同一首歌同時跑了 %d 件", r.maxSame)
	}
	if len(r.finished) != 5 {
		t.Fatalf("每件處理工作結束都要匯出：%v", r.finished)
	}
}

func TestDownloadThenProcess(t *testing.T) {
	r := newRunner()
	m := New(Config{Runner: r})
	m.Start()
	s, err := m.Submit(Request{Steps: []string{StepDownload, StepKaraoke}, URL: "https://x/1", Options: Options{Lyrics: "歌詞"}})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "下載完、移到處理佇列", func() bool {
		cur, _ := m.Get(s.ID)
		return cur.Lane == LaneProcess && cur.Status == Running
	})
	cur, _ := m.Get(s.ID)
	if cur.Song != "song-1" || cur.Title != "下載的歌" {
		t.Fatalf("%+v", cur)
	}
	r.release <- "song-1"
	waitFor(t, "完成", func() bool { return status(m, s.ID) == Done })
	logs, _ := m.Logs(s.ID, 0)
	joined := strings.Join(logs, "\n")
	for _, want := range []string{"排入下載佇列：下載 → 製作伴唱帶", "下載 https://x/1", "移到處理佇列排隊", "完成"} {
		if !strings.Contains(joined, want) {
			t.Errorf("紀錄缺少 %q：\n%s", want, joined)
		}
	}
	if more, _ := m.Logs(s.ID, len(logs)-1); len(more) != 1 {
		t.Fatal("offset 之後的紀錄")
	}
}

func TestCancelFailAndIdle(t *testing.T) {
	r := newRunner()
	rounds := make(chan []Summary, 4)
	m := New(Config{Runner: r, ProcessLimit: func() int { return 1 }, OnIdle: func(round []Summary) { rounds <- round }})
	m.Start()
	running, _ := m.Submit(Request{Steps: []string{StepKaraoke}, Song: "a"})
	queued, _ := m.Submit(Request{Steps: []string{StepKaraoke}, Song: "b"})
	failing, _ := m.Submit(Request{Steps: []string{StepKaraoke}, Song: "c"})
	r.fail["c"] = errors.New("歌詞檔沒有內容")
	waitFor(t, "第一件開始", func() bool { return status(m, running.ID) == Running })

	if _, err := m.Cancel(queued.ID); err != nil || status(m, queued.ID) != Cancelled {
		t.Fatal("排隊中的取消")
	}
	if _, err := m.Cancel(running.ID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "處理中的取消", func() bool { return status(m, running.ID) == Cancelled })
	waitFor(t, "下一件開始", func() bool { return status(m, failing.ID) == Running })
	r.release <- "c"
	waitFor(t, "失敗", func() bool { return status(m, failing.ID) == Failed })
	if s, _ := m.Get(failing.ID); s.Error != "歌詞檔沒有內容" {
		t.Fatal(s.Error)
	}
	select {
	case round := <-rounds:
		if len(round) != 3 {
			t.Fatalf("這一輪結束的工作：%d 件", len(round))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("佇列清空時要收尾")
	}
	if _, err := m.Cancel("沒有"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestValidation(t *testing.T) {
	m := New(Config{Runner: newRunner()})
	for _, req := range []Request{{}, {Steps: []string{"x"}, Song: "a"}, {Steps: []string{StepDownload}}, {Steps: []string{StepKaraoke}}} {
		if _, err := m.Submit(req); err == nil {
			t.Errorf("應該拒絕：%+v", req)
		}
	}
	s, _ := m.Submit(Request{Steps: []string{StepKaraoke, StepSeparate, StepSeparate}, Song: "a"})
	if strings.Join(s.Steps, ",") != "separate,karaoke" {
		t.Fatalf("步驟要照順序、去掉重複：%v", s.Steps)
	}
}

func TestPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "jobs.json")
	r := newRunner()
	m := New(Config{Runner: r, Path: path, ProcessLimit: func() int { return 1 }})
	m.Start()
	a, _ := m.Submit(Request{Steps: []string{StepKaraoke}, Song: "a"})
	b, _ := m.Submit(Request{Steps: []string{StepKaraoke}, Song: "b"})
	waitFor(t, "a 開始", func() bool { return status(m, a.ID) == Running })
	m.Shutdown(time.Second)

	// 重新啟動：處理到一半的與排隊中的都重新排隊
	r2 := newRunner()
	m2 := New(Config{Runner: r2, Path: path, ProcessLimit: func() int { return 2 }})
	if status(m2, a.ID) != Queued || status(m2, b.ID) != Queued {
		t.Fatalf("a=%s b=%s", status(m2, a.ID), status(m2, b.ID))
	}
	m2.Start()
	waitFor(t, "重新開始", func() bool { return r2.startedCount() == 2 })
	logs, _ := m2.Logs(a.ID, 0)
	if !strings.Contains(strings.Join(logs, "\n"), "下次啟動時繼續") {
		t.Fatal(logs)
	}
	c, _ := m2.Submit(Request{Steps: []string{StepCheck}, Song: "c"})
	if c.ID == a.ID || c.ID == b.ID {
		t.Fatal("id 不能和讀回來的工作重複")
	}
	r2.release <- "a"
	r2.release <- "b"
	r2.release <- "c"
	waitFor(t, "全部結束", m2.Idle)
}

func TestRunSystem(t *testing.T) {
	m := New(Config{Runner: newRunner()})
	s := m.RunSystem("收尾", func(ctx context.Context, log func(string)) error {
		log("  . 資料備份：1 首")
		return nil
	})
	waitFor(t, "收尾完成", func() bool { return status(m, s.ID) == Done })
	if !m.Idle() {
		t.Fatal("收尾不算進佇列")
	}
	logs, _ := m.Logs(s.ID, 0)
	if len(logs) != 1 {
		t.Fatal(logs)
	}
}

func TestCrashRecovery(t *testing.T) {
	// 伺服器當掉：存檔裡是「處理中」，下次啟動重新排隊
	path := filepath.Join(t.TempDir(), "jobs.json")
	data := `{"version":1,"seq":3,"jobs":[{"id":"x-3","steps":["karaoke"],"song":"a","title":"a","priority":"batch",
		"status":"running","lane":"process","created":"2026-10-03T12:00:00Z"}]}`
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
	m := New(Config{Runner: newRunner(), Path: path})
	if status(m, "x-3") != Queued {
		t.Fatal(status(m, "x-3"))
	}
	logs, _ := m.Logs("x-3", 0)
	if !strings.Contains(strings.Join(logs, "\n"), "重新排隊") {
		t.Fatal(logs)
	}
}

func TestRetry(t *testing.T) {
	r := newRunner()
	r.downErr = errors.New("下載失敗：HTTP Error 403: Forbidden")
	m := New(Config{Runner: r})
	m.Start()
	s, _ := m.Submit(Request{Steps: []string{StepDownload, StepKaraoke}, URL: "https://x/1",
		Options: Options{Lyrics: "自己編的歌詞", Folder: "f1"}})
	waitFor(t, "下載失敗", func() bool { return status(m, s.ID) == Failed })
	if _, err := m.Retry("不存在"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	// 重試：附上的歌詞、資料夾都還在
	r.mu.Lock()
	r.downErr = nil
	r.mu.Unlock()
	again, err := m.Retry(s.ID)
	if err != nil || again.ID == s.ID || again.URL != "https://x/1" {
		t.Fatalf("%+v %v", again, err)
	}
	waitFor(t, "下載完", func() bool { cur, _ := m.Get(again.ID); return cur.Song == "song-1" })
	r.mu.Lock()
	got := append([]string(nil), r.lyrics...)
	r.mu.Unlock()
	if len(got) != 2 || got[1] != "自己編的歌詞" {
		t.Fatalf("重試要帶著原本附上的歌詞：%q", got)
	}
	r.release <- "song-1"
	waitFor(t, "完成", func() bool { return status(m, again.ID) == Done })
	if _, err := m.Retry(again.ID); err == nil {
		t.Fatal("成功的工作不能重試")
	}
}
