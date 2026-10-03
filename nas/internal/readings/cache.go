package readings

import (
	"bufio"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strconv"
	"sync"
)

// Cache 是自動讀音的快取（<library>/cache/readings.jsonl，每行一句）。啟動時全部讀進記憶體，新增時附加一行。
//
// 每行記下當時的 versions.reading；讀音規則改版後，啟動時丟掉舊版的資料並重寫檔案。
type Cache struct {
	path    string
	version int

	mu sync.Mutex
	m  map[string][]Span
}

type cacheLine struct {
	K     string `json:"k"`
	V     int    `json:"v"`
	Spans []Span `json:"spans"`
}

// Key 是一句的快取鍵：sha256(語言 \x1f 句子 \x1f 讀音版本)。
func Key(language, text string, version int) string {
	s := sha256.Sum256([]byte(language + "\x1f" + text + "\x1f" + strconv.Itoa(version)))
	return hex.EncodeToString(s[:])
}

// OpenCache 讀入快取檔（不存在就從空的開始）。
func OpenCache(path string, version int) (*Cache, error) {
	c := &Cache{path: path, version: version, m: map[string][]Span{}}
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, err
	}
	stale := 0
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var ln cacheLine
		if json.Unmarshal(sc.Bytes(), &ln) != nil || ln.V != version {
			stale++ // 舊版或寫到一半的行
			continue
		}
		c.m[ln.K] = ln.Spans
	}
	f.Close()
	if err := sc.Err(); err != nil {
		return nil, err
	}
	if stale > 0 {
		if err := c.rewrite(); err != nil {
			return nil, err
		}
	}
	return c, nil
}

// Get 查一句的自動讀音。
func (c *Cache) Get(language, text string) ([]Span, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	spans, ok := c.m[Key(language, text, c.version)]
	return spans, ok
}

// Put 存一句的自動讀音（已經有就不重寫）。
func (c *Cache) Put(language, text string, spans []Span) error {
	if spans == nil {
		spans = []Span{}
	}
	k := Key(language, text, c.version)
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.m[k]; ok {
		return nil
	}
	data, err := json.Marshal(cacheLine{K: k, V: c.version, Spans: spans})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(c.path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	c.m[k] = spans
	return nil
}

// rewrite 只留目前版本的資料重寫整個檔案。
func (c *Cache) rewrite() error {
	var buf []byte
	for k, spans := range c.m {
		data, err := json.Marshal(cacheLine{K: k, V: c.version, Spans: spans})
		if err != nil {
			return err
		}
		buf = append(append(buf, data...), '\n')
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, buf, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
