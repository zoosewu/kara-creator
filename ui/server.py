#!/usr/bin/env python3
"""本機網頁 UI。預設只監聽 127.0.0.1（--lan 開放區域網路），所有處理都透過 songtool 完成。

    python ui/server.py              # 啟動並開啟瀏覽器
    python ui/server.py --port 9000 --no-browser
    python ui/server.py --lan        # 開放同一個區域網路的其他裝置連線
"""
from __future__ import annotations

import argparse
import asyncio
import difflib
import logging
import os
import sys
import threading
import webbrowser
from contextlib import asynccontextmanager
from pathlib import Path
from urllib.parse import quote

ROOT = Path(__file__).resolve().parent.parent
sys.path.insert(0, str(ROOT))

from fastapi import FastAPI, HTTPException, Request  # noqa: E402
from fastapi.routing import APIRoute  # noqa: E402
from fastapi.responses import FileResponse  # noqa: E402
from fastapi.staticfiles import StaticFiles  # noqa: E402
from pydantic import BaseModel  # noqa: E402

from songtool import backup, catalog, config, export, hooks, karaoke, lyrics, manifest, qa, reading, titles  # noqa: E402
from songtool.download import refresh_metadata  # noqa: E402
from songtool.local import import_local  # noqa: E402
from songtool.download import Download, list_downloads  # noqa: E402
from songtool.jobs import JobManager  # noqa: E402
from songtool.separate import is_current, output_dir_for  # noqa: E402

STATIC = Path(__file__).resolve().parent / "static"

@asynccontextmanager
async def _lifespan(_app: FastAPI):
    yield
    # 關閉：先停掉處理中的工作，避免 Demucs / ffmpeg 子程序留在背景。
    active = [j for j in jobs.list() if j["status"] in ("queued", "running")]
    if active:
        print(f"正在停止 {len(active)} 件工作…", flush=True)
        left = await asyncio.to_thread(jobs.shutdown, 5.0)
        for job in left:
            print(f"  「{job.title}」{job.stage or '處理'}中無法立即中斷，直接結束（下次重新執行即可）", flush=True)


app = FastAPI(title="song", lifespan=_lifespan)
jobs = JobManager(echo=lambda msg: print(msg, flush=True), on_idle=lambda finished: _wrapup("jobs_finished", finished))
app.mount("/static", StaticFiles(directory=STATIC), name="static")


# ---- 收尾：資料備份與使用者的 hook ------------------------------------------

_wrapup_lock = threading.Lock()
WRAPUP_LABELS = {"jobs_finished": "佇列清空", "library_changed": "曲庫有變動"}


def _wrapup(reason: str, finished=()) -> None:
    """佇列清空或曲庫變動後：備份資料到 data/（commit + push），再執行使用者的 hook。"""
    summaries = hooks.job_summaries(finished)

    def task(log) -> None:
        with _wrapup_lock:   # 兩次收尾不要同時跑（git、rsync 會互相干擾）
            backup.snapshot(log)
            done = sum(1 for j in summaries if j["status"] == "done")
            message = (f"佇列清空：完成 {done} 件" if reason == "jobs_finished" else "曲庫有變動")
            backup.publish(message, log)
            hooks.run_user_hook(reason, summaries, log)

    jobs.run_system(f"收尾（{WRAPUP_LABELS.get(reason, reason)}）", task)


def _changed_quietly() -> None:
    # 佇列還在跑的話，等佇列清空時一起處理。
    if jobs.idle():
        _wrapup("library_changed")


# 改歌名、移資料夾、改歌詞或時間等操作：安靜 30 秒後做一次收尾（連續操作只跑一次）。
library_changes = hooks.Debouncer(float(os.environ.get("SONG_CHANGE_DELAY", 30)), _changed_quietly)
# 不會改到曲庫或歌詞的操作：建立 / 取消工作、文字轉換、在本機開資料夾。
_READ_ONLY_POSTS = ("/api/jobs", "/api/lyrics/convert", "/api/export/open", "/api/v1/jobs")
library_version = 0   # 曲庫、歌詞或時間每變動一次加一（給 /api/v1/events 通知外部服務）


@app.middleware("http")
async def _track_changes(request: Request, call_next):
    response = await call_next(request)
    path = request.url.path
    if (request.method in ("POST", "PUT", "PATCH", "DELETE") and path.startswith("/api/")
            and response.status_code < 400 and not path.startswith(_READ_ONLY_POSTS)
            and not path.endswith("/open") and "/jobs" not in path):
        global library_version
        library_version += 1
        library_changes.touch()
    return response


# ---- 狀態 ------------------------------------------------------------------

def _media_url(path: Path) -> str | None:
    try:
        rel = path.resolve().relative_to(config.OUTPUT_DIR.resolve())
    except ValueError:
        return None
    return "/media/" + quote(rel.as_posix())


MEDIA_ORDER = [
    ("karaoke", "伴唱帶", "最終輸出：伴奏＋字幕"),
    ("karaoke_original", "原曲＋字幕", "原唱加上字幕"),
    ("source", "原始影片", "下載的原版"),
    ("instrumental", "伴奏", "去人聲後的伴奏"),
    ("vocals", "人聲", "分離出來的人聲"),
]


def _item_state(item: Download, busy: dict, cat: catalog.Catalog,
                export_names: dict[str, str]) -> dict:
    sep_dir = output_dir_for(item.file)
    sep = manifest.read(sep_dir / manifest.SEPARATE)
    separated = is_current(sep, item.file, sep_dir)
    lyrics_path = lyrics.find(item)
    k_rec = manifest.read(karaoke.output_dir(item) / karaoke.RECORD) or {}

    key = catalog.song_key(item)
    song = cat.songs[key]
    meta = lyrics.load(lyrics_path).meta if lyrics_path else {}
    title, artist = catalog.display_info(item, song, meta)
    auto = titles.guess(item.info)
    export_rel = Path(export_names[key])

    # 可預覽的版本，依重要性排序：第一個就是預覽按鈕預設播放的版本（最終輸出優先）。
    found: dict[str, Path] = {"source": item.file}
    for target, video in k_rec.get("videos", {}).items():
        found["karaoke" if target == "instrumental" else "karaoke_original"] = \
            karaoke.output_dir(item) / video["file"]
    if separated:
        for name in sep["outputs"]:
            stem = Path(name).stem.rsplit("_", 1)[-1]
            if stem in ("vocals", "instrumental"):
                found[stem] = sep_dir / name
    media = [
        {"key": key, "label": label, "desc": desc, "url": _media_url(found[key])}
        for key, label, desc in MEDIA_ORDER
        if key in found and found[key].is_file()
    ]

    return {
        "name": item.name,
        "key": key,
        "id": item.info.get("id"),
        "order": song.number,          # 同一層內的排列順序（拖曳排序用，不顯示）
        "folder": song.folder,
        "title": title,
        "artist": artist,
        "custom_title": song.title,
        "custom_artist": song.artist,
        "language": song.language,           # 手動指定的演唱語言（空字串 = 依歌詞文字判斷）
        "approval": karaoke.approval(item, song),   # 已確認成品沒問題：approved / stale / None
        "approved_at": song.approved_at,
        "auto_title": auto.title,
        "auto_artist": auto.artist,
        "auto_source": auto.source,
        "lyrics_title": meta.get("title", ""),
        "lyrics_artist": meta.get("artist", ""),
        "source_title": item.title,
        "export_name": export_rel.as_posix(),
        "exported": (config.EXPORT_DIR / export_rel).is_file(),
        "uploader": item.info.get("uploader"),
        "duration": item.info.get("duration"),
        "mode": item.info.get("mode", "video"),
        "url": item.info.get("url"),            # 下載時的影片連結（手動放入的檔案沒有）
        "link": song.link,                       # 手動放入的影片補上的連結
        "note": song.note,                       # 備註（開頭標題畫面第三行）
        "stages": {
            "download": "done",
            "separate": "done" if separated else ("outdated" if sep else "pending"),
            "lyrics": "done" if lyrics_path else "missing",
            "karaoke": karaoke.status(item),
        },
        "lyrics_path": str(lyrics_path or lyrics.candidates(item)[0]),
        "media": media,
        "qa": qa.summary(karaoke.qa_doc(item)),
        "job": busy.get(item.name),
    }


def _folders_state(cat: catalog.Catalog) -> list[dict]:
    out = []
    for f in sorted(cat.folders.values(), key=lambda f: cat.folder_sort_key(f.id)):
        path = cat.path(f.id)
        out.append({
            "id": f.id, "name": f.name, "order": f.number, "parent": f.parent,
            "label": f.label,
            "path": [p.id for p in path],
            "path_label": " › ".join(p.label for p in path),
            "songs": len(cat.songs_in(f.id)), "folders": len(cat.children(f.id)),
        })
    return out


@app.get("/api/state")
def state() -> dict:
    busy = jobs.busy_items()
    # 手動放進 output/downloads/ 的影音檔：寫完且穩定後自動登記（輪詢時順便檢查）。
    if import_local(log=lambda msg: print(msg, flush=True)):
        _sync_export()
    items = list_downloads()
    cat = catalog.include(items)
    names = export.targets(cat, items)
    ordered = sorted(items, key=lambda it: cat.sort_key(cat.songs[catalog.song_key(it)]))
    return {
        "items": [_item_state(it, busy, cat, names) for it in ordered],
        "folders": _folders_state(cat),
        "jobs": jobs.list(),
        "export_dir": str(config.EXPORT_DIR),
    }


# ---- 工作 ------------------------------------------------------------------

class JobRequest(BaseModel):
    steps: list[str]
    url: str | None = None
    item: str | None = None
    folder: str | None = None       # 新下載的歌放進哪個曲庫資料夾
    audio_only: bool = False
    target: str = "instrumental"
    lyrics: str | None = None
    force: bool = False             # 全部重做（包含覆蓋手動修改的字幕）
    realign: bool = False           # 重新對時
    line: int | None = None         # retime：第幾句（從 0 起算）
    mode: str | None = None         # retime：from（這句之後全部）/ line（只有這句）


@app.post("/api/jobs")
def create_job(req: JobRequest) -> dict:
    try:
        job = jobs.submit(req.steps, url=(req.url or "").strip() or None, item=req.item,
                          options={"audio_only": req.audio_only, "target": req.target,
                                   "folder": req.folder, "force": req.force, "realign": req.realign,
                                   "line": req.line, "mode": req.mode},
                          lyrics_text=req.lyrics)
    except ValueError as exc:
        raise HTTPException(400, str(exc))
    return job.summary()


@app.post("/api/jobs/{job_id}/cancel")
def cancel_job(job_id: str) -> dict:
    try:
        return jobs.cancel(job_id).summary()
    except KeyError as exc:
        raise HTTPException(404, str(exc).strip("'"))


@app.get("/api/jobs/{job_id}/log")
def job_log(job_id: str, offset: int = 0) -> dict:
    job = jobs.get(job_id)
    if job is None:
        raise HTTPException(404, "找不到工作")
    return {"lines": job.logs[offset:], "next": len(job.logs), "job": job.summary()}


# ---- 曲庫 ------------------------------------------------------------------

class FolderBody(BaseModel):
    name: str | None = None
    number: int | None = None
    parent: str | None = None
    move: bool = False          # PATCH 時是否要變更上層（parent 可能本來就是 None）


class SongBody(BaseModel):
    folder: str | None = None
    number: int | None = None
    title: str | None = None
    artist: str | None = None
    language: str | None = None     # 空字串 = 依歌詞文字判斷；None = 不改
    link: str | None = None         # 手動放入的影片補上的原始連結；空字串 = 清除；None = 不改
    note: str | None = None         # 備註（開頭標題畫面第三行）；空字串 = 清除；None = 不改


def _catalog_call(fn):
    try:
        with catalog.edit() as cat:
            result = fn(cat)
    except (KeyError, ValueError) as exc:
        raise HTTPException(400, str(exc).strip("'\""))
    _sync_export()
    return result


def _sync_export() -> None:
    try:
        export.sync()
    except Exception as exc:  # 輸出同步失敗不影響曲庫的修改
        print(f"同步輸出資料夾失敗：{exc}", flush=True)


@app.post("/api/folders")
def create_folder(body: FolderBody) -> dict:
    folder = _catalog_call(lambda cat: cat.add_folder(body.name or "", body.parent, body.number))
    return {"id": folder.id}


@app.patch("/api/folders/{folder_id}")
def update_folder(folder_id: str, body: FolderBody) -> dict:
    def change(cat):
        kwargs = {"name": body.name, "number": body.number}
        if body.move:
            kwargs["parent"] = body.parent
        cat.update_folder(folder_id, **kwargs)
    _catalog_call(change)
    return {"id": folder_id}


@app.delete("/api/folders/{folder_id}")
def delete_folder(folder_id: str) -> dict:
    _catalog_call(lambda cat: cat.delete_folder(folder_id))
    return {"deleted": folder_id}


class PlaceBody(BaseModel):
    kind: str                   # "folder" | "song"
    id: str
    parent: str | None = None
    before: str | None = None   # 排在這個 id 前面；None 表示排到最後


@app.post("/api/place")
def place(body: PlaceBody) -> dict:
    if body.kind not in ("folder", "song"):
        raise HTTPException(400, "kind 必須是 folder 或 song")
    _catalog_call(lambda cat: cat.place(body.kind, body.id, body.parent, body.before))
    return {"id": body.id}


class MoveBody(BaseModel):
    keys: list[str]
    folder: str | None = None
    before: str | None = None   # 排在這首歌前面；None 表示排到最後
    keep_present: bool = True   # 已經在目標資料夾的歌不動（放進資料夾時）；False 時一起重新排位置


@app.post("/api/songs/move")
def move_songs(body: MoveBody) -> dict:
    """批次移動：依 keys 的順序放進 folder，彼此的先後順序保留。"""
    if body.before in body.keys:
        raise HTTPException(400, "插入位置不能是要移動的歌")

    def run(cat) -> int:
        moved = 0
        for key in body.keys:
            song = cat.songs.get(key)
            if song is None:
                raise KeyError("曲庫裡沒有這首歌")
            if body.keep_present and song.folder == body.folder:
                continue
            cat.place("song", key, body.folder, body.before)
            moved += 1
        return moved

    return {"moved": _catalog_call(run)}


@app.put("/api/songs/{key:path}")
def update_song(key: str, body: SongBody) -> dict:
    _catalog_call(lambda cat: cat.update_song(key, folder=body.folder, number=body.number,
                                              title=body.title, artist=body.artist,
                                              language=body.language, link=body.link, note=body.note))
    return {"key": key}


class ApprovalBody(BaseModel):
    approved: bool


def set_approval(item: Download, approved: bool) -> None:
    """標記（或取消）「已確認成品沒問題」。只能確認已經做好、而且是最新的伴唱帶。"""
    sha = karaoke.product_sha1(item) if approved else ""
    if approved and not sha:
        raise HTTPException(409, "伴唱帶還沒做好或需要更新，請先製作完成再確認")
    _catalog_call(lambda cat: cat.update_song(catalog.song_key(item), approved=sha))


@app.put("/api/items/{name}/approval")
def put_approval(name: str, body: ApprovalBody) -> dict:
    set_approval(_get_item(name), body.approved)
    return {"approved": body.approved}


@app.post("/api/export/open")
def open_export() -> dict:
    _sync_export()
    config.EXPORT_DIR.mkdir(parents=True, exist_ok=True)
    os.startfile(config.EXPORT_DIR)  # noqa: S606 - 只開本機資料夾
    return {"opened": str(config.EXPORT_DIR)}


class ShiftBody(BaseModel):
    line: int                 # 第幾句（從 0 起算，與對時結果一致）
    delta: float              # 秒；正數往後、負數往前
    following: bool = False   # 之後的句子一起移（UI 目前只移單句；整段偏掉交給 AI 重對）


@app.get("/api/items/{name}/timing")
def get_timing(name: str) -> dict:
    """每句目前的時間；還沒對時時 lines 為空。"""
    return karaoke.timing(_get_item(name)) or {"lines": [], "adjustments": []}


@app.post("/api/items/{name}/timing")
def post_timing(name: str, body: ShiftBody) -> dict:
    try:
        return karaoke.shift_timing(_get_item(name), body.line, body.delta, body.following)
    except ValueError as exc:
        raise HTTPException(400, str(exc))


@app.get("/api/items/{name}/qa")
def get_qa(name: str) -> dict:
    """對時檢查結果：只列出有問題的句子（可能不準 / 待確認）。"""
    doc = karaoke.qa_doc(_get_item(name))
    if not doc:
        return {"checked": False, "lines": []}
    return {"checked": True, "checked_at": doc.get("checked_at"),
            "lines": [c for c in doc["lines"] if c["status"] != "ok"]}


# ---- 歌詞 ------------------------------------------------------------------

def _get_item(name: str) -> Download:
    item = next((d for d in list_downloads() if d.name == name), None)
    if item is None:
        raise HTTPException(404, "找不到歌曲")
    return item


def _editor_doc(doc: lyrics.Document, language: str | None = None) -> dict:
    """歌詞編輯器用的資料：文件內容加上每句的假名片段。language 沒給時依歌詞文字判斷。"""
    language = language or lyrics.detect_language([ln.text for ln in doc.lyric_lines])
    data = lyrics.to_dict(doc)
    for ln, raw in zip(doc.lines, data["lines"]):
        if ln.kind == "lyric":
            raw["segments"] = reading.furigana(ln.text, language, ln.rubies)
    data["language"] = language
    return data


def _lyrics_views(doc: lyrics.Document, language: str | None = None) -> dict:
    """歌詞編輯器的各種檢視：
        doc        結構（標註模式用，含每句的假名片段）
        text       存檔格式（只記手動指定的讀音）
        annotated  標註原文：演唱者標籤 + 每個漢字的讀音（自動判斷的也列出來）
        plain      原始歌詞：只有歌詞本身，沒有任何標註
    """
    language = language or lyrics.detect_language([ln.text for ln in doc.lyric_lines])
    full = lyrics.Document(meta=dict(doc.meta), lines=[])
    for ln in doc.lines:
        if ln.kind == "lyric":
            rubies = [lyrics.Ruby(seg["start"], seg["end"], seg["ruby"])
                      for seg in reading.furigana(ln.text, language, ln.rubies) if seg["ruby"]]
            full.lines.append(lyrics.Line("lyric", ln.text, ln.singer, rubies))
        else:
            full.lines.append(ln)
    plain = "\n".join("" if ln.kind == "blank" else ln.text for ln in doc.lines)
    text = lyrics.serialize(doc)
    return {"doc": _editor_doc(doc, language), "text": text,
            # 寫在括號裡的讀音（日文歌），編輯器會提示可以轉成讀音標註
            "paren": lyrics.paren_readings(text) if language == "ja" else [],
            "annotated": lyrics.serialize(full), "plain": plain + "\n" if plain else ""}


def _from_annotated(text: str, language: str | None = None) -> lyrics.Document:
    """標註原文轉回結構：和自動判斷相同的讀音不另外記成手動指定。"""
    doc = lyrics.parse(text)
    language = language or lyrics.detect_language([ln.text for ln in doc.lyric_lines])
    for ln in doc.lyric_lines:
        auto = {(seg["start"], seg["end"], seg["ruby"])
                for seg in reading.furigana(ln.text, language, ()) if seg["ruby"]}
        ln.rubies = [r for r in ln.rubies if (r.start, r.end, r.reading) not in auto]
    return doc


def _from_plain(text: str, base: lyrics.Document) -> lyrics.Document:
    """原始歌詞轉回結構：沒改到的句子沿用原本的演唱者與讀音（用 diff 對應，插入或刪除句子也不會錯位）；
    改過的句子保留演唱者，讀音只留下原字沒變的部分。"""
    doc = lyrics.parse(text)
    doc.meta = dict(base.meta)
    old, new = base.lyric_lines, doc.lyric_lines
    matcher = difflib.SequenceMatcher(a=[ln.text for ln in old], b=[ln.text for ln in new], autojunk=False)
    for tag, i1, i2, j1, j2 in matcher.get_opcodes():
        if tag not in ("equal", "replace") or (tag == "replace" and i2 - i1 != j2 - j1):
            continue
        for src, dst in zip(old[i1:i2], new[j1:j2]):
            if dst.singer is None:
                dst.singer = src.singer
            if not dst.rubies:
                dst.rubies = [r for r in src.rubies if dst.text[r.start:r.end] == src.text[r.start:r.end]]
    return doc


class LyricsText(BaseModel):
    text: str


class LyricsConvert(BaseModel):
    text: str | None = None
    doc: dict | None = None
    format: str = "file"    # text 的格式：file（存檔格式）/ annotated（標註原文）/ plain（原始歌詞，doc 為原本的結構）
    language: str | None = None   # 歌曲的演唱語言（台語 / 粵語要逐字標讀音）；None = 依歌詞文字判斷
    paren_to_ruby: bool = False   # 把「運命(さだめ)」這種括號讀音轉成讀音標註（只處理 format=file 的 text）


@app.get("/api/items/{name}/lyrics")
def get_lyrics(name: str) -> dict:
    item = _get_item(name)
    path = lyrics.find(item)
    text = path.read_text(encoding="utf-8-sig") if path else ""
    doc = lyrics.parse(text)
    song = catalog.load().songs.get(catalog.song_key(item))
    views = _lyrics_views(doc, (song.language if song else "") or None)
    if not text:
        views["text"] = views["annotated"] = ""
    return {"path": str(path or lyrics.candidates(item)[0]), "exists": path is not None, **views}


@app.put("/api/items/{name}/lyrics")
def put_lyrics(name: str, body: LyricsText) -> dict:
    item = _get_item(name)
    lyrics.save(item, lyrics.serialize(lyrics.parse(body.text)))
    _sync_export()  # 歌詞檔的 # title 可能影響輸出檔名
    return get_lyrics(name)


@app.post("/api/lyrics/convert")
def convert_lyrics(body: LyricsConvert) -> dict:
    """文字 <-> 結構互轉，回傳各種檢視（見 _lyrics_views）。
    只送 doc：從結構產生；送 text：依 format 解析（plain 需要一起送原本的 doc 以保留標註）。"""
    if body.text is None:
        doc = lyrics.from_dict(body.doc or {})
    elif body.format == "annotated":
        doc = _from_annotated(body.text, body.language)
    elif body.format == "plain":
        doc = _from_plain(body.text, lyrics.from_dict(body.doc or {}))
    else:
        doc = lyrics.parse(lyrics.paren_to_ruby(body.text) if body.paren_to_ruby else body.text)
    return _lyrics_views(doc, body.language)


# ---- 檔案 ------------------------------------------------------------------

@app.get("/media/{path:path}")
def media(path: str) -> FileResponse:
    target = (config.OUTPUT_DIR / path).resolve()
    if not target.is_relative_to(config.OUTPUT_DIR.resolve()) or not target.is_file():
        raise HTTPException(404)
    return FileResponse(target)


class OpenRequest(BaseModel):
    stage: str = "download"


@app.post("/api/items/{name}/open")
def open_folder(name: str, body: OpenRequest) -> dict:
    item = _get_item(name)
    folder = {
        "download": item.folder,
        "separate": output_dir_for(item.file),
        "karaoke": karaoke.output_dir(item),
        "lyrics": config.LYRICS_DIR,
    }.get(body.stage, item.folder)
    if not folder.is_dir():
        folder = item.folder
    os.startfile(folder)  # noqa: S606 - 只開本機資料夾
    return {"opened": str(folder)}


@app.get("/")
def index() -> FileResponse:
    return FileResponse(STATIC / "index.html")


# REST API v1（給其他服務串接，說明見 ui/api_v1.py 與 /docs）
import api_v1  # noqa: E402

app.include_router(api_v1.build(sys.modules[__name__]))

# OpenAPI（Swagger）規格由程式碼產生：只公開 /api/v1；網頁 UI 用的內部端點不放進規格。
app.title = "kara-creator API"
app.version = api_v1.API_VERSION
app.description = api_v1.DESCRIPTION
app.openapi_tags = api_v1.TAGS
for _route in app.routes:
    if isinstance(_route, APIRoute) and not _route.path.startswith("/api/v1"):
        _route.include_in_schema = False


def _startup() -> None:
    try:
        count = refresh_metadata()
        if count:
            print(f"已補抓 {count} 首歌的歌曲資訊", flush=True)
    except Exception as exc:
        print(f"補抓歌曲資訊失敗：{exc}", flush=True)
    _sync_export()


class _QuietShutdown(logging.Filter):
    """關閉時被中斷的連線（例如播放中的影片）會丟出 CancelledError 等例外，屬正常現象，不必印出。"""

    NOISE = (asyncio.CancelledError, KeyboardInterrupt, ConnectionResetError, ConnectionAbortedError)

    def filter(self, record: logging.LogRecord) -> bool:
        exc = record.exc_info[1] if record.exc_info else None
        if isinstance(exc, self.NOISE):
            return False
        # 有些地方直接把 traceback 當成訊息文字記錄，或只是通知「強制中斷了幾個連線」。
        text = record.getMessage()
        last = text.strip().splitlines()[-1] if text.strip() else ""
        return not (last.startswith(tuple(e.__name__ for e in self.NOISE) + ("asyncio.exceptions.CancelledError",))
                    or "timeout graceful shutdown exceeded" in text)


def _lan_addresses() -> list[str]:
    """這台電腦在區域網路上的 IPv4 位址（給其他裝置連線用）。"""
    import socket
    found = []
    try:
        # 不會真的送出封包，只是讓系統挑出對外的網路介面。
        with socket.socket(socket.AF_INET, socket.SOCK_DGRAM) as sock:
            sock.connect(("10.255.255.255", 1))
            found.append(sock.getsockname()[0])
    except OSError:
        pass
    try:
        for info in socket.getaddrinfo(socket.gethostname(), None, socket.AF_INET):
            ip = info[4][0]
            if not ip.startswith("127.") and ip not in found:
                found.append(ip)
    except OSError:
        pass
    return found


def main() -> None:
    import uvicorn

    parser = argparse.ArgumentParser(description="song 本機 UI")
    parser.add_argument("--port", type=int, default=8765)
    parser.add_argument("--no-browser", action="store_true")
    parser.add_argument("--lan", action="store_true",
                        help="開放區域網路裡的其他裝置連線（預設只有本機能開）")
    args = parser.parse_args()

    url = f"http://127.0.0.1:{args.port}/"
    print(f"song UI：{url}（Ctrl+C 結束）", flush=True)
    if args.lan:
        for ip in _lan_addresses():
            print(f"  區域網路：http://{ip}:{args.port}/", flush=True)
        print("  注意：同一個網路裡的人都能操作曲庫、修改歌詞與排入處理；"
              "第一次開放時 Windows 防火牆會詢問，請只允許「私人網路」。", flush=True)
    # 啟動時補抓舊下載紀錄的歌曲資訊，並同步一次輸出資料夾（例如在 UI 以外用 CLI 做好的伴唱帶）。
    threading.Thread(target=_startup, daemon=True).start()
    if not args.no_browser:
        threading.Timer(1.0, webbrowser.open, args=(url,)).start()
    # 關閉時最多等 1 秒讓連線自己結束（影片串流不會自己結束，不設的話要等很久）。
    host = "0.0.0.0" if args.lan else "127.0.0.1"
    server = uvicorn.Server(uvicorn.Config(app, host=host, port=args.port, log_level="warning",
                                           timeout_graceful_shutdown=1))
    quiet = _QuietShutdown()
    for name in ("uvicorn.error", "asyncio"):
        logging.getLogger(name).addFilter(quiet)
    try:
        server.run()
    except KeyboardInterrupt:
        pass  # 連按兩次 Ctrl+C：已經在關閉了
    print("song UI 已關閉", flush=True)


if __name__ == "__main__":
    try:
        sys.stdout.reconfigure(encoding="utf-8")
    except Exception:
        pass
    main()
