// Package api 是 NAS 伺服器對瀏覽器的 HTTP 介面：REST API、SSE、媒體檔、內嵌的前端。
package api

import (
	"embed"
	"encoding/json"
	"io/fs"
	"net/http"
	"path"

	kara "github.com/zoosewu/kara-creator"
)

// dist 是 nas/web 打包後的前端（npm run build）。還沒打包時只有 .gitkeep。
//
//go:embed all:dist
var dist embed.FS

// Handler 回傳整個網站的 handler。
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "versions": kara.Current})
	})
	web, _ := fs.Sub(dist, "dist")
	mux.Handle("GET /", spa(web))
	return mux
}

// spa 送出前端的檔案；找不到的路徑一律回 index.html（前端自己處理路由）。
func spa(web fs.FS) http.Handler {
	files := http.FileServerFS(web)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := path.Clean(r.URL.Path)[1:]
		if name == "" {
			name = "index.html"
		}
		if _, err := fs.Stat(web, name); err != nil {
			if _, err := fs.Stat(web, "index.html"); err != nil {
				http.Error(w, "前端還沒打包：請在 nas/web 執行 npm run build", http.StatusNotFound)
				return
			}
			r = r.Clone(r.Context())
			r.URL.Path = "/"
		}
		files.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
