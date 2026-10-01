"""REST API v1：給其他服務串接用（網頁 UI 目前仍用 /api 底下的內部端點，不在規格裡）。

這個檔案（端點）與 api_schema.py（資料模型）就是 API 規格：OpenAPI（Swagger）由 FastAPI 從程式碼產生，
執行中開 /docs（Swagger UI）、/redoc 或 /openapi.json；不啟動伺服器時用 `python scripts/openapi.py`
輸出 docs/openapi.json。端點的說明寫在函式的 docstring（第一行是摘要），會直接出現在規格裡。

慣例
- 一首歌只有一個 id：影片 id（手動放入的檔案是 local-xxxxxxxx）
- GET 讀取、POST 建立、PATCH 只改有給的欄位、PUT 整個取代、DELETE 刪除 / 取消
- 會花時間的處理都是「工作」：建立時回 202 與 Location，之後用 GET /jobs/{id} 或 GET /events 追蹤
- 錯誤一律回 {"detail": "說明"}
- 設了環境變數 SONG_API_TOKEN 時，所有端點都要帶 Authorization: Bearer <token>
"""
from __future__ import annotations

import asyncio
import inspect
import json
import os

from fastapi import APIRouter, Depends, HTTPException, Query, Response
from fastapi.responses import StreamingResponse
from fastapi.routing import APIRoute
from fastapi.security import HTTPAuthorizationCredentials, HTTPBearer

from songtool import backup, catalog, export, karaoke, lyrics, settings
from songtool.download import Download, list_downloads

from api_schema import (Error, Folder, FolderPatch, Job, JobLog, JobStatus, LinePatch, Lyrics, LyricsBody,
                        LyricsFormat, Media, NewFolder, NewSong, QaResult, Settings, SettingsPatch, Song, SongDetail,
                        SongJob, SongPatch, StageStatus, Timing)

API_VERSION = "1.0.0"
# 規格最上方給 API 使用者看的說明（Markdown）
DESCRIPTION = """kara-creator 的 REST API：管理曲庫、下載新歌、修改歌詞與時間、排處理工作、接收即時事件。

**慣例**

- 一首歌只有一個 id：影片 id（YouTube 的 11 碼），手動放入的檔案是 `local-xxxxxxxx`
- GET 讀取、POST 建立、PATCH 只改有給的欄位、PUT 整個取代、DELETE 刪除 / 取消
- 會花時間的處理都是「工作」：建立時回 `202` 與 `Location: /api/v1/jobs/{id}`，之後用 `GET /api/v1/jobs/{id}` 或 `GET /api/v1/events` 追蹤
- 錯誤一律回 `{"detail": "說明"}`
- 伺服器設了環境變數 `SONG_API_TOKEN` 時，所有端點都要帶 `Authorization: Bearer <token>`（右上角 Authorize 可以輸入）

規格由程式碼產生（`ui/api_v1.py` 端點、`ui/api_schema.py` 資料模型），檔案版在 repo 的 `docs/openapi.json`。
"""
TAGS = [
    {"name": "歌曲", "description": "曲庫裡的歌：查詢、下載新歌、修改歌名 / 演唱者 / 語言 / 資料夾、標記已確認"},
    {"name": "歌詞與時間", "description": "歌詞（三種格式）、逐字時間、對時檢查結果、可播放的檔案"},
    {"name": "工作", "description": "背景處理：去人聲、製作伴唱帶、檢查對時、AI 重對。建立後回 202，用 GET 或事件追蹤"},
    {"name": "資料夾", "description": "曲庫的巢狀資料夾（只是整理結構，不會搬動檔案）"},
    {"name": "設定", "description": "全域設定（套用到所有歌），例如字幕大小"},
    {"name": "事件", "description": "Server-Sent Events 即時通知"},
]

# 各端點共用的錯誤回應（寫進規格）
E401 = {401: {"model": Error, "description": "設了 SONG_API_TOKEN，但沒帶或帶錯 Bearer token"}}
E404 = {404: {"model": Error, "description": "找不到這個資源"}}
E400 = {400: {"model": Error, "description": "參數不合理（例如時間移過頭）"}}
E409 = {409: {"model": Error, "description": "目前的狀態不允許（例如歌正在處理中、伴唱帶還沒做好）"}}

_bearer = HTTPBearer(auto_error=False, description="只有設了環境變數 SONG_API_TOKEN 時才需要")


def build(srv) -> APIRouter:
    """srv 是 ui/server.py 這個模組（共用它的工作佇列與曲庫函式）。"""

    def auth(credentials: HTTPAuthorizationCredentials | None = Depends(_bearer)) -> None:
        token = os.environ.get("SONG_API_TOKEN")
        if token and (credentials is None or credentials.credentials != token):
            raise HTTPException(401, "需要 Authorization: Bearer <SONG_API_TOKEN>")

    router = APIRouter(prefix="/api/v1", dependencies=[Depends(auth)], responses=E401)

    # ---- 共用 ----------------------------------------------------------------

    def find(song_id: str) -> Download:
        item = next((d for d in list_downloads() if backup.song_id(d) == song_id), None)
        if item is None:
            raise HTTPException(404, f"找不到歌曲：{song_id}")
        return item

    def ids_by_name() -> dict[str, str]:
        return {d.name: backup.song_id(d) for d in list_downloads()}

    def song_out(state: dict) -> dict:
        """把 UI 用的歌曲狀態整理成 v1 的 Song。"""
        return {
            "id": state["id"] or state["name"],
            "title": state["title"],
            "artist": state["artist"],
            "language": state["language"] or None,
            "folder": state["folder"],
            "order": state["order"],
            "note": state["note"],
            "translation": {"burn": state["translation"], "lines": state["translation_lines"]},
            "custom": {"title": state["custom_title"], "artist": state["custom_artist"]},
            "source": {"url": state["url"], "link": state["link"] or None, "title": state["source_title"],
                       "duration": state["duration"], "uploader": state["uploader"], "mode": state["mode"]},
            "stages": state["stages"],
            "qa": state["qa"],
            "export": {"path": state["export_name"], "exists": state["exported"]},
            "job": state["job"]["id"] if state["job"] else None,
            "approval": {"status": state["approval"], "at": state["approved_at"] or None},
        }

    def states() -> list[dict]:
        busy = srv.jobs.busy_items()
        items = list_downloads()
        cat = catalog.include(items)
        names = export.targets(cat, items)
        ordered = sorted(items, key=lambda it: cat.sort_key(cat.songs[catalog.song_key(it)]))
        return [srv._item_state(it, busy, cat, names) for it in ordered]

    def state_of(item: Download) -> dict:
        items = list_downloads()
        cat = catalog.include(items)
        return srv._item_state(item, srv.jobs.busy_items(), cat, export.targets(cat, items))

    def job_out(summary: dict, names: dict[str, str] | None = None) -> dict:
        names = names if names is not None else ids_by_name()
        keys = ("id", "title", "steps", "lane", "status", "stage", "progress", "error", "url",
                "created", "started", "finished")
        return {**{k: summary.get(k) for k in keys}, "song": names.get(summary.get("item"))}

    def submit(steps: list[str], response: Response, **kwargs) -> dict:
        try:
            job = srv.jobs.submit(steps, **kwargs)
        except ValueError as exc:
            raise HTTPException(400, str(exc))
        response.headers["Location"] = f"/api/v1/jobs/{job.id}"
        return job_out(job.summary())

    def folders_out() -> list[dict]:
        cat = catalog.include(list_downloads())
        return [{k: f[k] for k in ("id", "name", "parent", "order", "path", "path_label", "songs", "folders")}
                for f in srv._folders_state(cat)]

    # ---- 歌曲 ----------------------------------------------------------------

    @router.get("/songs", tags=["歌曲"], response_model=list[Song])
    def list_songs(folder: str | None = Query(None, description="只列這個資料夾（id）裡的歌"),
                   q: str | None = Query(None, description="在歌名、演唱者、原始標題裡搜尋"),
                   stage: StageStatus | None = Query(None, description="依伴唱帶狀態篩選")):
        """歌曲列表

        依曲庫順序（資料夾、再依資料夾內順序）列出所有歌。
        """
        out = []
        for state in states():
            if folder is not None and state["folder"] != folder:
                continue
            if stage and state["stages"]["karaoke"] != stage:
                continue
            if q and q.lower() not in f"{state['title']} {state['artist']} {state['source_title']}".lower():
                continue
            out.append(song_out(state))
        return out

    @router.post("/songs", tags=["歌曲"], status_code=202, response_model=Job, responses=E400)
    def create_song(body: NewSong, response: Response):
        """下載新歌

        建立下載工作（make=true 時下載後接著去人聲並製作伴唱帶）。回 202 與 Location: /api/v1/jobs/{id}；
        下載完成前工作的 song 為 null。
        """
        steps = ["download", "separate", "karaoke"] if body.make else ["download"]
        return submit(steps, response, url=body.url.strip(), lyrics_text=body.lyrics,
                      options={"folder": body.folder, "audio_only": body.audio_only})

    @router.get("/songs/{song_id}", tags=["歌曲"], response_model=SongDetail, responses=E404)
    def get_song(song_id: str):
        """單首歌

        包含可以播放 / 下載的檔案（media）。
        """
        state = state_of(find(song_id))
        return {**song_out(state), "media": state["media"]}

    @router.patch("/songs/{song_id}", tags=["歌曲"], response_model=SongDetail, responses={**E404, **E409})
    def patch_song(song_id: str, body: SongPatch):
        """修改歌曲

        只修改有給的欄位。approved=true 確認「目前這一版」成品沒問題；之後成品有變動（重新製作、調整時間、
        改歌詞），approval.status 會變成 stale。伴唱帶還沒做好時確認會回 409。
        """
        item = find(song_id)
        kwargs = {k: getattr(body, k) for k in ("title", "artist", "language", "note", "translation") if k in body.model_fields_set}
        if "folder" in body.model_fields_set:
            kwargs["folder"] = body.folder
        if body.link is not None:
            if item.info.get("url"):
                raise HTTPException(409, "這首歌是用網址下載的，連結不能修改")
            kwargs["link"] = body.link
        if kwargs:
            srv._catalog_call(lambda cat: cat.update_song(catalog.song_key(item), **kwargs))
        if body.approved is not None:
            srv.set_approval(item, body.approved)
        return get_song(song_id)

    # ---- 歌詞與時間 ----------------------------------------------------------

    @router.get("/songs/{song_id}/lyrics", tags=["歌詞與時間"], response_model=Lyrics, responses=E404)
    def get_song_lyrics(song_id: str, format: LyricsFormat = Query("file", description="歌詞格式")):
        """讀取歌詞

        - file：存檔格式，只含手動指定的讀音
        - annotated：標註原文，含演唱者標籤與每個漢字的讀音（自動判斷的也列出）
        - plain：只有歌詞本身
        """
        data = srv.get_lyrics(find(song_id).name)
        return {"format": format, "text": data[{"file": "text"}.get(format, format)], "exists": data["exists"]}

    @router.put("/songs/{song_id}/lyrics", tags=["歌詞與時間"], response_model=Lyrics, responses=E404)
    def put_song_lyrics(song_id: str, body: LyricsBody):
        """取代歌詞

        歌詞文字改變後，下次製作伴唱帶會整首重新對時（手動調整的時間會被取代）。
        format=plain 時，沒改到的句子保留原本的演唱者與讀音。
        """
        item = find(song_id)
        language = catalog.load().songs[catalog.song_key(item)].language or None
        if body.format == "annotated":
            doc = srv._from_annotated(body.text, language)
        elif body.format == "plain":
            current = lyrics.find(item)
            base = lyrics.parse(current.read_text(encoding="utf-8-sig")) if current else lyrics.Document()
            doc = srv._from_plain(body.text, base)
        else:
            doc = lyrics.parse(body.text)
        srv.put_lyrics(item.name, srv.LyricsText(text=lyrics.serialize(doc)))
        return get_song_lyrics(song_id, body.format)

    @router.get("/songs/{song_id}/timing", tags=["歌詞與時間"], response_model=Timing, responses=E404)
    def get_timing(song_id: str):
        """讀取時間

        每句與每個字的開始 / 結束時間。還沒對時過回 404。
        """
        data = karaoke.timing(find(song_id))
        if data is None:
            raise HTTPException(404, "還沒有對時結果，請先製作伴唱帶")
        return data

    @router.patch("/songs/{song_id}/timing/lines/{line}", tags=["歌詞與時間"], response_model=Timing,
                  responses={**E400, **E404})
    def patch_line(song_id: str, line: int, body: LinePatch):
        """移動一句的開始時間

        只移這一句（不能早於上一句開頭、不能晚於下一句開頭）。修改後伴唱帶顯示需更新，
        重新製作時只重產字幕與燒錄、不會重新對時。整段偏掉請改用 retime 工作。
        """
        item = find(song_id)
        if (body.start is None) == (body.delta is None):
            raise HTTPException(400, "start 與 delta 要給其中一個")
        delta = body.delta
        if body.start is not None:
            current = karaoke.timing(item)
            if not current or not 0 <= line < len(current["lines"]):
                raise HTTPException(404, "沒有這一句")
            delta = body.start - current["lines"][line]["start"]
        try:
            return karaoke.shift_timing(item, line, delta, following=False)
        except ValueError as exc:
            raise HTTPException(400, str(exc))

    @router.get("/songs/{song_id}/qa", tags=["歌詞與時間"], response_model=QaResult, responses=E404)
    def get_song_qa(song_id: str):
        """對時檢查結果

        只列出有疑慮的句子。對時之後有變動的話 checked 為 false（要重新檢查）。
        """
        return srv.get_qa(find(song_id).name)

    @router.get("/songs/{song_id}/media", tags=["歌詞與時間"], response_model=list[Media], responses=E404)
    def get_media(song_id: str):
        """可以播放 / 下載的檔案

        第一個是最終成品（伴唱帶做好時）。url 是相對於伺服器的網址，支援 HTTP Range（可以拖曳播放）。
        """
        return state_of(find(song_id))["media"]

    # ---- 工作 ----------------------------------------------------------------

    @router.post("/songs/{song_id}/jobs", tags=["工作"], status_code=202, response_model=Job,
                 responses={**E400, **E404, **E409})
    def create_song_job(song_id: str, body: SongJob, response: Response):
        """對一首歌排處理

        steps 依序執行；已完成的步驟會略過（除非 force）。retime 以第 line 句目前的開頭為準讓 AI 重新對時：
        mode=from 這句及之後全部、mode=line 只重對這句。回 202 與 Location: /api/v1/jobs/{id}。
        """
        item = find(song_id)
        if "retime" in body.steps and (body.line is None or body.mode is None):
            raise HTTPException(400, "retime 需要 line 與 mode")
        if item.name in srv.jobs.busy_items():
            raise HTTPException(409, "這首歌正在處理中")
        return submit(list(body.steps), response, item=item.name,
                      options={"force": body.force, "realign": body.realign, "line": body.line, "mode": body.mode})

    @router.get("/jobs", tags=["工作"], response_model=list[Job])
    def list_jobs(status: JobStatus | None = Query(None, description="依狀態篩選"),
                  song: str | None = Query(None, description="只列這首歌（id）的工作")):
        """工作列表

        新的在前。伺服器重新啟動後會清空（處理結果都已存在檔案裡）。
        """
        names = ids_by_name()
        out = [job_out(j, names) for j in srv.jobs.list()]
        return [j for j in out if (not status or j["status"] == status) and (not song or j["song"] == song)]

    def get_job_or_404(job_id: str):
        job = srv.jobs.get(job_id)
        if job is None:
            raise HTTPException(404, f"找不到工作：{job_id}")
        return job

    @router.get("/jobs/{job_id}", tags=["工作"], response_model=Job, responses=E404)
    def get_job(job_id: str):
        """單件工作"""
        return job_out(get_job_or_404(job_id).summary())

    @router.get("/jobs/{job_id}/log", tags=["工作"], response_model=JobLog, responses=E404)
    def get_job_log(job_id: str, offset: int = Query(0, ge=0, description="從第幾行開始（上次回應的 next）")):
        """工作紀錄"""
        job = get_job_or_404(job_id)
        return {"lines": job.logs[offset:], "next": len(job.logs)}

    @router.delete("/jobs/{job_id}", tags=["工作"], response_model=Job, responses={**E404, **E409})
    def cancel_job(job_id: str):
        """取消工作

        排隊中的直接移除；處理中的在下一個安全點停下（對時進行中會等對時完成）。收尾工作不能取消。
        """
        job = get_job_or_404(job_id)
        if job.lane == "system":
            raise HTTPException(409, "收尾工作不能取消")
        return job_out(srv.jobs.cancel(job_id).summary())

    # ---- 資料夾 --------------------------------------------------------------

    @router.get("/folders", tags=["資料夾"], response_model=list[Folder])
    def list_folders():
        """資料夾列表"""
        return folders_out()

    @router.post("/folders", tags=["資料夾"], status_code=201, response_model=Folder, responses=E404)
    def create_folder(body: NewFolder):
        """新增資料夾"""
        folder = srv._catalog_call(lambda cat: cat.add_folder(body.name, body.parent))
        return next(f for f in folders_out() if f["id"] == folder.id)

    @router.patch("/folders/{folder_id}", tags=["資料夾"], response_model=Folder, responses={**E400, **E404})
    def patch_folder(folder_id: str, body: FolderPatch):
        """修改資料夾

        改名或移動（有給 parent 欄位才會移動；不能移進自己的子資料夾）。
        """
        kwargs = {"name": body.name}
        if "parent" in body.model_fields_set:
            kwargs["parent"] = body.parent
        srv._catalog_call(lambda cat: cat.update_folder(folder_id, **kwargs))
        return next(f for f in folders_out() if f["id"] == folder_id)

    @router.delete("/folders/{folder_id}", tags=["資料夾"], status_code=204, responses=E404)
    def delete_folder(folder_id: str) -> Response:
        """刪除資料夾

        裡面的歌與子資料夾移到上一層，不會刪除任何檔案。
        """
        srv._catalog_call(lambda cat: cat.delete_folder(folder_id))
        return Response(status_code=204)

    # ---- 設定 ----------------------------------------------------------------

    @router.get("/settings", tags=["設定"], response_model=Settings)
    def get_settings():
        """讀取全域設定"""
        return {**settings.load(), "subtitle_ratio": karaoke.subtitle_ratio()}

    @router.patch("/settings", tags=["設定"], response_model=Settings, responses=E400)
    def patch_settings(body: SettingsPatch):
        """修改全域設定

        只改有給的欄位。改了字幕大小，已經做好的伴唱帶都會顯示需更新（重新燒錄、不會重新對時）。
        """
        return srv.put_settings_value(srv.SettingsBody(**body.model_dump(exclude_none=True)))

    # ---- 事件 ----------------------------------------------------------------

    @router.get("/events", tags=["事件"], response_class=StreamingResponse, responses={200: {
        "description": "text/event-stream",
        "content": {"text/event-stream": {"example": 'event: job\ndata: {"id": "3f2a9c1d", "status": "running", ...}\n\n'
                                                     "event: idle\ndata: {}\n\n"
                                                     'event: library\ndata: {"version": 12}\n\n'}}}})
    async def events():
        """即時事件（Server-Sent Events）

        - `job`：工作的狀態、步驟或進度有變化（data 是 Job）
        - `idle`：兩條佇列都清空了（data 為 {}）
        - `library`：曲庫、歌詞或時間有變動（data 是 {"version": 變動次數}）

        每 15 秒送一個註解行保持連線。
        """

        async def stream():
            seen: dict[str, tuple] = {}
            was_idle = srv.jobs.idle()
            version = srv.library_version
            beat = 0
            while True:
                names = None
                for summary in srv.jobs.list():
                    sig = (summary["status"], summary["stage"], summary["progress"], summary["lane"])
                    if seen.get(summary["id"]) != sig:
                        seen[summary["id"]] = sig
                        names = names if names is not None else ids_by_name()
                        yield f"event: job\ndata: {json.dumps(job_out(summary, names), ensure_ascii=False)}\n\n"
                idle = srv.jobs.idle()
                if idle and not was_idle:
                    yield "event: idle\ndata: {}\n\n"
                was_idle = idle
                if srv.library_version != version:
                    version = srv.library_version
                    yield f"event: library\ndata: {json.dumps({'version': version})}\n\n"
                beat += 1
                if beat >= 15:
                    beat = 0
                    yield ": keep-alive\n\n"
                await asyncio.sleep(1)

        return StreamingResponse(stream(), media_type="text/event-stream",
                                 headers={"Cache-Control": "no-cache", "X-Accel-Buffering": "no"})

    # docstring 第一行當規格裡的摘要（summary），其餘當說明（description）。
    for route in router.routes:
        if isinstance(route, APIRoute) and route.endpoint.__doc__:
            summary, _, rest = inspect.cleandoc(route.endpoint.__doc__).partition("\n")
            route.summary, route.description = summary.strip(), rest.strip()
    return router
