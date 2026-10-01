"""REST API v1：給其他服務串接用（網頁 UI 目前仍用 /api 底下的端點）。

慣例
- 一首歌只有一個 id：影片 id（YouTube 的 11 碼；手動放入的檔案是 local-xxxxxxxx）。
- 資源用名詞、動作用 HTTP 方法：GET 讀取、POST 建立、PATCH 只改有給的欄位、PUT 整個取代、DELETE 刪除 / 取消。
- 會花時間的處理（下載、去人聲、製作伴唱帶、檢查對時、AI 重對）都是「工作」：建立時回 202 與工作內容，
  之後用 GET /jobs/{id} 或 GET /events 追蹤。
- 錯誤回 {"detail": "說明"}，狀態碼 400（參數不對）/ 404（找不到）/ 409（狀態不允許，例如歌詞改過還沒重新對時）。
- 設了環境變數 SONG_API_TOKEN 時，所有 /api/v1 端點都要帶 Authorization: Bearer <token>。
- 完整規格與線上測試：/docs；機器可讀規格：/openapi.json。

端點
  GET    /api/v1/songs                          歌曲列表（?folder=資料夾 id、?q=關鍵字、?stage=karaoke 狀態）
  POST   /api/v1/songs                          給網址下載（可附歌詞、指定資料夾；make=true 一路做到伴唱帶）
  GET    /api/v1/songs/{id}                     單首歌
  PATCH  /api/v1/songs/{id}                     改歌名、演唱者、語言、資料夾
  GET    /api/v1/songs/{id}/lyrics              歌詞（?format=file 存檔格式 / annotated 標註原文 / plain 原始歌詞）
  PUT    /api/v1/songs/{id}/lyrics              取代歌詞（{text, format}；plain 會保留沒改到的句子的標註）
  GET    /api/v1/songs/{id}/timing              每句與每個字的時間
  PATCH  /api/v1/songs/{id}/timing/lines/{n}    移動第 n 句（從 0 起算）的開始時間：{start} 或 {delta}，只移這一句
  GET    /api/v1/songs/{id}/qa                  對時檢查結果（只列有疑慮的句子）
  GET    /api/v1/songs/{id}/media               可以播放 / 下載的檔案
  POST   /api/v1/songs/{id}/jobs                對這首歌排處理：{steps: [separate, karaoke, check, retime], ...}
  GET    /api/v1/jobs                           工作列表（?status=、?song=）
  GET    /api/v1/jobs/{id}                      單件工作
  GET    /api/v1/jobs/{id}/log                  工作紀錄（?offset= 從第幾行開始）
  DELETE /api/v1/jobs/{id}                      取消工作
  GET    /api/v1/folders                        資料夾
  POST   /api/v1/folders                        新增資料夾：{name, parent}
  PATCH  /api/v1/folders/{id}                   改名或移動：{name, parent}
  DELETE /api/v1/folders/{id}                   刪除資料夾（裡面的歌與子資料夾移到上一層，不刪檔案）
  GET    /api/v1/events                         Server-Sent Events：job（工作狀態變化）、idle（佇列清空）、
                                                library（曲庫、歌詞或時間有變動）
"""
from __future__ import annotations

import asyncio
import json
import os
from typing import Literal

from fastapi import APIRouter, Depends, Header, HTTPException, Query, Response
from fastapi.responses import StreamingResponse
from pydantic import BaseModel, Field

from songtool import backup, catalog, export, karaoke, lyrics
from songtool.download import Download, list_downloads

# 工作的步驟（不含 download：下載用 POST /songs）。
SongStep = Literal["separate", "karaoke", "check", "retime"]
LyricsFormat = Literal["file", "annotated", "plain"]


class NewSong(BaseModel):
    url: str
    folder: str | None = None
    audio_only: bool = False
    lyrics: str | None = Field(None, description="歌詞（存檔格式），下載後存起來")
    make: bool = Field(True, description="true：下載後接著去人聲並製作伴唱帶；false：只下載")


class SongPatch(BaseModel):
    title: str | None = Field(None, description="空字串 = 還原自動辨識")
    artist: str | None = Field(None, description="空字串 = 還原自動辨識")
    language: Literal["", "zh", "nan", "yue", "ja", "en"] | None = Field(None, description="空字串 = 依歌詞文字判斷")
    folder: str | None = Field(None, description="資料夾 id；null = 最上層（有給這個欄位才會移動）")


class LyricsBody(BaseModel):
    text: str
    format: LyricsFormat = "file"


class LinePatch(BaseModel):
    start: float | None = Field(None, description="新的開始時間（秒）")
    delta: float | None = Field(None, description="移動量（秒，負數往前）")


class SongJob(BaseModel):
    steps: list[SongStep]
    force: bool = Field(False, description="已完成的也重做（去人聲重新分離、整首重新對時並覆蓋手動改過的字幕）")
    realign: bool = Field(False, description="製作伴唱帶時重新對時")
    line: int | None = Field(None, description="retime：以第幾句（從 0 起算）目前的開頭為準")
    mode: Literal["from", "line"] | None = Field(None, description="retime：from 這句及之後全部 / line 只重對這句")


class NewFolder(BaseModel):
    name: str
    parent: str | None = None


class FolderPatch(BaseModel):
    name: str | None = None
    parent: str | None = Field(None, description="上層資料夾 id；null = 最上層（有給這個欄位才會移動）")


def build(srv) -> APIRouter:
    """srv 是 ui/server.py 這個模組（共用它的工作佇列與曲庫函式）。"""

    def auth(authorization: str | None = Header(None)) -> None:
        token = os.environ.get("SONG_API_TOKEN")
        if token and authorization != f"Bearer {token}":
            raise HTTPException(401, "需要 Authorization: Bearer <SONG_API_TOKEN>")

    router = APIRouter(prefix="/api/v1", tags=["v1"], dependencies=[Depends(auth)])

    # ---- 共用 ----------------------------------------------------------------

    def find(song_id: str) -> Download:
        item = next((d for d in list_downloads() if backup.song_id(d) == song_id), None)
        if item is None:
            raise HTTPException(404, f"找不到歌曲：{song_id}")
        return item

    def ids_by_name() -> dict[str, str]:
        return {d.name: backup.song_id(d) for d in list_downloads()}

    def song_out(state: dict) -> dict:
        """把 UI 用的歌曲狀態整理成 v1 的格式。"""
        return {
            "id": state["id"] or state["name"],
            "title": state["title"],
            "artist": state["artist"],
            "language": state["language"] or None,     # 手動指定的語言；null = 依歌詞文字判斷
            "folder": state["folder"],
            "order": state["order"],
            "custom": {"title": state["custom_title"], "artist": state["custom_artist"]},
            "source": {"url": state["url"], "title": state["source_title"], "duration": state["duration"],
                       "uploader": state["uploader"], "mode": state["mode"]},
            "stages": state["stages"],
            "qa": state["qa"],
            "export": {"path": state["export_name"], "exists": state["exported"]},
            "job": state["job"]["id"] if state["job"] else None,
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
        response.status_code = 202
        response.headers["Location"] = f"/api/v1/jobs/{job.id}"
        return job_out(job.summary())

    # ---- 歌曲 ----------------------------------------------------------------

    @router.get("/songs")
    def list_songs(folder: str | None = None, q: str | None = None, stage: str | None = None) -> list[dict]:
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


    @router.post("/songs", status_code=202)
    def create_song(body: NewSong, response: Response) -> dict:
        steps = ["download", "separate", "karaoke"] if body.make else ["download"]
        return submit(steps, response, url=body.url.strip(), lyrics_text=body.lyrics,
                      options={"folder": body.folder, "audio_only": body.audio_only})

    @router.get("/songs/{song_id}")
    def get_song(song_id: str) -> dict:
        state = state_of(find(song_id))
        return {**song_out(state), "media": state["media"]}


    @router.patch("/songs/{song_id}")
    def patch_song(song_id: str, body: SongPatch) -> dict:
        item = find(song_id)
        kwargs = {k: getattr(body, k) for k in ("title", "artist", "language") if k in body.model_fields_set}
        if "folder" in body.model_fields_set:
            kwargs["folder"] = body.folder
        srv._catalog_call(lambda cat: cat.update_song(catalog.song_key(item), **kwargs))
        return get_song(song_id)

    @router.get("/songs/{song_id}/lyrics")
    def get_song_lyrics(song_id: str, format: LyricsFormat = "file") -> dict:
        data = srv.get_lyrics(find(song_id).name)
        return {"format": format, "text": data[{"file": "text"}.get(format, format)], "exists": data["exists"]}


    @router.put("/songs/{song_id}/lyrics")
    def put_song_lyrics(song_id: str, body: LyricsBody) -> dict:
        item = find(song_id)
        language = (catalog.load().songs[catalog.song_key(item)].language or None)
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

    @router.get("/songs/{song_id}/timing")
    def get_timing(song_id: str) -> dict:
        data = karaoke.timing(find(song_id))
        if data is None:
            raise HTTPException(404, "還沒有對時結果，請先製作伴唱帶")
        return data


    @router.patch("/songs/{song_id}/timing/lines/{line}")
    def patch_line(song_id: str, line: int, body: LinePatch) -> dict:
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

    @router.get("/songs/{song_id}/qa")
    def get_song_qa(song_id: str) -> dict:
        return srv.get_qa(find(song_id).name)

    @router.get("/songs/{song_id}/media")
    def get_media(song_id: str) -> list[dict]:
        return state_of(find(song_id))["media"]


    @router.post("/songs/{song_id}/jobs", status_code=202)
    def create_song_job(song_id: str, body: SongJob, response: Response) -> dict:
        item = find(song_id)
        if "retime" in body.steps and (body.line is None or body.mode is None):
            raise HTTPException(400, "retime 需要 line 與 mode")
        if item.name in srv.jobs.busy_items():
            raise HTTPException(409, "這首歌正在處理中")
        return submit(list(body.steps), response, item=item.name,
                      options={"force": body.force, "realign": body.realign, "line": body.line, "mode": body.mode})

    # ---- 工作 ----------------------------------------------------------------

    @router.get("/jobs")
    def list_jobs(status: str | None = None, song: str | None = None) -> list[dict]:
        names = ids_by_name()
        out = [job_out(j, names) for j in srv.jobs.list()]
        return [j for j in out if (not status or j["status"] == status) and (not song or j["song"] == song)]

    def get_job_or_404(job_id: str):
        job = srv.jobs.get(job_id)
        if job is None:
            raise HTTPException(404, f"找不到工作：{job_id}")
        return job

    @router.get("/jobs/{job_id}")
    def get_job(job_id: str) -> dict:
        return job_out(get_job_or_404(job_id).summary())

    @router.get("/jobs/{job_id}/log")
    def get_job_log(job_id: str, offset: int = Query(0, ge=0)) -> dict:
        job = get_job_or_404(job_id)
        return {"lines": job.logs[offset:], "next": len(job.logs)}

    @router.delete("/jobs/{job_id}")
    def cancel_job(job_id: str) -> dict:
        job = get_job_or_404(job_id)
        if job.lane == "system":
            raise HTTPException(409, "收尾工作不能取消")
        return job_out(srv.jobs.cancel(job_id).summary())

    # ---- 資料夾 --------------------------------------------------------------

    @router.get("/folders")
    def list_folders() -> list[dict]:
        cat = catalog.include(list_downloads())
        return [{k: f[k] for k in ("id", "name", "parent", "order", "path", "path_label", "songs", "folders")}
                for f in srv._folders_state(cat)]


    @router.post("/folders", status_code=201)
    def create_folder(body: NewFolder) -> dict:
        folder = srv._catalog_call(lambda cat: cat.add_folder(body.name, body.parent))
        return next(f for f in list_folders() if f["id"] == folder.id)


    @router.patch("/folders/{folder_id}")
    def patch_folder(folder_id: str, body: FolderPatch) -> dict:
        kwargs = {"name": body.name}
        if "parent" in body.model_fields_set:
            kwargs["parent"] = body.parent
        srv._catalog_call(lambda cat: cat.update_folder(folder_id, **kwargs))
        return next(f for f in list_folders() if f["id"] == folder_id)

    @router.delete("/folders/{folder_id}", status_code=204)
    def delete_folder(folder_id: str) -> Response:
        srv._catalog_call(lambda cat: cat.delete_folder(folder_id))
        return Response(status_code=204)

    # ---- 事件 ----------------------------------------------------------------

    @router.get("/events")
    async def events() -> StreamingResponse:
        """Server-Sent Events。event: job（data 是工作，狀態或進度有變就送）、idle（佇列清空）、
        library（曲庫、歌詞或時間有變動；data 是變動次數）。每 15 秒送一個註解行保持連線。"""

        async def stream():
            seen: dict[str, tuple] = {}
            was_idle = srv.jobs.idle()
            version = srv.library_version
            beat = 0.0
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

    return router
