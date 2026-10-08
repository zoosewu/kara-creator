package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	kara "github.com/zoosewu/kara-creator"
	"github.com/zoosewu/kara-creator/nas/internal/app"
	"github.com/zoosewu/kara-creator/nas/internal/config"
	"github.com/zoosewu/kara-creator/nas/internal/fakeworker"
	"github.com/zoosewu/kara-creator/nas/internal/media"
)

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler(nil).ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	var body struct {
		OK       bool          `json:"ok"`
		Versions kara.Versions `json:"versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || !body.OK || body.Versions != kara.Current {
		t.Fatalf("%s %v", rec.Body, err)
	}
}

func TestOpenAPI(t *testing.T) {
	data, err := OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]any `json:"paths"`
	}
	if err := json.Unmarshal(data, &spec); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"/api/v1/library", "/api/v1/songs/{id}/lyrics", "/api/v1/jobs", "/api/v1/songs/{id}/timing/lines/{n}"} {
		if _, ok := spec.Paths[p]; !ok {
			t.Errorf("規格裡沒有 %s", p)
		}
	}
	// docs/openapi.json 要和程式碼一致（改了 API 要執行 go run ./nas/cmd/kara-nas openapi > docs/openapi.json）
	saved, err := os.ReadFile(filepath.Join("..", "..", "..", "docs", "openapi.json"))
	if err != nil || !bytes.Equal(bytes.TrimSpace(saved), bytes.TrimSpace(data)) {
		t.Error("docs/openapi.json 過期了：執行 go run ./nas/cmd/kara-nas openapi > docs/openapi.json")
	}
}

// 假的 yt-dlp：-J 印出資訊；下載時把 $FAKE_MEDIA 複製到 -o 的位置；--version、-U 只印版本。
const fakeYTDLP = `#!/bin/sh
mode=download; out=""; prev=""
for a in "$@"; do
  case "$a" in -J) mode=info;; --version|-U) echo 2026.10.01; exit 0;; esac
  [ "$prev" = "-o" ] && out="$a"
  prev="$a"
done
if [ "$mode" = info ]; then
  echo '{"id":"dQw4w9WgXcQ","extractor_key":"Youtube","webpage_url":"https://www.youtube.com/watch?v=dQw4w9WgXcQ","title":"虛構歌手『自己編的歌』","channel":"虛構歌手","duration":3}'
  exit 0
fi
cp "$FAKE_MEDIA" "${out%%.%(ext)s}.mp4"
`

type client struct {
	t   *testing.T
	url string
}

func (c client) do(method, path string, body any, out any) int {
	c.t.Helper()
	var r io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		r = bytes.NewReader(data)
	}
	req, _ := http.NewRequest(method, c.url+path, r)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if out != nil {
		if err := json.Unmarshal(data, out); err != nil {
			c.t.Fatalf("%s %s：%v\n%s", method, path, err, data)
		}
	}
	if resp.StatusCode >= 400 && out == nil {
		c.t.Logf("%s %s → %d %s", method, path, resp.StatusCode, data)
	}
	return resp.StatusCode
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("等不到：" + what)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// 階段 1 的完成條件：用假的 worker 跑完「下載 → 去人聲 → 對時 → 燒錄 → 檢查」整條流程。
func TestEndToEnd(t *testing.T) {
	tools := media.DefaultTools("")
	if _, err := exec.LookPath(tools.FFmpeg); err != nil {
		t.Skip("沒有 ffmpeg")
	}
	dir := t.TempDir()
	toolsDir := filepath.Join(dir, "tools")
	_ = os.MkdirAll(toolsDir, 0o755)
	if err := os.WriteFile(filepath.Join(toolsDir, "yt-dlp"), []byte(fakeYTDLP), 0o755); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(dir, "fixture.mp4")
	if out, err := exec.Command(tools.FFmpeg, "-y", "-v", "error", "-f", "lavfi", "-i", "testsrc=size=320x240:rate=10:duration=3",
		"-f", "lavfi", "-i", "sine=duration=3", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", video).CombinedOutput(); err != nil {
		t.Fatalf("%v %s", err, out)
	}
	t.Setenv("FAKE_MEDIA", video)
	// 字型：開發環境的系統字型（沒有的話燒錄會因為找不到字型而失敗）
	lib := filepath.Join(dir, "library")
	cfg := config.Config{Library: lib, Data: filepath.Join(dir, "data"), Tools: toolsDir, Init: true}
	_ = os.MkdirAll(filepath.Join(lib, "fonts"), 0o755)
	if _, err := os.Stat("/usr/share/fonts/opentype/noto/NotoSansCJK-Bold.ttc"); err != nil {
		t.Skip("沒有 Noto Sans CJK 字型")
	}
	_ = os.Symlink("/usr/share/fonts/opentype/noto/NotoSansCJK-Bold.ttc", filepath.Join(lib, "fonts", "NotoSansCJK-Bold.ttc"))

	a, err := app.New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a.ChangeDelay = 100 * time.Millisecond
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Run(ctx)
	srv := httptest.NewServer(Handler(a))
	defer srv.Close()
	defer a.Shutdown()
	c := client{t, srv.URL}

	// SSE：收集事件
	got := make(chan string, 1000)
	sctx, sseCancel := context.WithCancel(context.Background())
	defer sseCancel() // 先斷開 SSE，srv.Close 才不會一直等這條長連線
	go func() {
		req, _ := http.NewRequestWithContext(sctx, "GET", srv.URL+"/api/v1/events", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return
		}
		defer resp.Body.Close()
		sc := bufio.NewScanner(resp.Body)
		for sc.Scan() {
			if kind, ok := strings.CutPrefix(sc.Text(), "event: "); ok {
				got <- kind
			}
		}
	}()

	wctx, wcancel := context.WithCancel(context.Background())
	defer wcancel()
	go func() { _ = fakeworker.Run(wctx, fakeworker.Options{NAS: srv.URL, Name: "fake", Logf: t.Logf}) }()

	// 1. 新資料夾、貼網址一路做到伴唱帶，附上歌詞
	var folder app.FolderView
	if code := c.do("POST", "/api/v1/folders", map[string]any{"name": "日文"}, &folder); code != 201 {
		t.Fatal(code)
	}
	var job struct{ ID string }
	if code := c.do("POST", "/api/v1/songs", map[string]any{"url": "https://youtu.be/dQw4w9WgXcQ", "folder": folder.ID, "make": true,
		"lyrics": "[男] 自己編的第一句\n> 翻譯\n[女] 第二句\n"}, &job); code != 202 {
		t.Fatal(code)
	}
	// YouTube 的網址：按下去的當下歌詞就存進曲庫了（還沒開始下載）
	if data, err := os.ReadFile(filepath.Join(lib, "songs", "dQw4w9WgXcQ", "lyrics.txt")); err != nil || !strings.Contains(string(data), "自己編的第一句") {
		t.Fatalf("歌詞要先存起來：%q %v", data, err)
	}
	var js struct {
		Status string
		Error  string
	}
	waitFor(t, "工作完成", func() bool {
		c.do("GET", "/api/v1/jobs/"+job.ID, nil, &js)
		return js.Status == "done" || js.Status == "failed"
	})
	if js.Status != "done" {
		var log struct{ Lines []string }
		c.do("GET", "/api/v1/jobs/"+job.ID+"/log", nil, &log)
		t.Fatalf("工作失敗：%s\n%s", js.Error, strings.Join(log.Lines, "\n"))
	}

	// 2. 狀態全部完成、可以播放、已匯出
	var song app.SongView
	waitFor(t, "狀態更新", func() bool {
		c.do("GET", "/api/v1/songs/dQw4w9WgXcQ", nil, &song)
		return song.Status.Karaoke == "done" && song.Job == nil
	})
	if song.Title != "自己編的歌" || song.Status.Separate != "done" || song.Status.QA == nil || song.Folder != folder.ID ||
		len(song.Media) == 0 || song.Media[0].Kind != "karaoke" {
		t.Fatalf("%+v", song)
	}
	var library LibraryOut
	c.do("GET", "/api/v1/library", nil, &library)
	if len(library.Songs) != 1 || library.Songs[0].Export != "日文/虛構歌手 - 自己編的歌.mp4" || len(library.Workers) != 1 {
		t.Fatalf("%+v", library)
	}
	if _, err := os.Stat(filepath.Join(lib, "export", "日文", "虛構歌手 - 自己編的歌.mp4")); err == nil {
		t.Fatal("沒確認、沒按匯出之前不該匯出")
	}
	resp, err := http.Get(srv.URL + song.Media[0].URL)
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Accept-Ranges") != "bytes" {
		t.Fatalf("媒體檔要能播放（支援 Range）：%v %v", resp, err)
	}
	resp.Body.Close()

	// 3. 歌詞、時間、檢查
	var views app.LyricsViews
	c.do("GET", "/api/v1/songs/dQw4w9WgXcQ/lyrics", nil, &views)
	if !views.Exists || !strings.Contains(views.Plain, "> 翻譯") || len(views.Doc.Lines) != 2 {
		t.Fatalf("%+v", views)
	}
	var timing app.TimingView
	if code := c.do("PATCH", "/api/v1/songs/dQw4w9WgXcQ/timing/lines/1", map[string]any{"delta": 0.2}, &timing); code != 200 {
		t.Fatal(code)
	}
	if len(timing.Lines) != 2 || len(timing.Adjustments) != 1 {
		t.Fatalf("%+v", timing)
	}
	waitFor(t, "調整後需更新", func() bool {
		c.do("GET", "/api/v1/songs/dQw4w9WgXcQ", nil, &song)
		return song.Status.Karaoke == "needs_render"
	})
	var qa app.QAView
	c.do("GET", "/api/v1/songs/dQw4w9WgXcQ/qa", nil, &qa)
	if qa.Checked {
		t.Fatal("對時改了，舊的檢查結果不算")
	}

	// 4. 批次：只做需要的 → 重燒；已確認
	var res app.JobResult
	if code := c.do("POST", "/api/v1/jobs", map[string]any{"songs": []string{"dQw4w9WgXcQ"}, "steps": []string{"karaoke"}, "only_needed": true}, &res); code != 202 || len(res.Created) != 1 {
		t.Fatalf("%d %+v", code, res)
	}
	waitFor(t, "重燒完成", func() bool {
		c.do("GET", "/api/v1/songs/dQw4w9WgXcQ", nil, &song)
		return song.Status.Karaoke == "done" && song.Job == nil
	})
	c.do("POST", "/api/v1/jobs", map[string]any{"songs": []string{"dQw4w9WgXcQ"}, "steps": []string{"karaoke"}, "only_needed": true}, &res)
	if len(res.Created) != 0 || res.Skipped["已完成"] != 1 {
		t.Fatalf("已完成的要略過：%+v", res)
	}
	if code := c.do("PUT", "/api/v1/songs/dQw4w9WgXcQ/approval", map[string]any{"approved": true}, &song); code != 200 || song.Status.Approval != "approved" {
		t.Fatalf("%d %+v", code, song.Status)
	}

	// 4b. 匯出：只匯出已確認的，而且要按「匯出」
	exported := filepath.Join(lib, "export", "日文", "虛構歌手 - 自己編的歌.mp4")
	c.do("GET", "/api/v1/library", nil, &library)
	if library.Songs[0].Exported != "pending" {
		t.Fatalf("已確認、還沒匯出：%q", library.Songs[0].Exported)
	}
	var exp struct {
		Added, Removed, Skipped []string
		Kept                    int
		Detail                  string
	}
	if code := c.do("POST", "/api/v1/export", nil, &exp); code != 200 || len(exp.Added) != 1 {
		t.Fatalf("%d %+v", code, exp)
	}
	if _, err := os.Stat(exported); err != nil {
		t.Fatal("按了匯出應該要有檔案")
	}
	c.do("GET", "/api/v1/songs/dQw4w9WgXcQ", nil, &song)
	if song.Exported != "exported" {
		t.Fatalf("%q", song.Exported)
	}
	// 改名：匯出檔名跟著變，要重新匯出（舊檔拿掉、新檔放上）；改回自動再匯出一次
	renamed := filepath.Join(lib, "export", "日文", "虛構歌手 - 改過的歌名.mp4")
	for _, step := range []struct{ title, file, gone string }{{"改過的歌名", renamed, exported}, {"", exported, renamed}} {
		c.do("PATCH", "/api/v1/songs/dQw4w9WgXcQ", map[string]any{"title": step.title}, &song)
		waitFor(t, "改名後待匯出", func() bool {
			c.do("GET", "/api/v1/songs/dQw4w9WgXcQ", nil, &song)
			return song.Exported == "pending"
		})
		if code := c.do("POST", "/api/v1/export", nil, &exp); code != 200 || len(exp.Added) != 1 || len(exp.Removed) != 1 {
			t.Fatalf("%d %+v", code, exp)
		}
		if _, err := os.Stat(step.file); err != nil {
			t.Fatalf("改名後匯出：%v", err)
		}
		if _, err := os.Stat(step.gone); err == nil {
			t.Fatal("舊檔名要拿掉")
		}
	}

	// 4b-2. 原曲音訊：設定打開後變成待匯出；匯出時產生 m4a（AAC 直接複製）放在伴唱帶旁邊；關掉後再匯出就拿掉
	original := filepath.Join(lib, "export", "日文", "虛構歌手 - 自己編的歌_original.m4a")
	for _, on := range []bool{true, false} {
		if code := c.do("PATCH", "/api/v1/settings", map[string]any{"export_original": on}, nil); code != 200 {
			t.Fatal(code)
		}
		waitFor(t, "設定改了就待匯出", func() bool {
			c.do("GET", "/api/v1/songs/dQw4w9WgXcQ", nil, &song)
			return song.Exported == "pending"
		})
		if code := c.do("POST", "/api/v1/export", nil, &exp); code != 200 {
			t.Fatalf("%d %+v", code, exp)
		}
		_, err := os.Stat(original)
		if on != (err == nil) {
			t.Fatalf("原曲音訊（設定 %v）：%v %+v", on, err, exp)
		}
		if on {
			info, err := tools.Probe(context.Background(), original)
			if err != nil || info.Audio != "aac" || info.HasVideo {
				t.Fatalf("原曲音訊要是只有 AAC 音軌的 m4a：%+v %v", info, err)
			}
		}
		c.do("GET", "/api/v1/songs/dQw4w9WgXcQ", nil, &song)
		if song.Exported != "exported" {
			t.Fatalf("匯出之後（設定 %v）：%q", on, song.Exported)
		}
	}

	// 4c. 只需重燒的更新（手動調時間）：確認保留
	if code := c.do("PATCH", "/api/v1/songs/dQw4w9WgXcQ/timing/lines/0", map[string]any{"delta": 0.1}, &timing); code != 200 {
		t.Fatal(code)
	}
	waitFor(t, "只需重燒", func() bool {
		c.do("GET", "/api/v1/songs/dQw4w9WgXcQ", nil, &song)
		return song.Status.Karaoke == "needs_render"
	})
	if song.Approval.Status != "approved" {
		t.Fatalf("只需重燒時確認要保留：%+v", song.Approval)
	}

	// 4d. 改歌詞（需重新對時）：確認失效，紀錄留著，標出改了哪一句
	c.do("PUT", "/api/v1/songs/dQw4w9WgXcQ/lyrics", map[string]any{"text": "[男] 自己編的第一句\n> 翻譯\n[女] 第二句改了\n"}, &views)
	waitFor(t, "需重新對時", func() bool {
		c.do("GET", "/api/v1/songs/dQw4w9WgXcQ", nil, &song)
		return song.Status.Karaoke == "needs_align"
	})
	if song.Approval.Status != "stale" || song.Approval.Count != 1 || len(song.Approval.Changed) != 1 || song.Approval.Changed[0] != 1 {
		t.Fatalf("%+v", song.Approval)
	}
	// 取消確認後再按匯出：匯出檔拿掉
	c.do("PUT", "/api/v1/songs/dQw4w9WgXcQ/approval", map[string]any{"approved": false}, &song)
	waitFor(t, "待移除", func() bool {
		c.do("GET", "/api/v1/songs/dQw4w9WgXcQ", nil, &song)
		return song.Exported == "remove"
	})
	c.do("POST", "/api/v1/export", nil, &exp)
	if _, err := os.Stat(exported); err == nil {
		t.Fatal("沒有確認的歌，匯出時要拿掉")
	}
	c.do("GET", "/api/v1/songs/dQw4w9WgXcQ", nil, &song)
	if song.Exported != "" {
		t.Fatalf("拿掉之後不再顯示：%q", song.Exported)
	}

	// 5. 錯誤：detail 是繁中
	var problem struct{ Detail string }
	if code := c.do("GET", "/api/v1/songs/不存在", nil, &problem); code != 404 || problem.Detail != "找不到這首歌" {
		t.Fatalf("%d %+v", code, problem)
	}
	if code := c.do("PATCH", "/api/v1/songs/dQw4w9WgXcQ", map[string]any{"language": "xx"}, &problem); code != 400 || problem.Detail != "不支援的語言：xx" {
		t.Fatalf("%d %+v", code, problem)
	}

	// 6. 收尾：資料備份寫出來了
	waitFor(t, "資料備份", func() bool {
		_, err := os.Stat(filepath.Join(dir, "data", "songs.json"))
		return err == nil
	})

	// 7. SSE 有送事件
	kinds := map[string]bool{}
	for len(got) > 0 {
		kinds[<-got] = true
	}
	for _, k := range []string{"hello", "job", "job.log", "song", "worker"} {
		if !kinds[k] {
			t.Errorf("SSE 應該送 %s 事件：%v", k, kinds)
		}
	}
}
