// Package api 是 NAS 伺服器的 HTTP 介面：REST API（/api/v1，網頁前端與外部服務共用）、SSE 事件、
// 媒體檔、字型、worker 協定（/worker/v1）、內嵌的前端。
//
// REST API 用 huma 定義，OpenAPI 規格從程式碼產生（kara-nas openapi > docs/openapi.json）。
// 錯誤一律是 RFC 9457 problem+json，detail 是給人看的繁中說明。
package api

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"path"

	"github.com/danielgtaylor/huma/v2"
	"github.com/danielgtaylor/huma/v2/adapters/humago"

	kara "github.com/zoosewu/kara-creator"
	"github.com/zoosewu/kara-creator/nas/internal/app"
	"github.com/zoosewu/kara-creator/nas/internal/library"
)

// dist 是 nas/web 打包後的前端（npm run build）。還沒打包時只有 .gitkeep。
//
//go:embed all:dist
var dist embed.FS

// Prefix 是 REST API 的路徑前綴。
const Prefix = "/api/v1"

func init() {
	// 回傳的清單一律是陣列（沒有資料時是 []，不是 null），前端的型別才不必到處處理 null。
	huma.DefaultArrayNullable = false
	// huma 內建的訊息是英文：detail 換成中文，細節（哪個欄位不對）留在 errors 裡。
	orig := huma.NewError
	huma.NewError = func(status int, msg string, errs ...error) huma.StatusError {
		switch msg {
		case "validation failed":
			msg = "送來的內容格式不對"
		case "request body is required":
			msg = "缺少內容"
		}
		return orig(status, msg, errs...)
	}
}

func apiConfig() huma.Config {
	cfg := huma.DefaultConfig("伴唱帶工作室 API", "2")
	cfg.Info.Description = "NAS 伺服器的 REST API。網頁前端和外部服務共用。一首歌一個 id；GET 讀、POST 建立、PATCH 部分更新、" +
		"PUT 取代、DELETE 刪除或取消；花時間的處理都是工作（回 202，Location 指向工作）。即時更新用 GET /api/v1/events（SSE）。"
	cfg.OpenAPIPath = Prefix + "/openapi"
	cfg.DocsPath = Prefix + "/docs"
	cfg.SchemasPath = Prefix + "/schemas"
	return cfg
}

// Handler 回傳整個網站的 handler。
func Handler(a *app.App) http.Handler {
	mux := http.NewServeMux()
	api := humago.New(mux, apiConfig())
	register(api, a)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "versions": kara.Current})
	})
	if a != nil {
		mux.HandleFunc("GET "+Prefix+"/events", events(a))
		mux.HandleFunc("GET /media/{id}/{file}", mediaFile(a))
		mux.HandleFunc("GET /fonts/{sha}", fontFile(a))
		for _, method := range []string{"GET", "POST", "PUT"} { // 要寫方法，才不會和 "GET /" 衝突
			mux.Handle(method+" /worker/v1/", a.WorkerHandler())
		}
	}
	web, _ := fs.Sub(dist, "dist")
	mux.Handle("GET /", spa(web))
	return mux
}

// OpenAPI 回傳 OpenAPI 規格（JSON）。
func OpenAPI() ([]byte, error) {
	api := humago.New(http.NewServeMux(), apiConfig())
	register(api, nil)
	return json.MarshalIndent(api.OpenAPI(), "", "  ")
}

// fail 把 app 的錯誤換成 HTTP 錯誤。
func fail(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, app.ErrNotFound):
		return huma.Error404NotFound(err.Error())
	case errors.Is(err, app.ErrInvalid):
		return huma.Error400BadRequest(err.Error())
	case errors.Is(err, app.ErrConflict), errors.Is(err, app.ErrReadingsPending):
		return huma.Error409Conflict(err.Error())
	case errors.Is(err, library.ErrNotFound):
		return huma.Error404NotFound(library.Message(err))
	}
	return huma.Error500InternalServerError(err.Error())
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
