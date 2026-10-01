"""曲庫：可巢狀的資料夾、排列順序、歌名與演唱者。

這是「整理」用的資料，存在 output/library.json；下載、去人聲、字幕的實體資料夾
不會因此搬動（它們靠影片 id 對應，搬動會破壞「已處理」的判斷）。
依曲庫整理好的成品由 export.py 另外輸出。

number 只是同一層內的排列順序（拖曳排序用），不會顯示在畫面、資料夾名稱或輸出檔名上。
code() 把各層順序串起來，只用來排序整個曲庫。
"""
from __future__ import annotations

import threading
import uuid
from contextlib import contextmanager
from dataclasses import asdict, dataclass
from typing import Iterator

from . import config, manifest, titles
from .download import Download

_lock = threading.RLock()
_UNSET = object()


@dataclass
class Folder:
    id: str
    name: str
    number: int
    parent: str | None = None

    @property
    def label(self) -> str:
        return self.name


@dataclass
class Song:
    key: str
    folder: str | None = None
    number: int = 1
    title: str = ""       # 空字串代表沿用歌詞檔的 # title，再沒有就用影片標題
    artist: str = ""
    language: str = ""    # 演唱語言（LANGUAGES 的鍵）；空字串代表依歌詞文字判斷


# 可以手動指定的演唱語言。台語、粵語的歌詞文字和國語分不出來，只能手動指定。
LANGUAGES = {"zh": "國語", "nan": "台語", "yue": "粵語", "ja": "日文", "en": "英文"}


def song_key(item: Download) -> str:
    """用網站與影片 id 當鍵：重新下載、影片改標題都還對得上。"""
    if item.info.get("id"):
        return f"{item.info.get('extractor') or 'url'}:{item.info['id']}"
    return f"name:{item.name}"


class Catalog:
    def __init__(self, folders: dict[str, Folder] | None = None, songs: dict[str, Song] | None = None):
        self.folders = folders or {}
        self.songs = songs or {}
        self.dirty = False

    # ---- 查詢 ----

    def children(self, parent: str | None) -> list[Folder]:
        return sorted((f for f in self.folders.values() if f.parent == parent),
                      key=lambda f: (f.number, f.name))

    def songs_in(self, folder: str | None) -> list[Song]:
        return sorted((s for s in self.songs.values() if s.folder == folder), key=lambda s: s.number)

    def path(self, folder_id: str | None) -> list[Folder]:
        """由最上層到 folder_id 的資料夾串列。"""
        chain, seen = [], set()
        while folder_id and folder_id in self.folders and folder_id not in seen:
            seen.add(folder_id)
            chain.append(self.folders[folder_id])
            folder_id = self.folders[folder_id].parent
        return chain[::-1]

    def folder_sort_key(self, folder_id: str | None) -> tuple:
        """資料夾在整個曲庫中的排序位置（各層的順序，再以名稱區分同順序者）。"""
        return tuple((f.number, f.name) for f in self.path(folder_id))

    def sort_key(self, song: Song) -> tuple:
        """歌曲在整個曲庫中的排序位置：先依所在資料夾，再依同層順序。"""
        return self.folder_sort_key(song.folder) + ((song.number, song.key),)

    def descendants(self, folder_id: str) -> set[str]:
        found, stack = set(), [folder_id]
        while stack:
            for child in self.children(stack.pop()):
                if child.id not in found:
                    found.add(child.id)
                    stack.append(child.id)
        return found

    def next_folder_number(self, parent: str | None) -> int:
        return max((f.number for f in self.children(parent)), default=0) + 1

    def next_song_number(self, folder: str | None) -> int:
        return max((s.number for s in self.songs_in(folder)), default=0) + 1

    # ---- 修改 ----

    def add_folder(self, name: str, parent: str | None = None, number: int | None = None) -> Folder:
        self._check_folder(parent)
        folder = Folder(uuid.uuid4().hex[:8], _clean(name) or "新資料夾",
                        number or self.next_folder_number(parent), parent)
        self.folders[folder.id] = folder
        self.dirty = True
        return folder

    def update_folder(self, folder_id: str, *, name=None, number=None, parent=_UNSET) -> Folder:
        folder = self._folder(folder_id)
        if parent is not _UNSET and parent != folder.parent:
            self._check_folder(parent)
            if parent == folder_id or parent in self.descendants(folder_id):
                raise ValueError("不能把資料夾移到自己或自己的子資料夾裡")
            folder.parent = parent
            if number is None and any(f.number == folder.number for f in self.children(parent) if f.id != folder_id):
                folder.number = self.next_folder_number(parent)
        if name is not None:
            folder.name = _clean(name) or folder.name
        if number is not None:
            folder.number = _positive(number)
        self.dirty = True
        return folder

    def delete_folder(self, folder_id: str) -> None:
        """刪除資料夾；裡面的子資料夾與歌曲移到上一層（只是整理結構，不刪任何檔案）。"""
        folder = self._folder(folder_id)
        for child in self.children(folder_id):
            self.update_folder(child.id, parent=folder.parent)
        for song in self.songs_in(folder_id):
            self.update_song(song.key, folder=folder.parent)
        del self.folders[folder_id]
        self.dirty = True

    def ensure_song(self, key: str, folder: str | None = None) -> Song:
        """曲庫裡還沒有這首歌時加入 folder，並給下一個編號；已存在則不動。"""
        if key not in self.songs:
            if folder not in self.folders:
                folder = None
            self.songs[key] = Song(key, folder, self.next_song_number(folder))
            self.dirty = True
        return self.songs[key]

    def update_song(self, key: str, *, folder=_UNSET, number=None, title=None, artist=None,
                    language=None) -> Song:
        song = self.songs.get(key)
        if song is None:
            raise KeyError("曲庫裡沒有這首歌")
        if folder is not _UNSET and folder != song.folder:
            self._check_folder(folder)
            song.folder = folder
            if number is None and any(s.number == song.number for s in self.songs_in(folder) if s.key != key):
                song.number = self.next_song_number(folder)
        if number is not None:
            song.number = _positive(number)
        if title is not None:
            song.title = _clean(title)
        if artist is not None:
            song.artist = _clean(artist)
        if language is not None:
            if language and language not in LANGUAGES:
                raise ValueError(f"不支援的語言：{language}")
            song.language = language
        self.dirty = True
        return song

    def place(self, kind: str, item_id: str, parent: str | None, before: str | None) -> None:
        """把資料夾或歌曲移到 parent 底下、排在 before 前面（None 表示排到最後）。

        只做最少的編號調整：取代 before 的編號，後面撞號的依序往後推一號，
        其他編號（包括刻意留的空號）不動。
        """
        if kind == "folder":
            if parent != self._folder(item_id).parent:
                self.update_folder(item_id, parent=parent)
            obj = self.folders[item_id]
            siblings = [f for f in self.children(parent) if f.id != item_id]
            anchor = self.folders.get(before) if before else None
        else:
            song = self.songs.get(item_id)
            if song is None:
                raise KeyError("曲庫裡沒有這首歌")
            if parent != song.folder:
                self.update_song(item_id, folder=parent)
            obj = song
            siblings = [s for s in self.songs_in(parent) if s.key != item_id]
            anchor = self.songs.get(before) if before else None
        if anchor is not None and anchor not in siblings:
            raise ValueError("插入位置不在同一層")

        if anchor is None:
            obj.number = max((s.number for s in siblings), default=0) + 1
        else:
            obj.number = previous = anchor.number
            for sibling in sorted((s for s in siblings if s.number >= anchor.number), key=lambda s: s.number):
                if sibling.number > previous:
                    break
                sibling.number = previous = previous + 1
        self.dirty = True

    def _folder(self, folder_id: str) -> Folder:
        if folder_id not in self.folders:
            raise KeyError("找不到資料夾")
        return self.folders[folder_id]

    def _check_folder(self, folder_id: str | None) -> None:
        if folder_id is not None and folder_id not in self.folders:
            raise KeyError("找不到資料夾")

    # ---- 存取 ----

    def to_dict(self) -> dict:
        return {
            "version": 1,
            "folders": [asdict(f) for f in sorted(self.folders.values(), key=lambda f: f.id)],
            "songs": {k: {n: v for n, v in asdict(s).items() if n != "key"}
                      for k, s in sorted(self.songs.items())},
        }

    @classmethod
    def from_dict(cls, data: dict) -> Catalog:
        folders = {f["id"]: Folder(f["id"], f["name"], int(f["number"]), f.get("parent"))
                   for f in data.get("folders", [])}
        songs = {k: Song(k, v.get("folder"), int(v.get("number", 1)), v.get("title", ""), v.get("artist", ""),
                         v.get("language", ""))
                 for k, v in data.get("songs", {}).items()}
        return cls(folders, songs)


def load() -> Catalog:
    data = manifest.read(config.LIBRARY_FILE)
    return Catalog.from_dict(data) if data else Catalog()


@contextmanager
def edit() -> Iterator[Catalog]:
    """讀出曲庫、修改、有變動才寫回；整段期間持有鎖，避免 UI 與背景工作互相覆蓋。"""
    with _lock:
        cat = load()
        yield cat
        if cat.dirty:
            manifest.write(config.LIBRARY_FILE, cat.to_dict())


def include(items: list[Download]) -> Catalog:
    """確保每首下載的歌都在曲庫裡（還沒整理的放在最上層），回傳最新的曲庫。"""
    with edit() as cat:
        # 依下載時間給號，先下載的歌編號較前面。
        for item in sorted(items, key=lambda i: i.info.get("downloaded_at") or ""):
            cat.ensure_song(song_key(item))
    return cat


def display_info(item: Download, song: Song | None, lyrics_meta: dict | None = None) -> tuple[str, str]:
    """實際使用的歌名與演唱者：
    曲庫手動設定 > 歌詞檔的 # title / # artist > 自動辨識（titles.guess）。
    自動辨識不會寫進曲庫，所以永遠不會蓋掉手動設定；手動欄位清空就回到自動辨識。"""
    meta = lyrics_meta or {}
    auto = titles.guess(item.info)
    title = (song.title if song else "") or meta.get("title") or auto.title
    artist = (song.artist if song else "") or meta.get("artist") or auto.artist
    return title, artist


def _clean(text: str) -> str:
    return " ".join(str(text).split())


def _positive(number) -> int:
    value = int(number)
    if value < 1:
        raise ValueError("編號必須是正整數")
    return value
