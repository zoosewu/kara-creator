package scheduler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	kara "github.com/zoosewu/kara-creator"
	"github.com/zoosewu/kara-creator/nas/internal/fakeworker"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

type env struct {
	t   *testing.T
	s   *Scheduler
	srv *httptest.Server
	dir string
}

func newEnv(t *testing.T, opt Options) *env {
	t.Helper()
	dir := t.TempDir()
	opt.Versions = kara.Current
	opt.WorkDir = filepath.Join(dir, "work")
	if opt.LeaseWait == 0 {
		opt.LeaseWait = 200 * time.Millisecond
	}
	s := New(opt)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go s.Run(ctx)
	srv := httptest.NewServer(s.Handler())
	t.Cleanup(srv.Close)
	return &env{t: t, s: s, srv: srv, dir: dir}
}

// input 寫一個輸入檔，回傳 Input。
func (e *env) input(name string, data []byte) Input {
	path := filepath.Join(e.dir, name)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		e.t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return Input{Path: path, SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
}

// worker 啟動一個假的 worker，測試結束時停掉。
func (e *env) worker(opt fakeworker.Options) {
	opt.NAS = e.srv.URL
	opt.Logf = e.t.Logf
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = fakeworker.Run(ctx, opt); close(done) }()
	e.t.Cleanup(func() { cancel(); <-done })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("等不到：" + what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type logs struct {
	mu    sync.Mutex
	lines []string
}

func (l *logs) add(s string) { l.mu.Lock(); l.lines = append(l.lines, s); l.mu.Unlock() }
func (l *logs) has(sub string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, s := range l.lines {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

func TestAllKinds(t *testing.T) {
	e := newEnv(t, Options{})
	e.worker(fakeworker.Options{Name: "pc"})
	audio := e.input("speech.wav", []byte("RIFF 假的音訊"))
	ctx := context.Background()

	var l logs
	res, err := e.s.Submit(ctx, Spec{Kind: wp.KindSeparate, Priority: wp.PriorityBatch, Song: "abc",
		Inputs: map[string]Input{wp.InputAudio: audio}, Params: wp.SeparateParams{Model: "htdemucs", Stems: 2},
		Outputs: []string{wp.FileVocals, wp.FileNoVocals}, Log: l.add})
	if err != nil {
		t.Fatal(err)
	}
	if res.Worker != "pc" {
		t.Errorf("worker = %q", res.Worker)
	}
	for _, name := range []string{wp.FileVocals, wp.FileNoVocals} {
		data, err := os.ReadFile(res.Files[name])
		if err != nil || string(data) != "RIFF 假的音訊" {
			t.Errorf("%s：%q %v", name, data, err)
		}
	}
	res.Cleanup()
	if _, err := os.Stat(filepath.Dir(res.Files[wp.FileVocals])); !os.IsNotExist(err) {
		t.Error("Cleanup 應該刪掉上傳的暫存檔")
	}
	if !l.has("[pc]") {
		t.Error("worker 的紀錄應該接到工作紀錄，前面標 worker 名稱")
	}

	res, err = e.s.Submit(ctx, Spec{Kind: wp.KindAlign, Priority: wp.PriorityBatch, Inputs: map[string]Input{wp.InputAudio: audio},
		Params: wp.AlignParams{Texts: []string{"一", "二"}}})
	if err != nil {
		t.Fatal(err)
	}
	var al wp.AlignResult
	if err := json.Unmarshal(res.Raw, &al); err != nil || len(al.Lines) != 2 {
		t.Fatalf("%s %v", res.Raw, err)
	}

	res, err = e.s.Submit(ctx, Spec{Kind: wp.KindReading, Priority: wp.PriorityRealtime, Params: wp.ReadingParams{Language: "ja", Texts: []string{"空"}}})
	if err != nil {
		t.Fatal(err)
	}
	var rd wp.ReadingResult
	if err := json.Unmarshal(res.Raw, &rd); err != nil || len(rd.Lines) != 1 {
		t.Fatalf("%s %v", res.Raw, err)
	}
	if e.s.Pending() != 0 {
		t.Error("做完的任務應該移出佇列")
	}
	ws := e.s.Workers()
	if len(ws) != 1 || !ws[0].Online || !ws[0].VersionOK || len(ws[0].Tasks) != 0 {
		t.Fatalf("%+v", ws)
	}
}

func TestPriorityAndCache(t *testing.T) {
	e := newEnv(t, Options{})
	a := e.input("a.wav", []byte("a"))
	b := e.input("b.wav", []byte("b"))
	c := e.input("c.wav", []byte("c"))
	var mu sync.Mutex
	var order []string
	submit := func(name, priority string, in Input) chan error {
		errc := make(chan error, 1)
		go func() {
			_, err := e.s.Submit(context.Background(), Spec{Kind: wp.KindAlign, Priority: priority,
				Inputs: map[string]Input{wp.InputAudio: in}, Params: wp.AlignParams{Texts: []string{"一"}},
				Started: func(string) { mu.Lock(); order = append(order, name); mu.Unlock() }})
			errc <- err
		}()
		waitFor(t, "排入 "+name, func() bool { return e.s.Pending() >= len(order)+1 })
		return errc
	}
	done := []chan error{
		submit("batch-a", wp.PriorityBatch, a),
		submit("batch-b", wp.PriorityBatch, b),
	}
	waitFor(t, "兩件排入", func() bool { return e.s.Pending() == 2 })
	done = append(done, submit("interactive-c", wp.PriorityInteractive, c))
	waitFor(t, "三件排入", func() bool { return e.s.Pending() == 3 })

	// worker 的快取裡已經有 b：同一級裡先做 b
	e.s.mu.Lock()
	e.s.workers["pc"] = &Worker{Name: "pc", Instance: "x", VersionOK: true, Kinds: []string{wp.KindAlign}, cached: map[string]bool{b.SHA256: true}}
	got := []string{}
	for range 3 {
		tk := e.s.pick(e.s.workers["pc"], wp.ChannelHeavy)
		got = append(got, tk.Kind+":"+tk.Inputs[wp.InputAudio].SHA256[:4])
		tk.state = leased
	}
	for _, tk := range e.s.tasks {
		tk.state = queued
	}
	delete(e.s.workers, "pc")
	e.s.mu.Unlock()
	want := []string{"align:" + c.SHA256[:4], "align:" + b.SHA256[:4], "align:" + a.SHA256[:4]}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("派工順序 %v，應該 %v（互動優先、同級先挑快取）", got, want)
	}

	e.worker(fakeworker.Options{Name: "pc", Channels: []string{wp.ChannelHeavy}})
	for _, errc := range done {
		if err := <-errc; err != nil {
			t.Fatal(err)
		}
	}
	if order[0] != "interactive-c" {
		t.Fatalf("實際派工順序 %v", order)
	}
}

func TestCacheAging(t *testing.T) {
	now := time.Now()
	e := newEnv(t, Options{Now: func() time.Time { return now }, CacheAging: time.Minute})
	old := e.input("old.wav", []byte("old"))
	fresh := e.input("fresh.wav", []byte("fresh"))
	go func() {
		_, _ = e.s.Submit(context.Background(), Spec{Kind: wp.KindAlign, Priority: wp.PriorityBatch, Inputs: map[string]Input{wp.InputAudio: old}, Params: 1})
	}()
	waitFor(t, "排入", func() bool { return e.s.Pending() == 1 })
	now = now.Add(2 * time.Minute)
	go func() {
		_, _ = e.s.Submit(context.Background(), Spec{Kind: wp.KindAlign, Priority: wp.PriorityBatch, Inputs: map[string]Input{wp.InputAudio: fresh}, Params: 1})
	}()
	waitFor(t, "排入", func() bool { return e.s.Pending() == 2 })
	e.s.mu.Lock()
	defer e.s.mu.Unlock()
	w := &Worker{Name: "pc", VersionOK: true, Kinds: []string{wp.KindAlign}, cached: map[string]bool{fresh.SHA256: true}}
	if tk := e.s.pick(w, wp.ChannelHeavy); tk.Inputs[wp.InputAudio].SHA256 != old.SHA256 {
		t.Fatal("等太久的任務應該優先，不再挑快取")
	}
}

func TestCancel(t *testing.T) {
	e := newEnv(t, Options{})
	audio := e.input("a.wav", []byte("a"))

	// 排隊中取消：直接移出佇列
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() {
		_, err := e.s.Submit(ctx, Spec{Kind: wp.KindAlign, Priority: wp.PriorityBatch, Inputs: map[string]Input{wp.InputAudio: audio}, Params: 1})
		errc <- err
	}()
	waitFor(t, "排入", func() bool { return e.s.Pending() == 1 })
	cancel()
	if err := <-errc; !errors.Is(err, ErrCancelled) {
		t.Fatal(err)
	}
	if e.s.Pending() != 0 {
		t.Fatal("排隊中的任務取消後應該移出佇列")
	}

	// 處理中取消：Submit 立刻回傳，worker 下一次心跳收到 cancel
	e.worker(fakeworker.Options{Name: "pc", Delay: time.Hour})
	ctx, cancel = context.WithCancel(context.Background())
	started := make(chan struct{})
	go func() {
		_, err := e.s.Submit(ctx, Spec{Kind: wp.KindAlign, Priority: wp.PriorityBatch, Inputs: map[string]Input{wp.InputAudio: audio},
			Params: wp.AlignParams{Texts: []string{"一"}}, Started: func(string) { close(started) }})
		errc <- err
	}()
	<-started
	cancel()
	if err := <-errc; !errors.Is(err, ErrCancelled) {
		t.Fatal(err)
	}
	waitFor(t, "worker 收到取消、任務結束", func() bool { return e.s.Pending() == 0 })
}

// rawWorker 用 HTTP 直接扮演 worker（測試失聯、失敗）。
type rawWorker struct {
	t        *testing.T
	url      string
	instance string
}

func (r rawWorker) post(path string, body any) (*http.Response, []byte) {
	data, _ := json.Marshal(body)
	resp, err := http.Post(r.url+wp.Prefix+path, "application/json", bytes.NewReader(data))
	if err != nil {
		r.t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func (e *env) raw(name string) rawWorker {
	r := rawWorker{t: e.t, url: e.srv.URL, instance: name + "-instance"}
	resp, _ := r.post("/hello", wp.Hello{Name: name, Instance: r.instance, Versions: kara.Current,
		Kinds: []string{wp.KindAlign}, Channels: []string{wp.ChannelHeavy}})
	if resp.StatusCode != 200 {
		e.t.Fatal(resp.Status)
	}
	return r
}

func (r rawWorker) lease() wp.Task {
	resp, body := r.post("/lease", wp.LeaseRequest{Instance: r.instance, Channel: wp.ChannelHeavy})
	if resp.StatusCode != 200 {
		r.t.Fatalf("lease：%s", resp.Status)
	}
	var tk wp.Task
	_ = json.Unmarshal(body, &tk)
	return tk
}

func TestLostWorkerReassigned(t *testing.T) {
	e := newEnv(t, Options{Lease: 200 * time.Millisecond})
	audio := e.input("a.wav", []byte("a"))
	var l logs
	errc := make(chan error, 1)
	go func() {
		_, err := e.s.Submit(context.Background(), Spec{Kind: wp.KindAlign, Priority: wp.PriorityBatch,
			Inputs: map[string]Input{wp.InputAudio: audio}, Params: wp.AlignParams{Texts: []string{"一"}}, Log: l.add})
		errc <- err
	}()
	waitFor(t, "排入", func() bool { return e.s.Pending() == 1 })
	lost := e.raw("lost")
	tk := lost.lease()
	// 不送心跳：租約到期後改派給下一台
	e.worker(fakeworker.Options{Name: "pc"})
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
	if !l.has("失聯") {
		t.Error("應該記一筆失聯、改派")
	}
	// 失聯的 worker 之後才回報：任務已經不是它的
	resp, _ := lost.post("/tasks/"+tk.ID+"/complete", wp.Complete{Instance: lost.instance, Result: json.RawMessage(`{}`)})
	if resp.StatusCode != http.StatusNotFound && resp.StatusCode != http.StatusConflict {
		t.Fatalf("太晚的回報應該被拒絕：%s", resp.Status)
	}
}

func TestFail(t *testing.T) {
	e := newEnv(t, Options{MaxReassign: 1})
	audio := e.input("a.wav", []byte("a"))
	errc := make(chan error, 1)
	submit := func() {
		go func() {
			_, err := e.s.Submit(context.Background(), Spec{Kind: wp.KindAlign, Priority: wp.PriorityBatch,
				Inputs: map[string]Input{wp.InputAudio: audio}, Params: 1})
			errc <- err
		}()
		waitFor(t, "排入", func() bool { return e.s.Pending() == 1 })
	}
	w := e.raw("pc")

	// 可以重試的失敗：放回佇列；超過改派次數就失敗
	submit()
	for range 2 {
		tk := w.lease()
		w.post("/tasks/"+tk.ID+"/fail", wp.Fail{Instance: w.instance, Error: "顯示卡記憶體不足", Retryable: true})
	}
	var te *TaskError
	if err := <-errc; !errors.As(err, &te) || !strings.Contains(te.Message, "顯示卡記憶體不足") {
		t.Fatalf("%v", err)
	}

	// 不能重試的失敗：直接失敗，訊息原樣給使用者
	submit()
	tk := w.lease()
	w.post("/tasks/"+tk.ID+"/fail", wp.Fail{Instance: w.instance, Error: "歌詞的讀音比人聲長度還多，無法對齊"})
	if err := <-errc; !errors.As(err, &te) || te.Message != "歌詞的讀音比人聲長度還多，無法對齊" {
		t.Fatalf("%v", err)
	}
}

func TestByeAndRestart(t *testing.T) {
	e := newEnv(t, Options{})
	audio := e.input("a.wav", []byte("a"))
	errc := make(chan error, 1)
	go func() {
		_, err := e.s.Submit(context.Background(), Spec{Kind: wp.KindAlign, Priority: wp.PriorityBatch,
			Inputs: map[string]Input{wp.InputAudio: audio}, Params: wp.AlignParams{Texts: []string{"一"}}})
		errc <- err
	}()
	waitFor(t, "排入", func() bool { return e.s.Pending() == 1 })
	w := e.raw("pc")
	w.lease()
	// 同名的 worker 重新啟動（新的 instance）：舊的手上的任務立刻改派
	e.worker(fakeworker.Options{Name: "pc"})
	if err := <-errc; err != nil {
		t.Fatal(err)
	}
}

func TestByeKeepsLastSeen(t *testing.T) {
	e := newEnv(t, Options{})
	w := e.raw("pc")
	if ws := e.s.Workers(); len(ws) != 1 || !ws[0].Online {
		t.Fatalf("%+v", ws)
	}
	if resp, _ := w.post("/bye", wp.Bye{Instance: w.instance}); resp.StatusCode != http.StatusNoContent {
		t.Fatal(resp.Status)
	}
	// 說了 bye 立刻離線，但「最後連線」要是剛剛，不是零值（畫面會顯示成西元 1 年）
	if ws := e.s.Workers(); ws[0].Online || time.Since(ws[0].LastSeen) > time.Minute {
		t.Fatalf("%+v", ws[0])
	}
	e.raw("pc")
	if ws := e.s.Workers(); !ws[0].Online {
		t.Fatal("重新 hello 要回到線上")
	}
}

func TestVersionAndToken(t *testing.T) {
	e := newEnv(t, Options{Token: "secret"})
	old := kara.Current
	old.Align--
	err := fakeworker.Run(context.Background(), fakeworker.Options{NAS: e.srv.URL, Name: "old", Token: "secret", Versions: &old, Logf: t.Logf})
	if !errors.Is(err, fakeworker.ErrVersion) {
		t.Fatalf("版本不同應該被拒絕：%v", err)
	}
	ws := e.s.Workers()
	if len(ws) != 1 || ws[0].VersionOK || len(ws[0].Diff) != 1 {
		t.Fatalf("UI 要能顯示版本不同：%+v", ws)
	}
	resp, err := http.Post(e.srv.URL+wp.Prefix+"/hello", "application/json", strings.NewReader("{}"))
	if err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("沒帶 token 應該被拒絕：%v %v", resp.Status, err)
	}
}

func TestDisabled(t *testing.T) {
	e := newEnv(t, Options{})
	e.s.SetDisabled("pc", true)
	e.worker(fakeworker.Options{Name: "pc"})
	audio := e.input("a.wav", []byte("a"))
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	_, err := e.s.Submit(ctx, Spec{Kind: wp.KindAlign, Priority: wp.PriorityBatch, Inputs: map[string]Input{wp.InputAudio: audio},
		Params: wp.AlignParams{Texts: []string{"一"}}})
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("停用的 worker 不該領到任務：%v", err)
	}
	e.s.SetDisabled("pc", false)
	if _, err := e.s.Submit(context.Background(), Spec{Kind: wp.KindAlign, Priority: wp.PriorityBatch,
		Inputs: map[string]Input{wp.InputAudio: audio}, Params: wp.AlignParams{Texts: []string{"一"}}}); err != nil {
		t.Fatal(err)
	}
}
