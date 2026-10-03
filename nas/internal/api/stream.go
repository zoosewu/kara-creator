package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	kara "github.com/zoosewu/kara-creator"
	"github.com/zoosewu/kara-creator/nas/internal/app"
)

// events 是 GET /api/v1/events（Server-Sent Events）：song、library、job、job.log、worker、readings、settings。
// 連線時先送 hello；每 25 秒送一行註解保持連線。斷線重連後前端重抓 /library。
func events(a *app.App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "不支援串流", http.StatusInternalServerError)
			return
		}
		ch, cancel := a.Events.Subscribe()
		defer cancel()
		h := w.Header()
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Accel-Buffering", "no")
		send := func(kind string, data any) bool {
			raw, err := json.Marshal(data)
			if err != nil {
				return true
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", kind, raw); err != nil {
				return false
			}
			flusher.Flush()
			return true
		}
		if !send("hello", map[string]any{"versions": kara.Current}) {
			return
		}
		ping := time.NewTicker(25 * time.Second)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case e, ok := <-ch:
				if !ok {
					return // 跟不上被斷掉：前端會重連
				}
				if !send(e.Kind, e.Data) {
					return
				}
			case <-ping.C:
				if _, err := fmt.Fprint(w, ": ping\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

// mediaFile 是 GET /media/{id}/{file}：songs/<id>/ 裡的媒體檔（支援 Range，給 <video> 播放與拖曳）。
func mediaFile(a *app.App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id, name := r.PathValue("id"), r.PathValue("file")
		if _, ok := a.Store.Song(id); !ok || name != filepath.Base(name) || name == "" || name[0] == '.' {
			http.NotFound(w, r)
			return
		}
		serve(w, r, a.Store.SongPath(id, name), false)
	}
}

// fontFile 是 GET /fonts/{sha256}：字型檔（前端用 @font-face 預覽成品的字型）。
func fontFile(a *app.App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		path, ok := a.Fonts.Path(r.PathValue("sha"))
		if !ok {
			http.NotFound(w, r)
			return
		}
		serve(w, r, path, true)
	}
}

func serve(w http.ResponseWriter, r *http.Request, path string, immutable bool) {
	f, err := os.Open(path)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		http.NotFound(w, r)
		return
	}
	if immutable {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable") // 網址就是內容的雜湊
	}
	http.ServeContent(w, r, filepath.Base(path), st.ModTime(), f)
}
