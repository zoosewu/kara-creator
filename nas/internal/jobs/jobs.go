// Package jobs 是使用者層級的工作佇列（docs/nas-server.md「排程器」）。
//
// 一件工作 = 一首歌 + 步驟（download、separate、karaoke、check、retime）+ 選項。
//   - 下載最多同時 2 首；下載完還有其他步驟的，移到處理佇列排隊
//   - 同一首歌同時只有一件工作在跑（後來的排隊等前一件結束）
//   - 處理中的批次工作有上限（依線上的 AI 伺服器數量，避免一次抽出上百首的暫存音軌）；互動工作不受限
//   - 還沒結束的工作與最近的紀錄寫到 cache/jobs.json，重新啟動後繼續排隊
//   - 佇列清空時（以及曲庫有變動、安靜 30 秒後）收尾一次（資料備份）
package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/zoosewu/kara-creator/nas/internal/store"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// 步驟。
const (
	StepDownload = "download"
	StepSeparate = "separate"
	StepKaraoke  = "karaoke"
	StepCheck    = "check"
	StepRetime   = "retime"
	StepWrapup   = "wrapup"
)

// Steps 是步驟的順序。
var Steps = []string{StepDownload, StepSeparate, StepKaraoke, StepCheck, StepRetime}

// StepNames 是步驟的名稱（畫面顯示）。
var StepNames = map[string]string{StepDownload: "下載", StepSeparate: "去人聲", StepKaraoke: "製作伴唱帶",
	StepCheck: "檢查對時", StepRetime: "AI 重新對時", StepWrapup: "收尾"}

// 狀態。
const (
	Queued    = "queued"
	Running   = "running"
	Done      = "done"
	Failed    = "failed"
	Cancelled = "cancelled"
)

// 佇列。
const (
	LaneDownload = "download"
	LaneProcess  = "process"
	LaneSystem   = "system" // 收尾
)

// Options 是工作的選項。
type Options struct {
	Force     bool   `json:"force,omitempty"`      // 全部重做（含覆蓋手改的字幕）
	Realign   bool   `json:"realign,omitempty"`    // 重新對時
	Line      int    `json:"line,omitempty"`       // retime：第幾句（從 0 起算）
	Mode      string `json:"mode,omitempty"`       // retime：from | line
	AudioOnly bool   `json:"audio_only,omitempty"` // download：只要音訊
	Folder    string `json:"folder,omitempty"`     // download：新歌放進哪個資料夾
	Lyrics    string `json:"lyrics,omitempty"`     // download：一起送來的歌詞
}

// Job 是一件工作。
type Job struct {
	ID       string    `json:"id"`
	Steps    []string  `json:"steps"`
	Song     string    `json:"song"` // 歌曲 id；下載完才知道
	URL      string    `json:"url,omitempty"`
	Title    string    `json:"title"`
	Options  Options   `json:"options"`
	Priority string    `json:"priority"` // interactive | batch
	Status   string    `json:"status"`
	Lane     string    `json:"lane"`
	Stage    string    `json:"stage"`
	Progress *float64  `json:"progress"`
	Error    string    `json:"error,omitempty"`
	Logs     []string  `json:"logs,omitempty"`
	Created  time.Time `json:"created"`
	Started  time.Time `json:"started,omitzero"`
	Finished time.Time `json:"finished,omitzero"`

	cancelling bool
	cancel     context.CancelFunc
}

// Summary 是工作的摘要（列表與 SSE 用，不含紀錄）。
type Summary struct {
	ID         string    `json:"id"`
	Steps      []string  `json:"steps"`
	Song       string    `json:"song"`
	URL        string    `json:"url,omitempty"`
	Title      string    `json:"title"`
	Priority   string    `json:"priority"`
	Status     string    `json:"status"`
	Lane       string    `json:"lane"`
	Stage      string    `json:"stage"`
	Progress   *float64  `json:"progress"`
	Error      string    `json:"error,omitempty"`
	LogSize    int       `json:"log_size"`
	Cancelling bool      `json:"cancelling"`
	Force      bool      `json:"force"`
	Realign    bool      `json:"realign"`
	Created    time.Time `json:"created"`
	Started    time.Time `json:"started,omitzero"`
	Finished   time.Time `json:"finished,omitzero"`
}

func (j *Job) summary() Summary {
	return Summary{ID: j.ID, Steps: j.Steps, Song: j.Song, URL: j.URL, Title: j.Title, Priority: j.Priority,
		Status: j.Status, Lane: j.Lane, Stage: j.Stage, Progress: j.Progress, Error: j.Error, LogSize: len(j.Logs),
		Cancelling: j.cancelling && j.Status == Running, Force: j.Options.Force, Realign: j.Options.Realign,
		Created: j.Created, Started: j.Started, Finished: j.Finished}
}

func active(status string) bool { return status == Queued || status == Running }

// Runner 是實際做事的地方（pipeline 與下載）。
type Runner interface {
	Download(ctx context.Context, job Job, env Env) (songID, title string, err error)
	Process(ctx context.Context, job Job, env Env) error // separate / karaoke / check / retime
	Finished(job Job, env Env)                           // 處理佇列的工作結束後（含失敗、取消）：匯出
}

// Env 是工作執行時的紀錄與進度。
type Env struct {
	Log      func(line string)
	Progress func(p float64) // 負值 = 不知道
	Stage    func(name string)
}

// Config 是佇列的設定。
type Config struct {
	Runner          Runner
	Path            string     // cache/jobs.json
	DownloadSlots   int        // 同時下載幾首（預設 2）
	ProcessLimit    func() int // 處理中的批次工作上限（預設 2）
	OnIdle          func(round []Summary)
	OnEvent         func(kind string, data any) // job、job.log
	Now             func() time.Time
	MaxLogLines     int // 每件工作最多留幾行紀錄（預設 5000）
	KeepFinished    int // 保留幾件結束的工作（預設 200）
	KeepFinishedAge time.Duration
}

// Manager 是工作佇列。
type Manager struct {
	cfg Config

	mu     sync.Mutex
	jobs   map[string]*Job
	order  []string // 建立順序
	round  []Summary
	seq    int
	wg     sync.WaitGroup
	closed bool
}

// ErrNotFound 表示找不到工作。
var ErrNotFound = errors.New("找不到工作")

// New 建立佇列並讀回上次沒做完的工作。
func New(cfg Config) *Manager {
	if cfg.DownloadSlots == 0 {
		cfg.DownloadSlots = 2
	}
	if cfg.ProcessLimit == nil {
		cfg.ProcessLimit = func() int { return 2 }
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxLogLines == 0 {
		cfg.MaxLogLines = 5000
	}
	if cfg.KeepFinished == 0 {
		cfg.KeepFinished = 200
	}
	if cfg.KeepFinishedAge == 0 {
		cfg.KeepFinishedAge = 7 * 24 * time.Hour
	}
	m := &Manager{cfg: cfg, jobs: map[string]*Job{}}
	m.load()
	return m
}

// Start 開始執行排隊中的工作（讀回來的工作也從這裡開始）。
func (m *Manager) Start() {
	m.mu.Lock()
	m.dispatch()
	m.mu.Unlock()
}

// Request 是新增工作的要求。
type Request struct {
	Steps    []string
	Song     string
	URL      string
	Title    string
	Options  Options
	Priority string
}

// Submit 排入一件工作。
func (m *Manager) Submit(req Request) (Summary, error) {
	var steps []string
	for _, s := range Steps {
		for _, want := range req.Steps {
			if s == want && !contains(steps, s) {
				steps = append(steps, s)
			}
		}
	}
	if len(steps) == 0 {
		return Summary{}, errors.New("至少要有一個步驟")
	}
	for _, s := range req.Steps {
		if !contains(Steps, s) {
			return Summary{}, fmt.Errorf("不認得的步驟：%s", s)
		}
	}
	if contains(steps, StepDownload) && req.URL == "" {
		return Summary{}, errors.New("下載需要網址")
	}
	if !contains(steps, StepDownload) && req.Song == "" {
		return Summary{}, errors.New("需要指定歌曲")
	}
	if req.Priority == "" {
		req.Priority = wp.PriorityBatch
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return Summary{}, errors.New("伺服器正在關閉")
	}
	m.seq++
	j := &Job{ID: fmt.Sprintf("%s-%d", m.cfg.Now().Format("150405"), m.seq), Steps: steps, Song: req.Song, URL: req.URL,
		Title: req.Title, Options: req.Options, Priority: req.Priority, Status: Queued, Lane: LaneProcess, Created: m.cfg.Now()}
	if j.Title == "" {
		j.Title = first(req.URL, req.Song)
	}
	where := "處理佇列"
	if contains(steps, StepDownload) {
		j.Lane, where = LaneDownload, "下載佇列"
	}
	extra := ""
	if req.Options.Force {
		extra = "（強制重做）"
	} else if req.Options.Realign {
		extra = "（重新對時）"
	}
	names := make([]string, len(steps))
	for i, s := range steps {
		names[i] = StepNames[s]
	}
	m.jobs[j.ID] = j
	m.order = append(m.order, j.ID)
	m.logLocked(j, "排入%s：%s%s", where, strings.Join(names, " → "), extra)
	m.changed(j)
	m.dispatch()
	return j.summary(), nil
}

// Cancel 取消工作：排隊中的直接取消；處理中的在下一個安全點停下（AI 任務不等 worker）。
func (m *Manager) Cancel(id string) (Summary, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Summary{}, ErrNotFound
	}
	switch {
	case j.Status == Queued:
		j.Status, j.Finished = Cancelled, m.cfg.Now()
		m.logLocked(j, "已取消（尚未開始）")
		m.changed(j)
		m.finishedLocked(j)
		m.dispatch()
	case j.Status == Running && !j.cancelling:
		j.cancelling = true
		m.logLocked(j, "取消中…")
		if j.cancel != nil {
			j.cancel()
		}
		m.changed(j)
	}
	return j.summary(), nil
}

// List 回傳所有工作的摘要（新的在前）。
func (m *Manager) List() []Summary {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Summary, 0, len(m.order))
	for i := len(m.order) - 1; i >= 0; i-- {
		out = append(out, m.jobs[m.order[i]].summary())
	}
	return out
}

// Get 回傳一件工作的摘要。
func (m *Manager) Get(id string) (Summary, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return Summary{}, false
	}
	return j.summary(), true
}

// Logs 回傳第 offset 行之後的紀錄。
func (m *Manager) Logs(id string, offset int) ([]string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	j, ok := m.jobs[id]
	if !ok {
		return nil, false
	}
	if offset < 0 || offset > len(j.Logs) {
		offset = len(j.Logs)
	}
	return append([]string(nil), j.Logs[offset:]...), true
}

// Busy 回傳還沒結束的工作所對應的歌曲：歌曲 id → 工作摘要（同一首有好幾件時取正在跑的，否則最早的）。
func (m *Manager) Busy() map[string]Summary {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]Summary{}
	for _, id := range m.order {
		j := m.jobs[id]
		if j.Song == "" || !active(j.Status) {
			continue
		}
		if cur, ok := out[j.Song]; !ok || (cur.Status != Running && j.Status == Running) {
			out[j.Song] = j.summary()
		}
	}
	return out
}

// Idle 表示下載與處理佇列都沒有排隊或處理中的工作（收尾不算）。
func (m *Manager) Idle() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.idleLocked()
}

func (m *Manager) idleLocked() bool {
	for _, j := range m.jobs {
		if active(j.Status) && j.Lane != LaneSystem {
			return false
		}
	}
	return true
}

// Kick 重新檢查能不能開始新的工作（例如 AI 伺服器上線，處理上限變大）。
func (m *Manager) Kick() {
	m.mu.Lock()
	m.dispatch()
	m.mu.Unlock()
}

// Shutdown 關閉：取消處理中的工作（排隊中的留著，下次啟動繼續），最多等 timeout 讓它們停下，然後存檔。
func (m *Manager) Shutdown(timeout time.Duration) {
	m.mu.Lock()
	m.closed = true
	for _, j := range m.jobs {
		if j.Status == Running && j.cancel != nil && j.Lane != LaneSystem {
			j.cancel()
		}
	}
	m.mu.Unlock()
	done := make(chan struct{})
	go func() { m.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(timeout):
	}
	m.mu.Lock()
	m.saveLocked()
	m.mu.Unlock()
}

// ---- 執行 ----------------------------------------------------------------------

// dispatch 開始可以開始的工作（持有鎖時呼叫）。
func (m *Manager) dispatch() {
	if m.closed {
		return
	}
	running := map[string]int{}
	busySong := map[string]bool{}
	batch := 0
	for _, j := range m.jobs {
		if j.Status == Running {
			running[j.Lane]++
			if j.Song != "" {
				busySong[j.Song] = true
			}
			if j.Lane == LaneProcess && j.Priority != wp.PriorityInteractive {
				batch++
			}
		}
	}
	limit := m.cfg.ProcessLimit()
	for _, id := range m.order {
		j := m.jobs[id]
		if j.Status != Queued {
			continue
		}
		switch j.Lane {
		case LaneDownload:
			if running[LaneDownload] >= m.cfg.DownloadSlots {
				continue
			}
			running[LaneDownload]++
		case LaneProcess:
			if busySong[j.Song] {
				continue // 同一首歌一次只做一件
			}
			if j.Priority != wp.PriorityInteractive {
				if batch >= limit {
					continue
				}
				batch++
			}
			busySong[j.Song] = true
		}
		m.start(j)
	}
}

func (m *Manager) start(j *Job) {
	ctx, cancel := context.WithCancel(context.Background())
	j.Status, j.cancel, j.Progress = Running, cancel, nil
	if j.Started.IsZero() {
		j.Started = m.cfg.Now()
	}
	m.changed(j)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		m.run(ctx, j)
	}()
}

func (m *Manager) env(j *Job) Env {
	return Env{
		Log: func(line string) {
			m.mu.Lock()
			m.logLocked(j, "%s", line)
			m.mu.Unlock()
		},
		Progress: func(p float64) {
			m.mu.Lock()
			if p < 0 {
				j.Progress = nil
			} else {
				p = min(1, p)
				j.Progress = &p
			}
			m.notify(j) // 進度更新很頻繁：只通知，不存檔
			m.mu.Unlock()
		},
		Stage: func(name string) {
			m.mu.Lock()
			j.Stage = name
			m.notify(j)
			m.mu.Unlock()
		},
	}
}

// snapshot 回傳工作的複本（給 Runner；Runner 不能碰鎖住的欄位）。
func (m *Manager) snapshot(j *Job) Job {
	m.mu.Lock()
	defer m.mu.Unlock()
	cp := *j
	cp.Logs, cp.cancel = nil, nil
	return cp
}

func (m *Manager) run(ctx context.Context, j *Job) {
	env := m.env(j)
	var err error
	lane := m.snapshot(j).Lane
	if lane == LaneDownload {
		env.Stage(StepNames[StepDownload])
		var id, title string
		id, title, err = m.cfg.Runner.Download(ctx, m.snapshot(j), env)
		if err == nil {
			m.mu.Lock()
			j.Song, j.Title = id, first(title, j.Title)
			j.Options.Lyrics = "" // 已經存檔
			more := len(j.Steps) > 1
			if more && ctx.Err() == nil {
				// 還有其他步驟：移到處理佇列排隊，這條下載線可以接著下載下一首
				j.Lane, j.Status, j.Stage, j.Progress, j.cancel = LaneProcess, Queued, "", nil, nil
				m.logLocked(j, "下載完成，移到處理佇列排隊")
				m.changed(j)
				m.dispatch()
				m.mu.Unlock()
				return
			}
			m.mu.Unlock()
		}
	} else {
		err = m.cfg.Runner.Process(ctx, m.snapshot(j), env)
		env.Stage("匯出")
		m.cfg.Runner.Finished(m.snapshot(j), env)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	j.Finished, j.Progress, j.Stage, j.cancel = m.cfg.Now(), nil, "", nil
	switch {
	case err == nil:
		j.Status = Done
		m.logLocked(j, "完成")
	case m.closed && !j.cancelling:
		// 伺服器關閉而中斷：下次啟動重新排隊
		j.Status, j.Finished = Queued, time.Time{}
		m.logLocked(j, "伺服器關閉，下次啟動時繼續")
		m.saveLocked()
		return
	case ctx.Err() != nil || j.cancelling:
		j.Status = Cancelled
		m.logLocked(j, "已取消")
	default:
		j.Status, j.Error = Failed, err.Error()
		m.logLocked(j, "失敗：%s", err.Error())
	}
	m.changed(j)
	m.finishedLocked(j)
	m.dispatch()
}

// finishedLocked 一件工作結束；這是最後一件的話（佇列清空）通知 OnIdle。
func (m *Manager) finishedLocked(j *Job) {
	if j.Lane == LaneSystem {
		return
	}
	m.round = append(m.round, j.summary())
	if !m.idleLocked() || m.closed {
		return
	}
	round := m.round
	m.round = nil
	if m.cfg.OnIdle != nil {
		go m.cfg.OnIdle(round)
	}
}

// RunSystem 執行收尾工作（備份）：在自己的 goroutine 執行，紀錄一樣顯示在工作清單裡，
// 不佔用下載 / 處理佇列，也不算進「佇列清空」的判斷。
func (m *Manager) RunSystem(title string, fn func(ctx context.Context, log func(string)) error) Summary {
	m.mu.Lock()
	m.seq++
	j := &Job{ID: fmt.Sprintf("%s-%d", m.cfg.Now().Format("150405"), m.seq), Steps: []string{StepWrapup}, Title: title,
		Priority: wp.PriorityBatch, Status: Running, Lane: LaneSystem, Stage: StepNames[StepWrapup],
		Created: m.cfg.Now(), Started: m.cfg.Now()}
	ctx, cancel := context.WithCancel(context.Background())
	j.cancel = cancel
	m.jobs[j.ID] = j
	m.order = append(m.order, j.ID)
	m.changed(j)
	sum := j.summary()
	m.mu.Unlock()
	env := m.env(j)
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		defer cancel()
		err := fn(ctx, env.Log)
		m.mu.Lock()
		defer m.mu.Unlock()
		j.Finished, j.Stage, j.cancel = m.cfg.Now(), "", nil
		if err != nil {
			j.Status, j.Error = Failed, err.Error()
			m.logLocked(j, "失敗：%s", err.Error())
		} else {
			j.Status = Done
		}
		m.changed(j)
	}()
	return sum
}

// ---- 紀錄、事件、存檔 ----------------------------------------------------------

func (m *Manager) logLocked(j *Job, format string, a ...any) {
	line := m.cfg.Now().Format("15:04:05") + "  " + fmt.Sprintf(format, a...)
	j.Logs = append(j.Logs, line)
	if over := len(j.Logs) - m.cfg.MaxLogLines; over > 0 {
		j.Logs = append([]string{"…（前面的紀錄太長，已省略）"}, j.Logs[over+1:]...)
	}
	if m.cfg.OnEvent != nil {
		m.cfg.OnEvent("job.log", map[string]any{"id": j.ID, "lines": []string{line}})
	}
}

// changed 通知工作有變化並存檔（持有鎖時呼叫）。
func (m *Manager) changed(j *Job) {
	m.notify(j)
	m.saveLocked()
}

func (m *Manager) notify(j *Job) {
	if m.cfg.OnEvent != nil {
		m.cfg.OnEvent("job", j.summary())
	}
}

type saved struct {
	Version int    `json:"version"`
	Seq     int    `json:"seq"`
	Jobs    []*Job `json:"jobs"`
}

// saveLocked 存還沒結束的工作與最近的紀錄（最多 KeepFinished 件、KeepFinishedAge 以內）。
func (m *Manager) saveLocked() {
	if m.cfg.Path == "" {
		return
	}
	cutoff := m.cfg.Now().Add(-m.cfg.KeepFinishedAge)
	var keep []string
	finished := 0
	for i := len(m.order) - 1; i >= 0; i-- {
		j := m.jobs[m.order[i]]
		if active(j.Status) && j.Lane != LaneSystem {
			keep = append(keep, j.ID)
			continue
		}
		if !active(j.Status) && finished < m.cfg.KeepFinished && j.Finished.After(cutoff) {
			keep = append(keep, j.ID)
			finished++
			continue
		}
		if !active(j.Status) {
			delete(m.jobs, j.ID) // 太舊的紀錄丟掉
		}
	}
	sort.Slice(keep, func(a, b int) bool { return indexOf(m.order, keep[a]) < indexOf(m.order, keep[b]) })
	var order []string
	for _, id := range m.order {
		if _, ok := m.jobs[id]; ok {
			order = append(order, id)
		}
	}
	m.order = order
	out := saved{Version: 1, Seq: m.seq}
	for _, id := range keep {
		out.Jobs = append(out.Jobs, m.jobs[id])
	}
	data, err := json.Marshal(out)
	if err == nil {
		_ = store.WriteFile(m.cfg.Path, data)
	}
}

func (m *Manager) load() {
	if m.cfg.Path == "" {
		return
	}
	data, err := os.ReadFile(m.cfg.Path)
	if err != nil {
		return
	}
	var in saved
	if json.Unmarshal(data, &in) != nil {
		return
	}
	m.seq = in.Seq
	for _, j := range in.Jobs {
		if j.Status == Running {
			// 上次執行到一半：重新排隊（每一步都看紀錄判斷，重做是安全的）
			j.Status, j.Stage, j.Progress = Queued, "", nil
			j.Logs = append(j.Logs, m.cfg.Now().Format("15:04:05")+"  伺服器重新啟動，重新排隊")
		}
		m.jobs[j.ID] = j
		m.order = append(m.order, j.ID)
	}
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func indexOf(list []string, v string) int {
	for i, x := range list {
		if x == v {
			return i
		}
	}
	return -1
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
