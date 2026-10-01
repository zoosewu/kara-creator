"""REST API v1 的資料模型：這個檔案就是 API 規格的資料結構。

OpenAPI（Swagger）規格由 FastAPI 從這些模型與 api_v1.py 的端點自動產生：
執行中可以開 /docs（Swagger UI）、/redoc、/openapi.json；
不啟動伺服器時用 `python scripts/openapi.py` 輸出成 docs/openapi.json。
欄位的說明（description）與範例（examples）會直接出現在規格裡，修改時請一併維護。
"""
from __future__ import annotations

from typing import Literal

from pydantic import BaseModel, ConfigDict, Field

SongStep = Literal["separate", "karaoke", "check", "retime"]
LyricsFormat = Literal["file", "annotated", "plain"]
Language = Literal["", "zh", "nan", "yue", "ja", "en"]
StageStatus = Literal["done", "pending", "outdated", "missing", "no_lyrics"]
JobStatus = Literal["queued", "running", "done", "failed", "cancelled"]

SONG_ID = Field(description="歌曲 id：影片 id（YouTube 的 11 碼），手動放入的檔案是 local-xxxxxxxx",
                examples=["jNQXAC9IVRw"])


# ---- 共用 ----------------------------------------------------------------------

class Error(BaseModel):
    """錯誤回應。"""
    detail: str = Field(description="給人看的錯誤說明（繁體中文）", examples=["找不到歌曲：abc"])


# ---- 歌曲 ----------------------------------------------------------------------

class CustomInfo(BaseModel):
    title: str = Field(description="手動設定的歌名；空字串 = 自動辨識")
    artist: str = Field(description="手動設定的演唱者；空字串 = 自動辨識")


class Source(BaseModel):
    url: str | None = Field(description="下載時的影片連結；手動放入的檔案為 null")
    link: str | None = Field(description="手動放入的影片補上的原始連結（重做時用它重新下載）")
    title: str = Field(description="原始影片標題")
    duration: float | None = Field(description="長度（秒）")
    uploader: str | None = Field(description="上傳者 / 頻道")
    mode: Literal["video", "audio"] = Field(description="影片或純音訊")


class Stages(BaseModel):
    """各處理步驟的狀態。"""
    download: StageStatus
    separate: StageStatus = Field(description="去人聲")
    lyrics: StageStatus = Field(description="歌詞：done 已有 / missing 沒有")
    karaoke: StageStatus = Field(description="伴唱帶：done 最新 / outdated 需更新 / pending 未製作 / no_lyrics 缺歌詞")


class QaSummary(BaseModel):
    checked: bool = Field(description="是否做過對時檢查（且對時之後沒有變動）")
    wrong: int = Field(description="可能不準的句數")
    suspect: int = Field(description="待確認的句數")


class ExportInfo(BaseModel):
    path: str = Field(description="成品在輸出資料夾裡的相對路徑", examples=["日文/緑黄色社会 - 花になって.mp4"])
    exists: bool = Field(description="成品是否已經輸出")


class Translation(BaseModel):
    burn: bool = Field(description="歌詞有中文翻譯時是否燒進伴唱帶")
    lines: int = Field(description="有翻譯的句數（歌詞檔裡每句下一行的「> 翻譯」）")


class Approval(BaseModel):
    status: Literal["approved", "stale"] | None = Field(
        description="approved：已確認成品沒問題；stale：確認後成品有變動，需重新確認；null：沒確認過")
    at: str | None = Field(description="確認時間（ISO 8601）")


class Song(BaseModel):
    """一首歌。"""
    id: str = SONG_ID
    title: str = Field(description="實際使用的歌名（手動設定 > 歌詞檔 > 自動辨識）")
    artist: str = Field(description="實際使用的演唱者")
    language: Language | None = Field(description="手動指定的演唱語言；null = 依歌詞文字判斷")
    folder: str | None = Field(description="所在資料夾 id；null = 最上層")
    order: int = Field(description="在資料夾裡的排列順序")
    note: str = Field(description="備註：顯示在開頭標題畫面的第三行（演唱者下面）；空字串 = 沒有")
    translation: Translation
    custom: CustomInfo
    source: Source
    stages: Stages
    qa: QaSummary
    export: ExportInfo
    job: str | None = Field(description="正在處理這首歌的工作 id")
    approval: Approval


class Media(BaseModel):
    key: Literal["karaoke", "karaoke_original", "source", "instrumental", "vocals"]
    label: str = Field(examples=["伴唱帶"])
    desc: str
    url: str = Field(description="檔案網址（相對於伺服器）", examples=["/media/karaoke/..._karaoke.mp4"])


class SongDetail(Song):
    media: list[Media] = Field(description="可以播放 / 下載的檔案，第一個是最終成品")


class NewSong(BaseModel):
    url: str = Field(description="影片網址（YouTube 等 yt-dlp 支援的網站）", examples=["https://www.youtube.com/watch?v=jNQXAC9IVRw"])
    folder: str | None = Field(None, description="放進哪個資料夾；null = 最上層")
    audio_only: bool = Field(False, description="只下載音訊")
    lyrics: str | None = Field(None, description="歌詞（存檔格式），下載後存起來")
    make: bool = Field(True, description="true：下載後接著去人聲並製作伴唱帶；false：只下載")


class SongPatch(BaseModel):
    """只會修改有給的欄位。"""
    model_config = ConfigDict(json_schema_extra={"examples": [{"title": "花になって", "artist": "緑黄色社会"},
                                                              {"approved": True}]})
    title: str | None = Field(None, description="空字串 = 還原自動辨識")
    artist: str | None = Field(None, description="空字串 = 還原自動辨識")
    language: Language | None = Field(None, description="空字串 = 依歌詞文字判斷；nan 台語、yue 粵語用 CTC 對時")
    folder: str | None = Field(None, description="資料夾 id；null = 最上層（有給這個欄位才會移動）")
    approved: bool | None = Field(None, description="true：確認目前的成品沒問題（伴唱帶要已完成）；false：取消確認")
    link: str | None = Field(None, description="手動放入的影片補上原始連結；空字串 = 清除。用網址下載的歌不能改（回 409）")
    note: str | None = Field(None, description="備註（開頭標題畫面第三行，例如「作詞：○○／作曲：○○」）；空字串 = 清除")
    translation: bool | None = Field(None, description="是否把中文翻譯燒進伴唱帶（改了只重新燒錄，不重新對時）")


# ---- 歌詞與時間 ----------------------------------------------------------------

class Lyrics(BaseModel):
    format: LyricsFormat = Field(description="file：存檔格式（只含手動讀音）/ annotated：含所有讀音的標註原文 / plain：只有歌詞")
    text: str = Field(examples=["# title: 春の約束\n[女] 窓の外に 小さな花が咲いた\n"])
    exists: bool = Field(description="歌詞檔是否存在")


class LyricsBody(BaseModel):
    text: str = Field(description="一行一句；[男] [女] [合] 標演唱者；漢字{よみ} 或 {原字|よみ} 標讀音；"
                                  "下一行「> 翻譯」是這句的中文翻譯")
    format: LyricsFormat = Field("file", description="plain 會保留沒改到的句子原本的演唱者與讀音")


class Word(BaseModel):
    text: str
    start: float = Field(description="開始（秒）")
    end: float = Field(description="結束（秒）")


class TimingLine(BaseModel):
    index: int = Field(description="第幾句（從 0 起算）")
    text: str
    start: float
    end: float
    words: list[Word] = Field(description="逐字（日文為音拍、中文為字、英文為單字）的時間")


class Timing(BaseModel):
    lines: list[TimingLine]
    adjustments: list[dict] = Field(description="手動調整與 AI 重對的紀錄")


class LinePatch(BaseModel):
    """start 與 delta 擇一。只移這一句；整段偏掉請用 retime 工作。"""
    model_config = ConfigDict(json_schema_extra={"examples": [{"delta": -0.3}, {"start": 12.5}]})
    start: float | None = Field(None, description="新的開始時間（秒）")
    delta: float | None = Field(None, description="移動量（秒，負數往前）")


class QaLine(BaseModel):
    index: int
    text: str
    start: float
    end: float
    status: Literal["wrong", "suspect"] = Field(description="wrong 可能不準 / suspect 待確認")
    reasons: list[str] = Field(description="原因說明")
    offset: float | None = Field(description="和獨立聽寫的時間差（秒）")
    match: float = Field(description="和聽寫對上的比例")


class QaResult(BaseModel):
    checked: bool
    checked_at: str | None = None
    lines: list[QaLine] = Field(description="只列出有疑慮的句子")


# ---- 工作 ----------------------------------------------------------------------

class Job(BaseModel):
    """一件背景工作（下載、去人聲、製作伴唱帶、檢查對時、AI 重對，或收尾）。"""
    id: str = Field(examples=["3f2a9c1d"])
    song: str | None = Field(description="對應的歌曲 id（下載完成前為 null）")
    title: str
    steps: list[str] = Field(examples=[["download", "separate", "karaoke"]])
    lane: Literal["download", "process", "system"] = Field(description="所在佇列：download 下載 / process AI 處理 / system 收尾")
    status: JobStatus
    stage: str = Field(description="目前在做的步驟（給人看的）")
    progress: float | None = Field(description="0–1；無法估計時為 null")
    error: str | None
    url: str | None
    created: float = Field(description="建立時間（Unix 秒）")
    started: float | None
    finished: float | None


class JobLog(BaseModel):
    lines: list[str]
    next: int = Field(description="下次從這個 offset 繼續讀")


class SongJob(BaseModel):
    model_config = ConfigDict(json_schema_extra={"examples": [{"steps": ["karaoke"]},
                                                              {"steps": ["retime"], "line": 12, "mode": "from"}]})
    steps: list[SongStep] = Field(description="separate 去人聲 / karaoke 製作伴唱帶 / check 檢查對時 / retime AI 重對")
    force: bool = Field(False, description="已完成的也重做（去人聲重新分離、整首重新對時並覆蓋手動改過的字幕）")
    realign: bool = Field(False, description="製作伴唱帶時重新對時")
    line: int | None = Field(None, description="retime：以第幾句（從 0 起算）目前的開頭為準")
    mode: Literal["from", "line"] | None = Field(None, description="retime：from 這句及之後全部 / line 只重對這句")


# ---- 全域設定 ------------------------------------------------------------------

class Settings(BaseModel):
    """全域設定，套用到所有歌。"""
    subtitle_scale: float = Field(description="字幕大小（相對預設的倍數，0.6–1.6；1.0 = 預設）", examples=[1.2])
    subtitle_ratio: float = Field(description="實際的字幕字高 / 畫面高（唯讀）")


class SettingsPatch(BaseModel):
    """只改有給的欄位。改了字幕大小，已經做好的伴唱帶會顯示需更新（重新燒錄、不重新對時）。"""
    subtitle_scale: float | None = Field(None, ge=0.6, le=1.6, description="字幕大小（0.6–1.6）")


# ---- 資料夾 --------------------------------------------------------------------

class Folder(BaseModel):
    id: str
    name: str
    parent: str | None = Field(description="上層資料夾 id；null = 最上層")
    order: int
    path: list[str] = Field(description="從最上層到自己的資料夾 id")
    path_label: str = Field(examples=["日文 › 動畫"])
    songs: int = Field(description="直接放在這裡的歌數")
    folders: int = Field(description="子資料夾數")


class NewFolder(BaseModel):
    name: str
    parent: str | None = Field(None, description="上層資料夾 id；null = 最上層")


class FolderPatch(BaseModel):
    name: str | None = None
    parent: str | None = Field(None, description="上層資料夾 id；null = 最上層（有給這個欄位才會移動）")
