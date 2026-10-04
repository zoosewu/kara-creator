// Package fonts 是字型目錄（docs/v2/data.md「字型」）：掃描 <library>/fonts/ 的字型檔，
// 讀出每個字型（含 .ttc 裡的每一個）的名稱與粗細，給設定選擇、給 AI worker 下載、給前端預覽。
//
// 一個字型用「檔案 sha256:第幾個字型」識別（ID）；.ttc 一個檔案裡有好幾個字型（例如 Noto Sans CJK 的 JP、TC、KR）。
package fonts

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"unicode/utf16"

	"github.com/zoosewu/kara-creator/nas/internal/store"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// Font 是一個字型。
type Font struct {
	ID       string `json:"id"`     // sha256:index
	SHA256   string `json:"sha256"` // 字型檔的 sha256
	Index    int    `json:"index"`  // .ttc 裡第幾個
	File     string `json:"file"`   // 字型資料夾底下的相對路徑
	Family   string `json:"family"` // ASS 的 Fontname（name table 的 family，nameID 1）
	FullName string `json:"full_name"`
	Weight   int    `json:"weight"` // 400 = 一般、700 = 粗體
	Default  bool   `json:"default"`
}

// Proto 轉成 worker 協定用的字型。
func (f Font) Proto() wp.Font { return wp.Font{SHA256: f.SHA256, Family: f.Family, Index: f.Index} }

// ID 組出字型 id。
func ID(sha string, index int) string { return sha + ":" + strconv.Itoa(index) }

// Defaults 是每種語言的預設字型（Q11，用完整名稱 nameID 4 找：family 相同的粗細只差在完整名稱；
// 開源 SIL OFL，安裝時下載到 fonts/）。
// 英文用日文版（內含拉丁字母）；判斷不出語言時也用日文版（同 v1）。
var Defaults = map[string]string{
	"ja": "Noto Sans CJK JP Bold", "en": "Noto Sans CJK JP Bold", "": "Noto Sans CJK JP Bold",
	"zh": "Noto Sans CJK TC Bold", "nan": "Noto Sans CJK TC Bold", "yue": "Noto Sans CJK TC Bold",
	"ko": "Noto Sans CJK KR Bold",
}

// Catalog 是掃描到的字型。
type Catalog struct {
	mu    sync.RWMutex
	fonts []Font
	files map[string]string // sha256 → 絕對路徑
}

// Scan 依序掃描字型資料夾（含子資料夾，例如掛載進來的自訂字型；不存在的資料夾略過）。讀不懂的檔案略過，
// 內容相同的檔案只算一次（先掃到的為準）。
// 字型檔的 sha256 依大小與修改時間快取在 cache，下次啟動不必重算（字型檔很大）。
func Scan(dirs []string, cache map[string]string) (*Catalog, error) {
	c := &Catalog{files: map[string]string{}}
	for _, dir := range dirs {
		if err := c.scan(dir, cache); err != nil {
			return nil, err
		}
	}
	sort.Slice(c.fonts, func(a, b int) bool {
		if c.fonts[a].Family != c.fonts[b].Family {
			return c.fonts[a].Family < c.fonts[b].Family
		}
		return c.fonts[a].ID < c.fonts[b].ID
	})
	defaults := map[string]bool{}
	for _, name := range Defaults {
		defaults[name] = true
	}
	for i := range c.fonts {
		c.fonts[i].Default = defaults[c.fonts[i].FullName]
	}
	return c, nil
}

func (c *Catalog) scan(dir string, cache map[string]string) error {
	return filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		ext := strings.ToLower(filepath.Ext(path))
		if d.IsDir() || (ext != ".ttf" && ext != ".otf" && ext != ".ttc" && ext != ".otc") {
			return nil
		}
		faces, err := readFaces(path)
		if err != nil {
			return nil
		}
		st, err := os.Stat(path)
		if err != nil {
			return nil
		}
		key := fmt.Sprintf("%s|%d|%d", path, st.Size(), st.ModTime().UnixNano())
		sum, ok := cache[key]
		if !ok {
			if sum, _, err = store.HashFile(path); err != nil {
				return nil
			}
			if cache != nil {
				cache[key] = sum
			}
		}
		if _, dup := c.files[sum]; dup {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		c.files[sum] = path
		for i, f := range faces {
			f.SHA256, f.Index, f.File, f.ID = sum, i, filepath.ToSlash(rel), ID(sum, i)
			c.fonts = append(c.fonts, f)
		}
		return nil
	})
}

// List 回傳所有字型。
func (c *Catalog) List() []Font {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return append([]Font{}, c.fonts...)
}

// Get 依 id 找字型。
func (c *Catalog) Get(id string) (Font, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, f := range c.fonts {
		if f.ID == id {
			return f, true
		}
	}
	return Font{}, false
}

// Path 依檔案 sha256 找字型檔（worker 下載、前端 @font-face）。
func (c *Catalog) Path(sha string) (string, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	p, ok := c.files[sha]
	return p, ok
}

// ForLanguage 回傳這個語言要用的字型：設定指定的（chosen 是語言 → 字型 id）優先，沒有就用預設字型。
func (c *Catalog) ForLanguage(language string, chosen map[string]string) (Font, bool) {
	if id, ok := chosen[language]; ok {
		if f, ok := c.Get(id); ok {
			return f, true
		}
	}
	name, ok := Defaults[language]
	if !ok {
		name = Defaults[""]
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	for _, f := range c.fonts {
		if f.FullName == name {
			return f, true
		}
	}
	return Font{}, false
}

// ---- 讀字型檔（只讀 name 與 OS/2 表）------------------------------------------

var errFormat = errors.New("不是字型檔")

func readFaces(path string) ([]Font, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 12 {
		return nil, errFormat
	}
	var offsets []uint32
	if string(data[:4]) == "ttcf" {
		n := binary.BigEndian.Uint32(data[8:12])
		if n == 0 || n > 1000 || len(data) < 12+int(n)*4 {
			return nil, errFormat
		}
		for i := range n {
			offsets = append(offsets, binary.BigEndian.Uint32(data[12+i*4:]))
		}
	} else {
		offsets = []uint32{0}
	}
	var out []Font
	for _, off := range offsets {
		f, err := readFace(data, int(off))
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, nil
}

func readFace(data []byte, off int) (Font, error) {
	if off+12 > len(data) {
		return Font{}, errFormat
	}
	n := int(binary.BigEndian.Uint16(data[off+4:]))
	tables := map[string][]byte{}
	for i := range n {
		rec := off + 12 + i*16
		if rec+16 > len(data) {
			return Font{}, errFormat
		}
		tag := string(data[rec : rec+4])
		start := int(binary.BigEndian.Uint32(data[rec+8:]))
		length := int(binary.BigEndian.Uint32(data[rec+12:]))
		if start+length > len(data) {
			return Font{}, errFormat
		}
		tables[tag] = data[start : start+length]
	}
	names := readNames(tables["name"])
	f := Font{Family: names[1], FullName: names[4], Weight: 400}
	if f.Family == "" {
		return Font{}, errFormat
	}
	if os2 := tables["OS/2"]; len(os2) >= 6 {
		f.Weight = int(binary.BigEndian.Uint16(os2[4:]))
	}
	return f, nil
}

// readNames 讀 name 表：nameID → 名稱。優先 Windows 平台的美式英文，其次任何 Windows 平台、Mac 平台的英文。
func readNames(t []byte) map[int]string {
	out := map[int]string{}
	if len(t) < 6 {
		return out
	}
	count := int(binary.BigEndian.Uint16(t[2:]))
	strs := int(binary.BigEndian.Uint16(t[4:]))
	rank := map[int]int{}
	for i := range count {
		rec := 6 + i*12
		if rec+12 > len(t) {
			break
		}
		platform := binary.BigEndian.Uint16(t[rec:])
		lang := binary.BigEndian.Uint16(t[rec+4:])
		id := int(binary.BigEndian.Uint16(t[rec+6:]))
		length := int(binary.BigEndian.Uint16(t[rec+8:]))
		start := strs + int(binary.BigEndian.Uint16(t[rec+10:]))
		if start+length > len(t) {
			continue
		}
		raw := t[start : start+length]
		var s string
		r := 0
		switch {
		case platform == 3 && lang == 0x409:
			s, r = utf16be(raw), 3
		case platform == 3 || platform == 0:
			s, r = utf16be(raw), 2
		case platform == 1 && lang == 0:
			s, r = string(raw), 1
		default:
			continue
		}
		if r > rank[id] {
			out[id], rank[id] = s, r
		}
	}
	return out
}

func utf16be(b []byte) string {
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.BigEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u))
}
