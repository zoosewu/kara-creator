// Package export 把完成的伴唱帶依曲庫整理輸出到 <library>/export/（v1 songtool/export.py）。
//
//	export/日文/虛構歌手/虛構歌手 - 自己編的歌.mp4
//
// 資料夾結構與曲庫相同（只用資料夾名稱），檔名是卡拉 OK 軟體慣用的「歌手 - 歌名」（沒有歌手就只有歌名）。
// 同一個資料夾裡歌手與歌名都相同時，依曲庫順序在後面的加上 (2)、(3)…。
// 放置方式依序嘗試 clone（不佔空間、各自獨立）→ 硬連結 → 複製（Q2）。
// 只管理自己放進去的檔案（記在 export/.export.json），不會動到使用者另外放的東西。
// 成品需更新時照樣匯出舊的那份，重燒完再換成新的。
package export

import (
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/planner"
	"github.com/zoosewu/kara-creator/nas/internal/pystr"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
)

// Record 是匯出工具自己管理的檔案清單。
const Record = ".export.json"

var (
	illegal = strings.NewReplacer(`<`, "＿", `>`, "＿", `:`, "＿", `"`, "＿", `/`, "＿", `\`, "＿", `|`, "＿", `?`, "＿", `*`, "＿")
	control = regexp.MustCompile(`[\x00-\x1f]`)
)

// FileName 是「歌手 - 歌名.mp4」；同名的第 2 首以後加上「 (2)」。
func FileName(title, artist string, copy int) string {
	name := pystr.Strip(title)
	if a := pystr.Strip(artist); a != "" {
		name = a + " - " + pystr.Strip(title)
	}
	name = strings.TrimRight(control.ReplaceAllString(illegal.Replace(name), ""), " .")
	if r := []rune(name); len(r) > 150 {
		name = string(r[:150])
	}
	if copy > 1 {
		return name + " (" + strconv.Itoa(copy) + ").mp4"
	}
	return name + ".mp4"
}

func folderName(name string) string {
	if n := strings.TrimRight(illegal.Replace(name), " ."); n != "" {
		return n
	}
	return "_"
}

// Song 是一首歌在匯出時需要的資料。
type Song struct {
	ID     string
	Title  string // 實際使用的歌名與演唱者
	Artist string
	Video  string // 伴唱帶成品的路徑；還沒做好時為空
}

type sortKey struct {
	folders [][2]any // 各層 (順序, 名稱)
	order   int
	id      string
}

func less(a, b sortKey) bool {
	for i := 0; i < len(a.folders) && i < len(b.folders); i++ {
		x, y := a.folders[i], b.folders[i]
		if x[0].(int) != y[0].(int) {
			return x[0].(int) < y[0].(int)
		}
		if x[1].(string) != y[1].(string) {
			return x[1].(string) < y[1].(string)
		}
	}
	if len(a.folders) != len(b.folders) {
		return len(a.folders) < len(b.folders)
	}
	if a.order != b.order {
		return a.order < b.order
	}
	return a.id < b.id
}

// Names 回傳每首歌在 export/ 裡的相對路徑（不論伴唱帶做好了沒）。曲庫中排前面的先取得原始檔名。
func Names(lib *library.Library, songs []Song) map[string]string {
	type entry struct {
		key    sortKey
		folder string
		s      Song
	}
	var entries []entry
	for _, s := range songs {
		place, ok := lib.Songs[s.ID]
		if !ok {
			continue
		}
		var key sortKey
		var parts []string
		for _, f := range lib.Path(place.Folder) {
			key.folders = append(key.folders, [2]any{f.Order, f.Name})
			parts = append(parts, folderName(f.Name))
		}
		key.order, key.id = place.Order, s.ID
		entries = append(entries, entry{key, path.Join(parts...), s})
	}
	sort.Slice(entries, func(a, b int) bool { return less(entries[a].key, entries[b].key) })
	names := map[string]string{}
	copies := map[[2]string]int{}
	for _, e := range entries {
		slot := [2]string{e.folder, pystr.Casefold(FileName(e.s.Title, e.s.Artist, 1))}
		copies[slot]++
		names[e.s.ID] = path.Join(e.folder, FileName(e.s.Title, e.s.Artist, copies[slot]))
	}
	return names
}

// Result 是一次同步的結果。
type Result struct {
	Added   []string // 新放的（含更新）
	Removed []string
	Kept    int
	Skipped []string // 目的地已有不是我們放的同名檔
	Method  string   // 最近一次放置用的方式：clone / link / copy
}

// Exporter 同步 export/。
type Exporter struct {
	Root string // export/ 的路徑
	mu   sync.Mutex
}

// Sync 讓 export/ 和目前的曲庫一致：新增完成的歌、改名或換資料夾的歌移到新位置、重新製作過的歌更新成新檔。
func (x *Exporter) Sync(lib *library.Library, songs []Song) (Result, error) {
	x.mu.Lock()
	defer x.mu.Unlock()
	names := Names(lib, songs)
	wanted := map[string]string{}
	for _, s := range songs {
		if rel, ok := names[s.ID]; ok && s.Video != "" {
			if _, err := os.Stat(s.Video); err == nil {
				wanted[rel] = s.Video
			}
		}
	}
	managed, err := x.readRecord()
	if err != nil {
		return Result{}, err
	}
	var res Result
	rels := make([]string, 0, len(wanted))
	for rel := range wanted {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		src, dest := wanted[rel], filepath.Join(x.Root, filepath.FromSlash(rel))
		if _, err := os.Lstat(dest); err == nil {
			if same(dest, src) {
				res.Kept++
				continue
			}
			if !managed[rel] {
				res.Skipped = append(res.Skipped, rel)
				continue
			}
		}
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return res, err
		}
		method, err := place(src, dest)
		if err != nil {
			return res, err
		}
		res.Method = method
		res.Added = append(res.Added, rel)
	}
	var old []string
	for rel := range managed {
		if _, ok := wanted[rel]; !ok {
			old = append(old, rel)
		}
	}
	sort.Strings(old)
	for _, rel := range old {
		p := filepath.Join(x.Root, filepath.FromSlash(rel))
		_ = os.Remove(p)
		res.Removed = append(res.Removed, rel)
		prune(filepath.Dir(p), x.Root)
	}
	placed := map[string]bool{}
	for rel := range wanted {
		placed[rel] = true
	}
	for _, rel := range res.Skipped {
		delete(placed, rel)
	}
	if len(placed) > 0 || len(managed) > 0 {
		if err := x.writeRecord(placed); err != nil {
			return res, err
		}
	}
	return res, nil
}

type record struct {
	Files []string `json:"files"`
}

func (x *Exporter) readRecord() (map[string]bool, error) {
	out := map[string]bool{}
	data, err := os.ReadFile(filepath.Join(x.Root, Record))
	if errors.Is(err, fs.ErrNotExist) {
		return out, nil
	}
	if err != nil {
		return nil, err
	}
	var r record
	if err := json.Unmarshal(data, &r); err != nil {
		return out, nil // 壞掉的清單當成空的（只會讓舊檔不被刪，不會刪到使用者的檔案）
	}
	for _, f := range r.Files {
		out[f] = true
	}
	return out, nil
}

func (x *Exporter) writeRecord(files map[string]bool) error {
	r := record{Files: []string{}}
	for f := range files {
		r.Files = append(r.Files, f)
	}
	sort.Strings(r.Files)
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(x.Root, 0o755); err != nil {
		return err
	}
	return store.WriteFile(filepath.Join(x.Root, Record), append(data, '\n'))
}

// same：硬連結到同一個檔案，或大小與修改時間都相同（clone 與複製會保留修改時間）。
func same(dest, src string) bool {
	a, err := os.Stat(dest)
	if err != nil {
		return false
	}
	b, err := os.Stat(src)
	if err != nil {
		return false
	}
	return os.SameFile(a, b) || a.Size() == b.Size() && a.ModTime().Unix() == b.ModTime().Unix()
}

// place 依序嘗試 clone → 硬連結 → 複製。目的地已有檔案時先放到暫存名稱再取代，播放軟體不會讀到一半的檔案。
func place(src, dest string) (string, error) {
	tmp := dest + ".kara-tmp"
	_ = os.Remove(tmp)
	method := "clone"
	err := cloneFile(src, tmp)
	if err != nil {
		method = "link"
		err = os.Link(src, tmp)
	}
	if err != nil {
		method = "copy"
		err = copyFile(src, tmp)
	}
	if err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	if method != "link" {
		if st, err := os.Stat(src); err == nil {
			_ = os.Chtimes(tmp, st.ModTime(), st.ModTime())
		}
	}
	if err := os.Rename(tmp, dest); err != nil {
		_ = os.Remove(tmp)
		return "", err
	}
	return method, nil
}

func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// prune 往上刪掉變成空的資料夾，直到 export 根目錄為止。
func prune(dir, root string) {
	for dir != root && strings.HasPrefix(dir, root) {
		if err := os.Remove(dir); err != nil { // 不是空的就刪不掉
			return
		}
		dir = filepath.Dir(dir)
	}
}

// Collect 從曲庫收集每首歌的歌名、演唱者與伴唱帶成品（只匯出 instrumental 成品）。
func Collect(st *store.Store) []Song {
	var out []Song
	for _, id := range st.SongIDs() {
		sg, ok := st.Song(id)
		if !ok {
			continue
		}
		var meta map[string]string
		if doc, err := planner.ReadLyrics(st, id); err == nil && doc != nil {
			meta = doc.Meta
		}
		title, artist, _ := planner.Display(sg, meta)
		s := Song{ID: id, Title: title, Artist: artist}
		if rec := sg.Stages.Render[song.TargetInstrumental]; rec != nil && rec.Video.Name != "" {
			s.Video = st.SongPath(id, rec.Video.Name)
		}
		out = append(out, s)
	}
	return out
}
