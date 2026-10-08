package api

import (
	"context"
	"net/http"

	"github.com/danielgtaylor/huma/v2"

	"github.com/zoosewu/kara-creator/nas/internal/app"
	"github.com/zoosewu/kara-creator/nas/internal/export"
	"github.com/zoosewu/kara-creator/nas/internal/fonts"
	"github.com/zoosewu/kara-creator/nas/internal/jobs"
	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/readings"
	"github.com/zoosewu/kara-creator/nas/internal/scheduler"
	"github.com/zoosewu/kara-creator/nas/internal/song"
)

// ---- 輸入輸出 ------------------------------------------------------------------

type songID struct {
	ID string `path:"id" doc:"歌曲 id"`
}

type body[T any] struct {
	Body T
}

type accepted[T any] struct {
	Location string `header:"Location" doc:"工作的網址"`
	Body     T
}

// LibraryOut 是前端初次載入的資料。
type LibraryOut struct {
	app.Library
	Workers []scheduler.Worker `json:"workers"`
	Jobs    []jobs.Summary     `json:"jobs"`
}

type approvalIn struct {
	ID   string `path:"id" doc:"歌曲 id"`
	Body struct {
		Approved bool `json:"approved" doc:"true = 標記已確認；false = 取消確認"`
	}
}

type lyricsPut struct {
	ID   string `path:"id" doc:"歌曲 id"`
	Body struct {
		Text string `json:"text" doc:"存檔格式的歌詞；空字串 = 刪除歌詞"`
	}
}

type lineIn struct {
	ID   string `path:"id" doc:"歌曲 id"`
	N    int    `path:"n" minimum:"0" doc:"第幾句（要唱的句子，從 0 起算）"`
	Body app.LineEdit
}

type shiftIn struct {
	ID   string `path:"id" doc:"歌曲 id"`
	N    int    `path:"n" minimum:"0" doc:"第幾句（從 0 起算，與對時結果一致）"`
	Body app.ShiftRequest
}

type folderIn struct {
	Body struct {
		Name   string `json:"name,omitempty" doc:"名稱；空的話叫「新資料夾」"`
		Parent string `json:"parent,omitempty" doc:"上一層資料夾 id；空字串 = 最上層"`
		Order  int    `json:"order,omitempty" doc:"順序；0 = 排到最後"`
	}
}

type folderPatch struct {
	ID   string `path:"id" doc:"資料夾 id"`
	Body struct {
		Name   *string `json:"name,omitempty" doc:"新名稱"`
		Order  *int    `json:"order,omitempty" doc:"新順序（正整數）"`
		Parent *string `json:"parent,omitempty" doc:"移到哪個資料夾底下；空字串 = 最上層"`
	}
}

type folderID struct {
	ID string `path:"id" doc:"資料夾 id"`
}

type jobID struct {
	ID string `path:"id" doc:"工作 id"`
}

type logIn struct {
	ID     string `path:"id" doc:"工作 id"`
	Offset int    `query:"offset" minimum:"0" doc:"從第幾行開始（上次回應的 next）"`
}

// LogOut 是工作的紀錄。
type LogOut struct {
	Lines []string     `json:"lines"`
	Next  int          `json:"next" doc:"下次從這一行開始取"`
	Job   jobs.Summary `json:"job"`
}

type workerPatch struct {
	Name string `path:"name" doc:"AI 伺服器名稱"`
	Body struct {
		Disabled bool `json:"disabled" doc:"true = 停用（領不到新任務）"`
	}
}

// Empty 是沒有內容的回應。
type Empty struct{}

// ---- 註冊 ----------------------------------------------------------------------

func op(id, method, p, summary, desc string, tags ...string) huma.Operation {
	return huma.Operation{OperationID: id, Method: method, Path: Prefix + p, Summary: summary, Description: desc, Tags: tags}
}

func register(api huma.API, a *app.App) {
	const (
		tSongs   = "歌曲"
		tLyrics  = "歌詞與時間"
		tLibrary = "曲庫"
		tJobs    = "工作"
		tSystem  = "系統"
	)

	huma.Register(api, op("get-library", http.MethodGet, "/library", "整個曲庫",
		"前端初次載入：資料夾、所有歌的摘要（含各階段狀態、疑慮數、目前的工作）、設定、AI 伺服器、工作。", tLibrary),
		func(ctx context.Context, _ *struct{}) (*body[LibraryOut], error) {
			return &body[LibraryOut]{LibraryOut{Library: a.Library(), Workers: a.Sched.Workers(), Jobs: a.Jobs.List()}}, nil
		})

	huma.Register(api, op("list-songs", http.MethodGet, "/songs", "所有歌的摘要", "", tSongs),
		func(ctx context.Context, _ *struct{}) (*body[[]*app.SongView], error) {
			return &body[[]*app.SongView]{a.Library().Songs}, nil
		})

	huma.Register(api, op("get-song", http.MethodGet, "/songs/{id}", "一首歌的摘要", "", tSongs),
		func(ctx context.Context, in *songID) (*body[*app.SongView], error) {
			v, err := a.Song(in.ID)
			if err != nil {
				return nil, fail(err)
			}
			return &body[*app.SongView]{v}, nil
		})

	add := op("add-song", http.MethodPost, "/songs", "新增歌曲（貼網址）",
		"建立下載工作，回 202；Location 指向工作。make=true 時一路做到伴唱帶（有歌詞才製作）。", tSongs)
	add.DefaultStatus = http.StatusAccepted
	huma.Register(api, add, func(ctx context.Context, in *body[app.AddRequest]) (*accepted[jobs.Summary], error) {
		s, err := a.AddSong(in.Body)
		if err != nil {
			return nil, fail(err)
		}
		return &accepted[jobs.Summary]{Location: Prefix + "/jobs/" + s.ID, Body: s}, nil
	})

	huma.Register(api, op("update-song", http.MethodPatch, "/songs/{id}", "修改歌曲資訊",
		"只改有給的欄位。歌名、演唱者給空字串 = 回到自動辨識；語言給空字串 = 依歌詞判斷。", tSongs),
		func(ctx context.Context, in *struct {
			ID   string `path:"id" doc:"歌曲 id"`
			Body song.InfoUpdate
		}) (*body[*app.SongView], error) {
			v, err := a.UpdateInfo(in.ID, in.Body)
			if err != nil {
				return nil, fail(err)
			}
			return &body[*app.SongView]{v}, nil
		})

	huma.Register(api, op("set-approval", http.MethodPut, "/songs/{id}/approval", "已確認 / 取消確認",
		"只能確認已經做好、而且是最新的伴唱帶。之後對時、歌詞、演唱者、翻譯或標題畫面改了會變成需重新確認（換字型、改字幕大小不影響）。", tSongs),
		func(ctx context.Context, in *approvalIn) (*body[*app.SongView], error) {
			v, err := a.SetApproval(in.ID, in.Body.Approved)
			if err != nil {
				return nil, fail(err)
			}
			return &body[*app.SongView]{v}, nil
		})

	huma.Register(api, op("get-lyrics", http.MethodGet, "/songs/{id}/lyrics", "歌詞的各種檢視",
		"結構（含假名片段）、存檔格式、標註原文、原始歌詞。日文的自動假名由 AI 伺服器算；還沒算好的句子列在 pending_readings，"+
			"算好後送 readings 事件。", tLyrics),
		func(ctx context.Context, in *songID) (*body[app.LyricsViews], error) {
			v, err := a.Lyrics(in.ID)
			if err != nil {
				return nil, fail(err)
			}
			return &body[app.LyricsViews]{v}, nil
		})

	huma.Register(api, op("put-lyrics", http.MethodPut, "/songs/{id}/lyrics", "儲存歌詞", "改了歌詞文字或讀音：製作伴唱帶時只重對改過的句子，其他句子的時間（含手動調整）不動；人聲、語言或對時方法也改了時才整首重新對時。", tLyrics),
		func(ctx context.Context, in *lyricsPut) (*body[app.LyricsViews], error) {
			v, err := a.SaveLyrics(in.ID, in.Body.Text)
			if err != nil {
				return nil, fail(err)
			}
			return &body[app.LyricsViews]{v}, nil
		})

	huma.Register(api, op("convert-lyrics", http.MethodPost, "/lyrics/convert", "歌詞文字 ↔ 結構互轉",
		"只給 doc：從結構產生各種檢視；給 text：依 format 解析（plain 要一起給原本的 doc 才能保留演唱者與讀音）。"+
			"標註原文需要自動讀音才能判斷哪些是手動的：AI 不在線時回 409。", tLyrics),
		func(ctx context.Context, in *body[app.ConvertRequest]) (*body[readings.Views], error) {
			v, err := a.Convert(in.Body)
			if err != nil {
				return nil, fail(err)
			}
			return &body[readings.Views]{v}, nil
		})

	huma.Register(api, op("get-timing", http.MethodGet, "/songs/{id}/timing", "每句目前的時間", "還沒對時時 lines 為空。", tLyrics),
		func(ctx context.Context, in *songID) (*body[app.TimingView], error) {
			v, err := a.Timing(in.ID)
			if err != nil {
				return nil, fail(err)
			}
			return &body[app.TimingView]{v}, nil
		})

	huma.Register(api, op("shift-line", http.MethodPatch, "/songs/{id}/timing/lines/{n}", "移動一句",
		"只移這一句：往後撞到下一句時把它往後推、往前撞到上一句時把它往前推（連鎖），唱不完的部分壓縮。"+
			"回傳的 pushed 是一起被推動的句子。之後伴唱帶顯示需更新（只重燒，不重新對時）。", tLyrics),
		func(ctx context.Context, in *shiftIn) (*body[app.TimingView], error) {
			v, err := a.Shift(in.ID, in.N, in.Body)
			if err != nil {
				return nil, fail(err)
			}
			return &body[app.TimingView]{v}, nil
		})

	huma.Register(api, op("edit-line", http.MethodPatch, "/songs/{id}/lines/{n}", "改一句的文字或演唱者",
		"立刻存檔。改了文字之後，製作伴唱帶時只重對這一句（手動調過的保留開頭）。", tLyrics),
		func(ctx context.Context, in *lineIn) (*body[app.LyricsViews], error) {
			v, err := a.EditLine(in.ID, in.N, in.Body)
			if err != nil {
				return nil, fail(err)
			}
			return &body[app.LyricsViews]{v}, nil
		})

	huma.Register(api, op("get-qa", http.MethodGet, "/songs/{id}/qa", "對時檢查結果", "只列有疑慮的句子；對時改過之後 checked 為 false。", tLyrics),
		func(ctx context.Context, in *songID) (*body[app.QAView], error) {
			v, err := a.QA(in.ID)
			if err != nil {
				return nil, fail(err)
			}
			return &body[app.QAView]{v}, nil
		})

	huma.Register(api, op("list-folders", http.MethodGet, "/folders", "所有資料夾", "", tLibrary),
		func(ctx context.Context, _ *struct{}) (*body[[]app.FolderView], error) {
			return &body[[]app.FolderView]{a.Library().Folders}, nil
		})

	newFolder := op("add-folder", http.MethodPost, "/folders", "新增資料夾", "", tLibrary)
	newFolder.DefaultStatus = http.StatusCreated
	huma.Register(api, newFolder, func(ctx context.Context, in *folderIn) (*body[app.FolderView], error) {
		f, err := a.AddFolder(in.Body.Name, in.Body.Parent, in.Body.Order)
		if err != nil {
			return nil, fail(err)
		}
		return &body[app.FolderView]{f}, nil
	})

	huma.Register(api, op("update-folder", http.MethodPatch, "/folders/{id}", "改名、改順序或移動資料夾", "", tLibrary),
		func(ctx context.Context, in *folderPatch) (*body[app.FolderView], error) {
			f, err := a.UpdateFolder(in.ID, library.FolderUpdate{Name: in.Body.Name, Order: in.Body.Order, Parent: in.Body.Parent})
			if err != nil {
				return nil, fail(err)
			}
			return &body[app.FolderView]{f}, nil
		})

	huma.Register(api, op("delete-folder", http.MethodDelete, "/folders/{id}", "刪除資料夾",
		"裡面的子資料夾與歌移到上一層，不刪任何檔案。", tLibrary),
		func(ctx context.Context, in *folderID) (*struct{}, error) {
			return nil, fail(a.DeleteFolder(in.ID))
		})

	huma.Register(api, op("place", http.MethodPost, "/library/place", "拖曳：移動與排序",
		"把歌或資料夾放進某個資料夾、排在某個項目前面。只做最少的編號調整（留的空號不動）。", tLibrary),
		func(ctx context.Context, in *body[app.PlaceRequest]) (*struct{}, error) {
			return nil, fail(a.Place(in.Body))
		})

	submit := op("submit-jobs", http.MethodPost, "/jobs", "建立工作",
		"每首歌一件工作。only_needed 時略過不需要做的歌（已完成、缺歌詞、處理中），回傳略過的原因與首數。", tJobs)
	submit.DefaultStatus = http.StatusAccepted
	huma.Register(api, submit, func(ctx context.Context, in *body[app.JobRequest]) (*body[app.JobResult], error) {
		res, err := a.SubmitJobs(in.Body)
		if err != nil {
			return nil, fail(err)
		}
		return &body[app.JobResult]{res}, nil
	})

	huma.Register(api, op("list-jobs", http.MethodGet, "/jobs", "所有工作", "新的在前；結束的工作保留最近 200 件、7 天。", tJobs),
		func(ctx context.Context, _ *struct{}) (*body[[]jobs.Summary], error) {
			return &body[[]jobs.Summary]{a.Jobs.List()}, nil
		})

	huma.Register(api, op("get-job", http.MethodGet, "/jobs/{id}", "一件工作", "", tJobs),
		func(ctx context.Context, in *jobID) (*body[jobs.Summary], error) {
			s, ok := a.Jobs.Get(in.ID)
			if !ok {
				return nil, huma.Error404NotFound("找不到工作")
			}
			return &body[jobs.Summary]{s}, nil
		})

	huma.Register(api, op("get-job-log", http.MethodGet, "/jobs/{id}/log", "工作的紀錄", "即時更新請用 SSE 的 job.log 事件。", tJobs),
		func(ctx context.Context, in *logIn) (*body[LogOut], error) {
			lines, ok := a.Jobs.Logs(in.ID, in.Offset)
			s, _ := a.Jobs.Get(in.ID)
			if !ok {
				return nil, huma.Error404NotFound("找不到工作")
			}
			return &body[LogOut]{LogOut{Lines: lines, Next: in.Offset + len(lines), Job: s}}, nil
		})

	huma.Register(api, op("cancel-job", http.MethodDelete, "/jobs/{id}", "取消工作",
		"排隊中的直接取消；處理中的在下一個安全點停下（AI 正在對時的話，NAS 不等它做完）。", tJobs),
		func(ctx context.Context, in *jobID) (*body[jobs.Summary], error) {
			s, err := a.Jobs.Cancel(in.ID)
			if err != nil {
				return nil, huma.Error404NotFound("找不到工作")
			}
			return &body[jobs.Summary]{s}, nil
		})

	huma.Register(api, op("export", http.MethodPost, "/export", "匯出",
		"把「已確認」的伴唱帶依曲庫結構同步到匯出資料夾（檔名「歌手 - 歌名.mp4」；設定打開時加上原曲音訊「歌手 - 歌名_original.m4a」）。只在呼叫時執行，不會自動匯出；"+
			"取消確認、改名或換資料夾的歌，舊的匯出檔會在這時移除。", tLibrary),
		func(ctx context.Context, _ *struct{}) (*body[export.Result], error) {
			res, err := a.Export(ctx)
			if err != nil {
				return nil, fail(err)
			}
			return &body[export.Result]{res}, nil
		})

	huma.Register(api, op("list-workers", http.MethodGet, "/workers", "AI 伺服器", "名稱、GPU、版本是否相符、目前的任務、最後連線時間。", tSystem),
		func(ctx context.Context, _ *struct{}) (*body[[]scheduler.Worker], error) {
			return &body[[]scheduler.Worker]{a.Sched.Workers()}, nil
		})

	huma.Register(api, op("update-worker", http.MethodPatch, "/workers/{name}", "停用 / 啟用 AI 伺服器", "停用的領不到新任務，手上的任務照常做完。", tSystem),
		func(ctx context.Context, in *workerPatch) (*struct{}, error) {
			return nil, fail(a.SetWorkerDisabled(in.Name, in.Body.Disabled))
		})

	huma.Register(api, op("get-settings", http.MethodGet, "/settings", "全域設定", "", tSystem),
		func(ctx context.Context, _ *struct{}) (*body[library.Settings], error) {
			return &body[library.Settings]{a.Store.Library().Settings}, nil
		})

	huma.Register(api, op("update-settings", http.MethodPatch, "/settings", "修改全域設定",
		"字幕大小、每種語言的字型。改了之後做好的伴唱帶顯示需更新（只重燒）。", tSystem),
		func(ctx context.Context, in *body[app.SettingsUpdate]) (*body[library.Settings], error) {
			s, err := a.UpdateSettings(in.Body)
			if err != nil {
				return nil, fail(err)
			}
			return &body[library.Settings]{s}, nil
		})

	huma.Register(api, op("list-fonts", http.MethodGet, "/fonts", "字型", "fonts/ 裡的字型（含 .ttc 裡的每一個）。字型檔本身在 GET /fonts/{sha256}。", tSystem),
		func(ctx context.Context, _ *struct{}) (*body[[]fonts.Font], error) {
			return &body[[]fonts.Font]{a.Fonts.List()}, nil
		})

	huma.Register(api, op("get-system", http.MethodGet, "/system", "系統資訊", "版本、yt-dlp、磁碟用量。", tSystem),
		func(ctx context.Context, _ *struct{}) (*body[app.System], error) {
			return &body[app.System]{a.SystemInfo()}, nil
		})

	huma.Register(api, op("rollback-ytdlp", http.MethodPost, "/system/ytdlp/rollback", "yt-dlp 退回上一版",
		"自動更新後下載失敗時用；再按一次可以換回新版。", tSystem),
		func(ctx context.Context, _ *struct{}) (*body[app.System], error) {
			if _, err := a.RollbackYTDLP(); err != nil {
				return nil, fail(err)
			}
			return &body[app.System]{a.SystemInfo()}, nil
		})
}
