// Package library 是曲庫的結構（library.json）：巢狀資料夾、每首歌所在的資料夾與順序、全域設定。
// 行為照 v1 songtool/catalog.py（黃金測試見 library_test.go）。
//
// Order 只是同一層內的排列順序（拖曳排序用），不會顯示在畫面、資料夾名稱或輸出檔名上。
// 歌名、演唱者等歌曲資訊在各首歌的 song.json，不在這裡。
package library

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/zoosewu/kara-creator/nas/internal/pystr"
)

// Root 代表最上層（不在任何資料夾裡）。
const Root = ""

// Folder 是一個資料夾。
type Folder struct {
	ID     string
	Name   string
	Parent string // Root = 最上層
	Order  int
}

// Place 是一首歌在曲庫裡的位置。
type Place struct {
	Folder string // Root = 最上層
	Order  int
}

// Settings 是全域設定（所有歌共用）。
type Settings struct {
	SubtitleScale float64           `json:"subtitle_scale"` // 字幕大小，1 = 100%
	Fonts         map[string]string `json:"fonts"`          // 語言 → 字型檔 sha256；沒設的語言用預設字型
}

// Library 是 library.json 的內容。
type Library struct {
	Folders  map[string]*Folder
	Songs    map[string]*Place
	Settings Settings
}

// New 回傳空的曲庫。
func New() *Library {
	return &Library{Folders: map[string]*Folder{}, Songs: map[string]*Place{},
		Settings: Settings{SubtitleScale: 1, Fonts: map[string]string{}}}
}

// 錯誤。API 依種類決定 HTTP 狀態碼；訊息直接給使用者看。
var (
	ErrNotFound = errors.New("找不到")
	ErrInvalid  = errors.New("不合法")
)

func notFound(msg string) error { return fmt.Errorf("%w：%s", ErrNotFound, msg) }
func invalid(msg string) error  { return fmt.Errorf("%w：%s", ErrInvalid, msg) }

// Message 取出給使用者看的訊息（去掉種類前綴）。
func Message(err error) string {
	for _, kind := range []error{ErrNotFound, ErrInvalid} {
		if errors.Is(err, kind) {
			return err.Error()[len(kind.Error())+len("："):]
		}
	}
	return err.Error()
}

var (
	errNoFolder    = func() error { return notFound("找不到資料夾") }
	errNoSong      = func() error { return notFound("曲庫裡沒有這首歌") }
	errFolderCycle = func() error { return invalid("不能把資料夾移到自己或自己的子資料夾裡") }
	errNotSibling  = func() error { return invalid("插入位置不在同一層") }
	errOrder       = func() error { return invalid("編號必須是正整數") }
)

// ---- 查詢 ----------------------------------------------------------------------

// Children 回傳 parent 底下的資料夾，依順序、名稱排列。
func (l *Library) Children(parent string) []*Folder {
	var out []*Folder
	for _, f := range l.Folders {
		if f.Parent == parent {
			out = append(out, f)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		if out[a].Order != out[b].Order {
			return out[a].Order < out[b].Order
		}
		if out[a].Name != out[b].Name {
			return out[a].Name < out[b].Name
		}
		return out[a].ID < out[b].ID
	})
	return out
}

// SongsIn 回傳資料夾裡的歌（id），依順序排列（同號時依 id）。
func (l *Library) SongsIn(folder string) []string {
	var out []string
	for id, p := range l.Songs {
		if p.Folder == folder {
			out = append(out, id)
		}
	}
	sort.Slice(out, func(a, b int) bool {
		pa, pb := l.Songs[out[a]], l.Songs[out[b]]
		if pa.Order != pb.Order {
			return pa.Order < pb.Order
		}
		return out[a] < out[b]
	})
	return out
}

// Path 回傳由最上層到 folder 的資料夾串列。
func (l *Library) Path(folder string) []*Folder {
	var chain []*Folder
	seen := map[string]bool{}
	for folder != Root && !seen[folder] {
		f, ok := l.Folders[folder]
		if !ok {
			break
		}
		seen[folder] = true
		chain = append(chain, f)
		folder = f.Parent
	}
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	return chain
}

// Descendants 回傳 folder 底下所有的子孫資料夾。
func (l *Library) Descendants(folder string) map[string]bool {
	found := map[string]bool{}
	stack := []string{folder}
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		for _, c := range l.Children(cur) {
			if !found[c.ID] {
				found[c.ID] = true
				stack = append(stack, c.ID)
			}
		}
	}
	return found
}

func (l *Library) nextFolderOrder(parent string) int {
	n := 0
	for _, f := range l.Children(parent) {
		n = max(n, f.Order)
	}
	return n + 1
}

func (l *Library) nextSongOrder(folder string) int {
	n := 0
	for _, id := range l.SongsIn(folder) {
		n = max(n, l.Songs[id].Order)
	}
	return n + 1
}

// ---- 修改 ----------------------------------------------------------------------

func (l *Library) folder(id string) (*Folder, error) {
	if f, ok := l.Folders[id]; ok {
		return f, nil
	}
	return nil, errNoFolder()
}

func (l *Library) checkFolder(id string) error {
	if id != Root {
		if _, ok := l.Folders[id]; !ok {
			return errNoFolder()
		}
	}
	return nil
}

// NewID 產生新的資料夾 id（8 個十六進位字元）。
func NewID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// AddFolder 新增資料夾；order 為 0 時排到最後。id 為空字串時自動產生。
func (l *Library) AddFolder(id, name, parent string, order int) (*Folder, error) {
	if err := l.checkFolder(parent); err != nil {
		return nil, err
	}
	if id == "" {
		id = NewID()
	}
	name = pystr.JoinFields(name)
	if name == "" {
		name = "新資料夾"
	}
	if order == 0 {
		order = l.nextFolderOrder(parent)
	}
	f := &Folder{ID: id, Name: name, Parent: parent, Order: order}
	l.Folders[id] = f
	return f, nil
}

// FolderUpdate 是要改的欄位；nil 代表不改。
type FolderUpdate struct {
	Name   *string
	Order  *int
	Parent *string
}

// UpdateFolder 修改資料夾。和 v1 一樣，出錯前已經做的修改不會復原（呼叫端整個丟掉這份副本即可）。
func (l *Library) UpdateFolder(id string, u FolderUpdate) (*Folder, error) {
	f, err := l.folder(id)
	if err != nil {
		return nil, err
	}
	if u.Parent != nil && *u.Parent != f.Parent {
		parent := *u.Parent
		if err := l.checkFolder(parent); err != nil {
			return nil, err
		}
		if parent == id || l.Descendants(id)[parent] {
			return nil, errFolderCycle()
		}
		f.Parent = parent
		if u.Order == nil {
			for _, c := range l.Children(parent) {
				if c.ID != id && c.Order == f.Order {
					f.Order = l.nextFolderOrder(parent)
					break
				}
			}
		}
	}
	if u.Name != nil {
		if name := pystr.JoinFields(*u.Name); name != "" {
			f.Name = name
		}
	}
	if u.Order != nil {
		if *u.Order < 1 {
			return nil, errOrder()
		}
		f.Order = *u.Order
	}
	return f, nil
}

// DeleteFolder 刪除資料夾；裡面的子資料夾與歌曲移到上一層（只是整理結構，不刪任何檔案）。
func (l *Library) DeleteFolder(id string) error {
	f, err := l.folder(id)
	if err != nil {
		return err
	}
	parent := f.Parent
	for _, c := range l.Children(id) {
		if _, err := l.UpdateFolder(c.ID, FolderUpdate{Parent: &parent}); err != nil {
			return err
		}
	}
	for _, song := range l.SongsIn(id) {
		if err := l.UpdateSong(song, SongUpdate{Folder: &parent}); err != nil {
			return err
		}
	}
	delete(l.Folders, id)
	return nil
}

// EnsureSong 曲庫裡還沒有這首歌時加進 folder（不存在就放最上層）並排到最後；已存在則不動。
func (l *Library) EnsureSong(id, folder string) *Place {
	if p, ok := l.Songs[id]; ok {
		return p
	}
	if _, ok := l.Folders[folder]; !ok {
		folder = Root
	}
	p := &Place{Folder: folder, Order: l.nextSongOrder(folder)}
	l.Songs[id] = p
	return p
}

// SongUpdate 是要改的欄位；nil 代表不改。
type SongUpdate struct {
	Folder *string
	Order  *int
}

// UpdateSong 移動歌曲或改順序。
func (l *Library) UpdateSong(id string, u SongUpdate) error {
	p, ok := l.Songs[id]
	if !ok {
		return errNoSong()
	}
	if u.Folder != nil && *u.Folder != p.Folder {
		if err := l.checkFolder(*u.Folder); err != nil {
			return err
		}
		p.Folder = *u.Folder
		if u.Order == nil {
			for _, other := range l.SongsIn(p.Folder) {
				if other != id && l.Songs[other].Order == p.Order {
					p.Order = l.nextSongOrder(p.Folder)
					break
				}
			}
		}
	}
	if u.Order != nil {
		if *u.Order < 1 {
			return errOrder()
		}
		p.Order = *u.Order
	}
	return nil
}

// 拖曳的項目種類。
const (
	KindFolder = "folder"
	KindSong   = "song"
)

// Place 把資料夾或歌曲移到 parent 底下、排在 before 前面（before 為空字串表示排到最後）。
//
// 只做最少的編號調整：取代 before 的編號，後面撞號的依序往後推一號，其他編號（包括刻意留的空號）不動。
func (l *Library) Place(kind, id, parent, before string) error {
	type item struct {
		id    string
		order *int
	}
	var obj *int
	var siblings []item
	var anchor *item
	if kind == KindFolder {
		f, err := l.folder(id)
		if err != nil {
			return err
		}
		if parent != f.Parent {
			if _, err := l.UpdateFolder(id, FolderUpdate{Parent: &parent}); err != nil {
				return err
			}
		}
		obj = &f.Order
		for _, c := range l.Children(parent) {
			if c.ID != id {
				siblings = append(siblings, item{c.ID, &c.Order})
			}
		}
		if a, ok := l.Folders[before]; ok && before != "" {
			anchor = &item{a.ID, &a.Order}
		}
	} else {
		p, ok := l.Songs[id]
		if !ok {
			return errNoSong()
		}
		if parent != p.Folder {
			if err := l.UpdateSong(id, SongUpdate{Folder: &parent}); err != nil {
				return err
			}
		}
		obj = &p.Order
		for _, s := range l.SongsIn(parent) {
			if s != id {
				siblings = append(siblings, item{s, &l.Songs[s].Order})
			}
		}
		if a, ok := l.Songs[before]; ok && before != "" {
			anchor = &item{before, &a.Order}
		}
	}
	if anchor != nil {
		in := false
		for _, s := range siblings {
			in = in || s.id == anchor.id
		}
		if !in {
			return errNotSibling()
		}
	}

	if anchor == nil {
		n := 0
		for _, s := range siblings {
			n = max(n, *s.order)
		}
		*obj = n + 1
		return nil
	}
	previous := *anchor.order
	*obj = previous
	var after []item // siblings 已經依順序排好；sort.SliceStable 保持同號時的先後
	for _, s := range siblings {
		if *s.order >= previous {
			after = append(after, s)
		}
	}
	sort.SliceStable(after, func(a, b int) bool { return *after[a].order < *after[b].order })
	for _, s := range after {
		if *s.order > previous {
			break
		}
		previous++
		*s.order = previous
	}
	return nil
}

// ---- 存檔格式 ------------------------------------------------------------------

type folderJSON struct {
	ID     string  `json:"id"`
	Name   string  `json:"name"`
	Parent *string `json:"parent"`
	Order  int     `json:"order"`
}

type placeJSON struct {
	Folder *string `json:"folder"`
	Order  int     `json:"order"`
}

type fileJSON struct {
	Version  int                  `json:"version"`
	Folders  []folderJSON         `json:"folders"`
	Songs    map[string]placeJSON `json:"songs"`
	Settings Settings             `json:"settings"`
}

// Version 是 library.json 的格式版本。
const Version = 2

func ptr(s string) *string {
	if s == Root {
		return nil
	}
	return &s
}

func val(s *string) string {
	if s == nil {
		return Root
	}
	return *s
}

// MarshalJSON 寫成 library.json 的格式（資料夾依 id 排序，輸出穩定）。
func (l *Library) MarshalJSON() ([]byte, error) {
	out := fileJSON{Version: Version, Folders: []folderJSON{}, Songs: map[string]placeJSON{}, Settings: l.Settings}
	for _, f := range l.Folders {
		out.Folders = append(out.Folders, folderJSON{f.ID, f.Name, ptr(f.Parent), f.Order})
	}
	sort.Slice(out.Folders, func(a, b int) bool { return out.Folders[a].ID < out.Folders[b].ID })
	for id, p := range l.Songs {
		out.Songs[id] = placeJSON{ptr(p.Folder), p.Order}
	}
	if out.Settings.Fonts == nil {
		out.Settings.Fonts = map[string]string{}
	}
	return json.Marshal(out)
}

// UnmarshalJSON 讀 library.json。
func (l *Library) UnmarshalJSON(data []byte) error {
	var in fileJSON
	if err := json.Unmarshal(data, &in); err != nil {
		return err
	}
	if in.Version != Version {
		return fmt.Errorf("library.json 的格式版本是 %d，這個版本的程式只認得 %d", in.Version, Version)
	}
	*l = *New()
	for _, f := range in.Folders {
		l.Folders[f.ID] = &Folder{f.ID, f.Name, val(f.Parent), f.Order}
	}
	for id, p := range in.Songs {
		l.Songs[id] = &Place{val(p.Folder), p.Order}
	}
	l.Settings = in.Settings
	if l.Settings.SubtitleScale == 0 {
		l.Settings.SubtitleScale = 1
	}
	if l.Settings.Fonts == nil {
		l.Settings.Fonts = map[string]string{}
	}
	return nil
}

// Clone 回傳深複本（修改失敗時丟掉複本，原本的不受影響）。
func (l *Library) Clone() *Library {
	c := New()
	for id, f := range l.Folders {
		cp := *f
		c.Folders[id] = &cp
	}
	for id, p := range l.Songs {
		cp := *p
		c.Songs[id] = &cp
	}
	c.Settings.SubtitleScale = l.Settings.SubtitleScale
	for k, v := range l.Settings.Fonts {
		c.Settings.Fonts[k] = v
	}
	return c
}
