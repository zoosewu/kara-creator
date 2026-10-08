// Package store 管理曲庫資料夾（docs/data.md「資料夾結構」）：library.json、各首歌的 song.json。
//
// 啟動時全部讀進記憶體，之後只在修改時寫檔（不輪詢，NAS 的硬碟才能休眠）。
// 寫檔一律「寫暫存檔再改名」，中斷時不會留下半個 JSON。
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/song"
)

// 曲庫底下的資料夾與檔案。
const (
	LibraryFile = "library.json"
	SongsDir    = "songs"
	InboxDir    = "inbox"
	ExportDir   = "export"
	FontsDir    = "fonts"
	CacheDir    = "cache"
	WorkDir     = "cache/work" // 給 AI 的暫存輸入（以 sha256 命名）
)

// ErrNoLibrary 表示曲庫資料夾裡沒有 library.json（外接硬碟沒掛上，或還沒建立曲庫）。
var ErrNoLibrary = errors.New("找不到曲庫")

// ErrSongExists 表示要新增的歌已經在曲庫裡。
var ErrSongExists = errors.New("這首歌已經在曲庫裡")

// ErrNoSong 表示找不到這首歌。
var ErrNoSong = errors.New("找不到這首歌")

// Change 是一次修改，給 SSE 與狀態判斷用。
type Change struct {
	Kind string // "library" | "song"
	ID   string // 歌曲 id（Kind = song 時）
}

// Store 是開啟中的曲庫。所有方法都可以同時呼叫。
type Store struct {
	root string
	lock *os.File // 曲庫的獨佔鎖（同時只能有一個 kara-nas 寫曲庫）

	mu    sync.Mutex
	lib   *library.Library
	songs map[string]*song.Song

	// OnChange 在每次修改寫檔後呼叫（持有鎖以外的地方）；Open 之後設定。
	OnChange func(Change)
}

// Open 開啟曲庫。沒有 library.json 時：create 為 true 就建立新的曲庫，否則回傳 ErrNoLibrary
// （避免外接硬碟沒掛上時在掛載點建立一個空的曲庫）。
func Open(root string, create bool) (*Store, error) {
	s := &Store{root: root, songs: map[string]*song.Song{}}
	data, err := os.ReadFile(s.Path(LibraryFile))
	switch {
	case errors.Is(err, fs.ErrNotExist) && create:
		if err := os.MkdirAll(root, 0o755); err != nil {
			return nil, err
		}
		s.lib = library.New()
		if err := s.writeJSON(s.Path(LibraryFile), s.lib); err != nil {
			return nil, err
		}
	case errors.Is(err, fs.ErrNotExist):
		return nil, fmt.Errorf("%w：%s 沒有 %s。外接硬碟有掛上嗎？第一次使用請加 --init 建立新的曲庫",
			ErrNoLibrary, root, LibraryFile)
	case err != nil:
		return nil, err
	default:
		s.lib = library.New()
		if err := json.Unmarshal(data, s.lib); err != nil {
			return nil, fmt.Errorf("%s：%w", s.Path(LibraryFile), err)
		}
	}
	if s.lock, err = lock(root); err != nil {
		return nil, err
	}
	for _, dir := range []string{SongsDir, InboxDir, ExportDir, FontsDir, WorkDir} {
		if err := os.MkdirAll(s.Path(dir), 0o755); err != nil {
			return nil, err
		}
	}
	if err := s.loadSongs(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) loadSongs() error {
	entries, err := os.ReadDir(s.Path(SongsDir))
	if err != nil {
		return err
	}
	added := false
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		path := s.SongPath(e.Name(), song.FileRecord)
		data, err := os.ReadFile(path)
		if errors.Is(err, fs.ErrNotExist) {
			continue // 下載到一半、還沒有紀錄的資料夾
		}
		if err != nil {
			return err
		}
		var sg song.Song
		if err := json.Unmarshal(data, &sg); err != nil || sg.ID != e.Name() {
			// 壞掉的紀錄不讓整個伺服器起不來；這首歌先不顯示。
			log.Printf("略過 %s：紀錄檔讀不出來或 id 不符（%v）", path, err)
			continue
		}
		s.songs[sg.ID] = &sg
		if _, ok := s.lib.Songs[sg.ID]; !ok {
			s.lib.EnsureSong(sg.ID, library.Root)
			added = true
		}
	}
	if added {
		return s.writeJSON(s.Path(LibraryFile), s.lib)
	}
	return nil
}

// Close 放開曲庫的鎖（程式結束時也會自動放開）。
func (s *Store) Close() {
	if s.lock != nil {
		s.lock.Close()
		s.lock = nil
	}
}

// Root 是曲庫根目錄。
func (s *Store) Root() string { return s.root }

// Path 回傳曲庫底下的路徑。
func (s *Store) Path(rel ...string) string { return filepath.Join(append([]string{s.root}, rel...)...) }

// SongPath 回傳 songs/<id>/ 底下的路徑。
func (s *Store) SongPath(id string, rel ...string) string {
	return filepath.Join(append([]string{s.root, SongsDir, id}, rel...)...)
}

// ---- 讀取（回傳複本，可以隨意修改）--------------------------------------------

// Library 回傳曲庫結構的複本。
func (s *Store) Library() *library.Library {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lib.Clone()
}

// Song 回傳一首歌的紀錄複本。
func (s *Store) Song(id string) (*song.Song, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sg, ok := s.songs[id]
	if !ok {
		return nil, false
	}
	return clone(sg), true
}

// SongIDs 回傳所有歌的 id（排序過）。
func (s *Store) SongIDs() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	ids := make([]string, 0, len(s.songs))
	for id := range s.songs {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// ---- 修改 ----------------------------------------------------------------------

// EditLibrary 修改曲庫結構：fn 拿到複本，回傳 nil 才寫檔並取代；出錯時什麼都不變。
func (s *Store) EditLibrary(fn func(*library.Library) error) error {
	s.mu.Lock()
	next := s.lib.Clone()
	if err := fn(next); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.writeJSON(s.Path(LibraryFile), next); err != nil {
		s.mu.Unlock()
		return err
	}
	s.lib = next
	s.mu.Unlock()
	s.notify(Change{Kind: "library"})
	return nil
}

// EditSong 修改一首歌的紀錄：fn 拿到複本，回傳 nil 才寫檔並取代。
func (s *Store) EditSong(id string, fn func(*song.Song) error) error {
	s.mu.Lock()
	cur, ok := s.songs[id]
	if !ok {
		s.mu.Unlock()
		return ErrNoSong
	}
	next := clone(cur)
	if err := fn(next); err != nil {
		s.mu.Unlock()
		return err
	}
	next.ID, next.Version = id, song.Version
	if err := s.writeJSON(s.SongPath(id, song.FileRecord), next); err != nil {
		s.mu.Unlock()
		return err
	}
	s.songs[id] = next
	s.mu.Unlock()
	s.notify(Change{Kind: "song", ID: id})
	return nil
}

// AddSong 新增一首歌（寫 song.json），並放進曲庫的 folder（不存在就放最上層）。
func (s *Store) AddSong(sg *song.Song, folder string) error {
	s.mu.Lock()
	if _, ok := s.songs[sg.ID]; ok {
		s.mu.Unlock()
		return ErrSongExists
	}
	sg = clone(sg)
	sg.Version = song.Version
	if err := os.MkdirAll(s.SongPath(sg.ID), 0o755); err != nil {
		s.mu.Unlock()
		return err
	}
	if err := s.writeJSON(s.SongPath(sg.ID, song.FileRecord), sg); err != nil {
		s.mu.Unlock()
		return err
	}
	s.songs[sg.ID] = sg
	next := s.lib.Clone()
	next.EnsureSong(sg.ID, folder)
	err := s.writeJSON(s.Path(LibraryFile), next)
	if err == nil {
		s.lib = next
	}
	s.mu.Unlock()
	s.notify(Change{Kind: "song", ID: sg.ID})
	s.notify(Change{Kind: "library"})
	return err
}

func (s *Store) notify(c Change) {
	if s.OnChange != nil {
		s.OnChange(c)
	}
}

func clone(sg *song.Song) *song.Song {
	data, err := json.Marshal(sg)
	if err != nil {
		panic(err)
	}
	var out song.Song
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return &out
}

func (s *Store) writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return WriteFile(path, append(data, '\n'))
}

// WriteFile 原子寫入：寫到同一個資料夾的暫存檔，再改名取代。
func WriteFile(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // 改名成功後這個檔案已經不存在，Remove 失敗無妨
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
