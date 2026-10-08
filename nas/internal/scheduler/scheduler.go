// Package scheduler 是 AI 任務的佇列與 worker 協定的 NAS 端（docs/worker-protocol.md、nas-server.md「排程器」）。
//
// 工作流程（pipeline）用 Submit 交出一件 AI 任務並等它做完；worker 主動來 /worker/v1/lease 領任務。
// NAS 不需要知道 worker 的位址，可以接很多台。
package scheduler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	kara "github.com/zoosewu/kara-creator"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// Options 是排程器的設定。
type Options struct {
	Versions    kara.Versions
	Token       string        // worker 共用 token；空字串 = 不檢查
	WorkDir     string        // 放 worker 上傳結果的暫存資料夾
	Lease       time.Duration // 租約長度（預設 60 秒）
	Heartbeat   time.Duration // worker 至少多久送一次心跳（預設 10 秒）
	LeaseWait   time.Duration // /lease 沒有任務時最多等多久（預設 25 秒）
	MaxReassign int           // 同一件任務最多改派幾次（預設 2）
	CacheAging  time.Duration // 等超過這麼久的任務不再挑快取（預設 2 分鐘）
	Fonts       FontSource    // worker 索取字型
	Now         func() time.Time
}

// FontSource 依 sha256 找字型檔的路徑。
type FontSource func(sha256 string) (path string, ok bool)

// Input 是任務的一個輸入檔。
type Input struct {
	Path   string
	SHA256 string
	Size   int64
}

// Spec 是要交給 AI 的一件任務。
type Spec struct {
	Kind     string
	Priority string // wp.Priority*
	Song     string
	Inputs   map[string]Input
	Params   any
	Outputs  []string            // 允許上傳的檔名
	Log      func(line string)   // worker 的紀錄（前面已加上 worker 名稱）
	Progress func(p float64)     // 0–1
	Started  func(worker string) // worker 領走任務時
}

// Result 是任務的結果。上傳的檔案在 Files（檔名 → 暫存路徑），用完呼叫 Cleanup。
type Result struct {
	Worker string
	Raw    json.RawMessage
	Files  map[string]string
	dir    string
}

// Cleanup 刪掉上傳的暫存檔。
func (r *Result) Cleanup() {
	if r.dir != "" {
		_ = os.RemoveAll(r.dir)
	}
}

// 錯誤。
var (
	ErrCancelled = errors.New("已取消")
)

// TaskError 是 worker 回報的失敗（訊息直接給使用者看）。
type TaskError struct {
	Message string
}

func (e *TaskError) Error() string { return e.Message }

type state int

const (
	queued state = iota
	leased
	finished
)

type task struct {
	wp.Task
	spec     Spec
	created  time.Time
	state    state
	worker   string // 領走的 worker 名稱
	instance string
	deadline time.Time
	attempts int // 改派次數
	cancel   bool
	uploads  map[string]string // 檔名 → sha256（已上傳）
	dir      string
	done     chan outcome
}

type outcome struct {
	result *Result
	err    error
}

// Worker 是一台 AI worker 目前的狀態（給 UI 看）。
type Worker struct {
	Name      string        `json:"name"`
	Instance  string        `json:"-"`
	Hardware  wp.Hardware   `json:"hardware"`
	Versions  kara.Versions `json:"versions"`
	VersionOK bool          `json:"version_ok"`
	Diff      []string      `json:"version_diff,omitempty"`
	Kinds     []string      `json:"kinds"`
	Channels  []string      `json:"channels"`
	Disabled  bool          `json:"disabled"`
	LastSeen  time.Time     `json:"last_seen"`
	Online    bool          `json:"online"`
	Tasks     []WorkerTask  `json:"tasks"`
	cached    map[string]bool
	gone      bool // 說過 bye（不必等心跳逾時就算離線；重新 hello 會換成新的 Worker）
}

// WorkerTask 是 worker 手上的一件任務。
type WorkerTask struct {
	ID   string `json:"id"`
	Kind string `json:"kind"`
	Song string `json:"song"`
}

// Event 是狀態變化（給 SSE）。
type Event struct {
	Kind string // "worker"
	Name string
}

// Scheduler 是 AI 任務的佇列。
//
// Spec 的 Log、Progress 與 OnEvent 可能在持有排程器的鎖時被呼叫：callback 裡不能再呼叫排程器的方法。
type Scheduler struct {
	opt Options

	mu       sync.Mutex
	tasks    map[string]*task
	workers  map[string]*Worker // 依名稱
	disabled map[string]bool
	wake     chan struct{} // 有新任務時關閉並換新（廣播）
	seq      int

	OnEvent func(Event)
}

// New 建立排程器。
func New(opt Options) *Scheduler {
	if opt.Lease == 0 {
		opt.Lease = 60 * time.Second
	}
	if opt.Heartbeat == 0 {
		opt.Heartbeat = 10 * time.Second
	}
	if opt.LeaseWait == 0 {
		opt.LeaseWait = wp.LeaseWaitSeconds * time.Second
	}
	if opt.MaxReassign == 0 {
		opt.MaxReassign = 2
	}
	if opt.CacheAging == 0 {
		opt.CacheAging = 2 * time.Minute
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Scheduler{opt: opt, tasks: map[string]*task{}, workers: map[string]*Worker{},
		disabled: map[string]bool{}, wake: make(chan struct{})}
}

// Run 定期收回過期的租約，直到 ctx 結束。
func (s *Scheduler) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.expire()
		}
	}
}

// Submit 交出一件任務並等它做完。ctx 結束時取消任務並立刻回傳 ErrCancelled（不等 worker）。
func (s *Scheduler) Submit(ctx context.Context, spec Spec) (*Result, error) {
	params, err := json.Marshal(spec.Params)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.seq++
	id := fmt.Sprintf("t%d-%d", s.opt.Now().UnixMilli(), s.seq)
	t := &task{spec: spec, created: s.opt.Now(), uploads: map[string]string{}, done: make(chan outcome, 1)}
	t.Task = wp.Task{ID: id, Kind: spec.Kind, Channel: wp.ChannelOf(spec.Kind), Priority: spec.Priority, Song: spec.Song,
		Inputs: map[string]wp.Blob{}, Params: params, Outputs: spec.Outputs}
	for name, in := range spec.Inputs {
		t.Inputs[name] = wp.Blob{SHA256: in.SHA256, Size: in.Size}
	}
	s.tasks[id] = t
	s.broadcast()
	s.mu.Unlock()

	select {
	case out := <-t.done:
		return out.result, out.err
	case <-ctx.Done():
		s.mu.Lock()
		switch t.state {
		case queued:
			delete(s.tasks, id)
		case leased:
			t.cancel = true // worker 下一次心跳收到 cancel；結果送來時丟掉
		}
		s.mu.Unlock()
		return nil, ErrCancelled
	}
}

// broadcast 叫醒所有在等任務的 /lease（持有鎖時呼叫）。
func (s *Scheduler) broadcast() {
	close(s.wake)
	s.wake = make(chan struct{})
}

var priorityRank = map[string]int{wp.PriorityRealtime: 0, wp.PriorityInteractive: 1, wp.PriorityBatch: 2}

// pick 替 worker 挑一件任務（持有鎖時呼叫）。
func (s *Scheduler) pick(w *Worker, channel string) *task {
	if w.Disabled || !w.VersionOK {
		return nil
	}
	var candidates []*task
	for _, t := range s.tasks {
		if t.state == queued && t.Channel == channel && contains(w.Kinds, t.Kind) {
			candidates = append(candidates, t)
		}
	}
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(a, b int) bool {
		x, y := candidates[a], candidates[b]
		if priorityRank[x.Priority] != priorityRank[y.Priority] {
			return priorityRank[x.Priority] < priorityRank[y.Priority]
		}
		return x.created.Before(y.created) || x.created.Equal(y.created) && x.ID < y.ID
	})
	top := priorityRank[candidates[0].Priority]
	var same []*task
	for _, t := range candidates {
		if priorityRank[t.Priority] == top {
			same = append(same, t)
		}
	}
	// 等太久的任務不再挑快取（避免一直被跳過）；其次挑輸入檔已經在這台快取裡的
	if s.opt.Now().Sub(same[0].created) > s.opt.CacheAging {
		return same[0]
	}
	for _, t := range same {
		if len(t.Inputs) > 0 && allCached(w, t) {
			return t
		}
	}
	return same[0]
}

func allCached(w *Worker, t *task) bool {
	for _, b := range t.Inputs {
		if !w.cached[b.SHA256] {
			return false
		}
	}
	return true
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// requeue 把任務放回佇列（持有鎖時呼叫）；改派太多次就讓任務失敗。
func (s *Scheduler) requeue(t *task, reason string) {
	t.attempts++
	t.worker, t.instance = "", ""
	s.dropUploads(t)
	if t.cancel {
		delete(s.tasks, t.ID)
		return
	}
	if t.attempts > s.opt.MaxReassign {
		s.finish(t, nil, &TaskError{Message: reason + "，已經改派 " + fmt.Sprint(s.opt.MaxReassign) + " 次都沒有完成"})
		return
	}
	t.state = queued
	s.log(t, "  [!] "+reason+"，改派給其他 AI 伺服器")
	s.broadcast()
}

func (s *Scheduler) dropUploads(t *task) {
	if t.dir != "" {
		_ = os.RemoveAll(t.dir)
		t.dir = ""
	}
	t.uploads = map[string]string{}
}

// finish 結束任務並通知 Submit（持有鎖時呼叫）。
func (s *Scheduler) finish(t *task, r *Result, err error) {
	t.state = finished
	delete(s.tasks, t.ID)
	if w := s.workers[t.worker]; w != nil {
		w.Tasks = removeTask(w.Tasks, t.ID)
		s.event(w.Name)
	}
	if t.cancel {
		if r != nil {
			r.Cleanup()
		}
		return
	}
	t.done <- outcome{r, err}
}

func removeTask(list []WorkerTask, id string) []WorkerTask {
	out := list[:0]
	for _, x := range list {
		if x.ID != id {
			out = append(out, x)
		}
	}
	return out
}

// expire 收回過期的租約。
func (s *Scheduler) expire() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	for _, t := range s.tasks {
		if t.state == leased && now.After(t.deadline) {
			name := t.worker
			if w := s.workers[name]; w != nil {
				w.Tasks = removeTask(w.Tasks, t.ID)
				s.event(name)
			}
			s.requeue(t, "AI 伺服器「"+name+"」失聯")
		}
	}
}

func (s *Scheduler) log(t *task, line string) {
	if t.spec.Log != nil {
		t.spec.Log(line)
	}
}

func (s *Scheduler) event(name string) {
	if s.OnEvent != nil {
		s.OnEvent(Event{Kind: "worker", Name: name})
	}
}

// Workers 回傳所有 worker 的狀態（依名稱排序）。
func (s *Scheduler) Workers() []Worker {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.opt.Now()
	out := []Worker{}
	for _, w := range s.workers {
		cp := *w
		cp.Online = !w.gone && now.Sub(w.LastSeen) < s.opt.LeaseWait+10*time.Second
		cp.Tasks = append([]WorkerTask{}, w.Tasks...)
		cp.cached = nil
		out = append(out, cp)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Name < out[b].Name })
	return out
}

// SetDisabled 停用或啟用一台 worker（依名稱）。停用的 worker 領不到新任務，手上的任務照常做完。
func (s *Scheduler) SetDisabled(name string, disabled bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if disabled {
		s.disabled[name] = true
	} else {
		delete(s.disabled, name)
	}
	if w := s.workers[name]; w != nil {
		w.Disabled = disabled
	}
	s.event(name)
}

// Disabled 回傳停用中的 worker 名稱（持久化用）。
func (s *Scheduler) Disabled() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for name := range s.disabled {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// Pending 回傳還沒做完的任務數（給測試與 UI）。
func (s *Scheduler) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.tasks)
}

func (s *Scheduler) taskDir(t *task) (string, error) {
	if t.dir == "" {
		dir := filepath.Join(s.opt.WorkDir, "tasks", t.ID)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", err
		}
		t.dir = dir
	}
	return t.dir, nil
}
