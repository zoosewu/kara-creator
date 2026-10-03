// Package app 組裝 NAS 伺服器的所有元件（曲庫、排程器、處理流程、工作佇列、下載、手動放入、匯出、備份、字型、讀音），
// 並提供 API 用的操作。API 那層只做 HTTP 的轉換。
package app

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"

	kara "github.com/zoosewu/kara-creator"
	"github.com/zoosewu/kara-creator/nas/internal/backup"
	"github.com/zoosewu/kara-creator/nas/internal/config"
	"github.com/zoosewu/kara-creator/nas/internal/download"
	"github.com/zoosewu/kara-creator/nas/internal/events"
	"github.com/zoosewu/kara-creator/nas/internal/export"
	"github.com/zoosewu/kara-creator/nas/internal/fonts"
	"github.com/zoosewu/kara-creator/nas/internal/inbox"
	"github.com/zoosewu/kara-creator/nas/internal/jobs"
	"github.com/zoosewu/kara-creator/nas/internal/media"
	"github.com/zoosewu/kara-creator/nas/internal/pipeline"
	"github.com/zoosewu/kara-creator/nas/internal/readings"
	"github.com/zoosewu/kara-creator/nas/internal/scheduler"
	"github.com/zoosewu/kara-creator/nas/internal/store"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// App 是開啟中的 NAS 伺服器。
type App struct {
	Cfg      config.Config
	Versions kara.Versions
	Store    *store.Store
	Media    media.Tools
	Sched    *scheduler.Scheduler
	Pipe     *pipeline.Pipeline
	Jobs     *jobs.Manager
	Inbox    *inbox.Inbox
	Exporter *export.Exporter
	Backup   *backup.Backup
	Fonts    *fonts.Catalog
	Readings *readings.Cache
	Down     *download.Downloader
	Events   *events.Hub

	// ChangeDelay 是曲庫有變動後安靜多久才收尾（預設 30 秒；測試可以縮短）。
	ChangeDelay time.Duration
	Now         func() time.Time

	mu       sync.Mutex
	views    map[string]*SongView // 每首歌的狀態快取；nil = 要重算
	dirty    map[string]bool
	flush    *time.Timer // 合併短時間內的多次變動，再送 song 事件
	wrapup   *time.Timer
	inflight map[string]chan struct{} // 正在算的讀音（快取鍵 → 算完時關閉）
	ytdlp    string                   // yt-dlp 版本
	started  time.Time
}

// New 開啟曲庫、組裝所有元件（還沒開始執行，見 Run）。
func New(cfg config.Config) (*App, error) {
	st, err := store.Open(cfg.Library, cfg.Init)
	if err != nil {
		return nil, err
	}
	a := &App{Cfg: cfg, Versions: kara.Current, Store: st, Media: media.DefaultTools(cfg.Tools), Events: events.New(),
		ChangeDelay: 30 * time.Second, Now: time.Now, views: map[string]*SongView{}, dirty: map[string]bool{},
		inflight: map[string]chan struct{}{}, started: time.Now()}
	if d := os.Getenv("KARA_CHANGE_DELAY"); d != "" {
		if v, err := time.ParseDuration(d); err == nil {
			a.ChangeDelay = v
		}
	}

	if a.Fonts, err = fonts.Scan(st.Path(store.FontsDir), a.loadFontCache()); err != nil {
		return nil, err
	}
	a.saveFontCache()
	if a.Readings, err = readings.OpenCache(st.Path(store.CacheDir, "readings.jsonl"), a.Versions.Reading); err != nil {
		return nil, err
	}

	a.Sched = scheduler.New(scheduler.Options{Versions: a.Versions, Token: cfg.WorkerToken, WorkDir: st.Path(store.WorkDir),
		Fonts: a.Fonts.Path})
	for _, name := range a.loadDisabled() {
		a.Sched.SetDisabled(name, true)
	}
	a.Sched.OnEvent = func(e scheduler.Event) {
		a.Events.Publish("worker", map[string]string{"name": e.Name})
		go a.Jobs.Kick() // 排程器的鎖還沒放開：另外開 goroutine，避免互相等待
	}

	a.Pipe = pipeline.New(pipeline.Deps{Store: st, Media: a.Media, AI: a.Sched, Fonts: a.fontFor, Versions: a.Versions})
	a.Exporter = &export.Exporter{Root: st.Path(store.ExportDir)}
	a.Backup = &backup.Backup{Store: st, Dir: cfg.Data}
	a.Down = &download.Downloader{Store: st, Media: a.Media, YTDLP: tool(cfg.Tools, "yt-dlp", "yt-dlp_linux", "yt-dlp_macos"),
		Deno: optionalTool(cfg.Tools, "deno"), FFmpeg: filepath.Dir(a.Media.FFmpeg)}
	a.Inbox = &inbox.Inbox{Store: st, Media: a.Media, Log: func(s string) { log.Print(s) }}
	a.Jobs = jobs.New(jobs.Config{Runner: &runner{a}, Path: st.Path(store.CacheDir, "jobs.json"),
		ProcessLimit: a.processLimit, OnIdle: a.onIdle, OnEvent: a.onJobEvent})

	st.OnChange = func(c store.Change) {
		if c.Kind == "song" {
			a.invalidate(c.ID)
		} else {
			a.Events.Publish("library", nil)
		}
	}
	return a, nil
}

// tool 找外部工具：--tools 資料夾優先，其次 PATH。
func tool(dir string, names ...string) string {
	for _, n := range names {
		if dir != "" {
			if p, err := exec.LookPath(filepath.Join(dir, n)); err == nil {
				return p
			}
		}
	}
	for _, n := range names {
		if p, err := exec.LookPath(n); err == nil {
			return p
		}
	}
	return names[0]
}

func optionalTool(dir, name string) string {
	if p := tool(dir, name); filepath.IsAbs(p) {
		return p
	}
	return ""
}

// Handler 回傳 worker 協定的 handler（/worker/v1）。
func (a *App) WorkerHandler() http.Handler { return a.Sched.Handler() }

// Run 開始執行：排程器、工作佇列、手動放入、檔案監看、yt-dlp 更新。ctx 結束時停止。
func (a *App) Run(ctx context.Context) {
	go a.Sched.Run(ctx)
	a.Jobs.Start()
	go func() {
		if err := a.Inbox.Watch(ctx, func(ids []string) {
			for _, id := range ids {
				a.invalidate(id)
			}
			a.Events.Publish("library", nil)
		}); err != nil {
			log.Printf("監看 inbox 失敗：%v", err)
		}
	}()
	go a.watchSongs(ctx)
	go a.updater(ctx)
	go func() {
		if _, err := a.syncExport(); err != nil {
			log.Printf("同步匯出資料夾失敗：%v", err)
		}
	}()
}

// Shutdown 關閉：處理中的工作停下（下次啟動繼續），存檔。
func (a *App) Shutdown() {
	a.Jobs.Shutdown(time.Second)
	a.mu.Lock()
	if a.wrapup != nil {
		a.wrapup.Stop()
	}
	a.mu.Unlock()
}

// processLimit：處理中的批次工作上限 = 2 + 每台可以做重工作的 AI 伺服器 2 件（夠讓每台都有下一件在準備）。
func (a *App) processLimit() int {
	n := 0
	for _, w := range a.Sched.Workers() {
		if w.Online && w.VersionOK && !w.Disabled && contains(w.Channels, wp.ChannelHeavy) {
			n++
		}
	}
	return 2 + 2*n
}

func (a *App) fontFor(language string) (wp.Font, bool) {
	f, ok := a.Fonts.ForLanguage(language, a.Store.Library().Settings.Fonts)
	return f.Proto(), ok
}

// ---- 收尾 ----------------------------------------------------------------------

func (a *App) onIdle(round []jobs.Summary) { a.runWrapup("佇列清空") }

// touched 記下曲庫有變動（改歌名、資料夾、歌詞、時間…）：安靜一段時間後收尾一次（連續操作只跑一次）。
func (a *App) touched() {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.wrapup != nil {
		a.wrapup.Stop()
	}
	a.wrapup = time.AfterFunc(a.ChangeDelay, func() {
		if a.Jobs.Idle() { // 還有工作在跑：等佇列清空時一起收尾
			a.runWrapup("曲庫有變動")
		}
	})
}

func (a *App) runWrapup(reason string) {
	a.Jobs.RunSystem("收尾（"+reason+"）", func(ctx context.Context, logf func(string)) error {
		return a.Backup.Run(ctx, logf)
	})
}

func (a *App) syncExport() (export.Result, error) {
	return a.Exporter.Sync(a.Store.Library(), export.Collect(a.Store))
}

// ---- yt-dlp 更新（Q10）----------------------------------------------------------

func (a *App) updater(ctx context.Context) {
	for {
		if v, err := a.Down.Version(ctx); err == nil {
			a.setYTDLP(v)
		}
		// 只更新放在 --tools 的執行檔（從系統套件裝的交給系統更新）
		if a.Cfg.Tools != "" && filepath.Dir(a.Down.YTDLP) == filepath.Clean(a.Cfg.Tools) {
			before, after, err := a.Down.Update(ctx)
			switch {
			case err != nil:
				log.Printf("更新 yt-dlp 失敗：%v", err)
			case before != after:
				log.Printf("yt-dlp 已更新：%s → %s", before, after)
				a.setYTDLP(after)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(24 * time.Hour):
		}
	}
}

func (a *App) setYTDLP(v string) {
	a.mu.Lock()
	a.ytdlp = v
	a.mu.Unlock()
}

// ---- 小檔案：字型雜湊快取、停用的 worker ----------------------------------------

func (a *App) loadFontCache() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(a.Store.Path(store.CacheDir, "fonts.json"))
	if err == nil {
		_ = json.Unmarshal(data, &out)
	}
	return out
}

func (a *App) saveFontCache() {
	// fonts.Scan 會把新算的雜湊加進同一個 map；這裡重新掃描結果寫回（字型很少，檔案很小）
	cache := map[string]string{}
	for _, f := range a.Fonts.List() {
		if p, ok := a.Fonts.Path(f.SHA256); ok {
			if st, err := os.Stat(p); err == nil {
				cache[fontKey(p, st)] = f.SHA256
			}
		}
	}
	if data, err := json.Marshal(cache); err == nil {
		_ = store.WriteFile(a.Store.Path(store.CacheDir, "fonts.json"), data)
	}
}

func fontKey(path string, st os.FileInfo) string {
	return path + "|" + itoa(st.Size()) + "|" + itoa(st.ModTime().UnixNano())
}

func (a *App) loadDisabled() []string {
	var names []string
	data, err := os.ReadFile(a.Store.Path(store.CacheDir, "workers.json"))
	if err == nil {
		_ = json.Unmarshal(data, &names)
	}
	return names
}

// SetWorkerDisabled 停用或啟用一台 AI 伺服器（依名稱，記在 cache/workers.json）。
func (a *App) SetWorkerDisabled(name string, disabled bool) error {
	a.Sched.SetDisabled(name, disabled)
	data, err := json.Marshal(a.Sched.Disabled())
	if err != nil {
		return err
	}
	return store.WriteFile(a.Store.Path(store.CacheDir, "workers.json"), data)
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

// onJobEvent 轉送工作的事件；工作結束時這首歌的狀態也要更新。
func (a *App) onJobEvent(kind string, data any) {
	a.Events.Publish(kind, data)
	if s, ok := data.(jobs.Summary); ok && kind == "job" && s.Song != "" {
		switch s.Status {
		case jobs.Done, jobs.Failed, jobs.Cancelled, jobs.Queued:
			a.invalidate(s.Song)
		case jobs.Running:
			if s.Stage == "" {
				a.invalidate(s.Song) // 剛開始：曲庫畫面要顯示處理中
			}
		}
	}
}
