package planner

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"strings"

	kara "github.com/zoosewu/kara-creator"
	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/lyrics"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
	"github.com/zoosewu/kara-creator/nas/internal/timing"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// QADoc 是 qa.json 的內容。
type QADoc struct {
	Key       string      `json:"key"` // 對時檢查的指紋
	Lines     []wp.QALine `json:"lines"`
	CheckedAt string      `json:"checked_at"`
}

// Counts 統計可能不準與待確認的句數。
func (d QADoc) Counts() QACounts {
	var c QACounts
	for _, ln := range d.Lines {
		switch ln.Status {
		case "wrong":
			c.Wrong++
		case "suspect":
			c.Suspect++
		}
	}
	return c
}

// ReadLyrics 讀 songs/<id>/lyrics.txt；沒有歌詞檔時回傳 nil。
func ReadLyrics(st *store.Store, id string) (*lyrics.Document, error) {
	data, err := os.ReadFile(st.SongPath(id, song.FileLyrics))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	doc := lyrics.Parse(strings.TrimPrefix(string(data), "\ufeff"))
	return &doc, nil
}

// ReadAlignment 讀 alignment.json；還沒對時回傳 nil。
func ReadAlignment(st *store.Store, id string) (*timing.Alignment, error) {
	var a timing.Alignment
	ok, err := readJSON(st.SongPath(id, song.FileAlignment), &a)
	if !ok {
		return nil, err
	}
	return &a, nil
}

// ReadQA 讀 qa.json；還沒檢查過回傳 nil。
func ReadQA(st *store.Store, id string) (*QADoc, error) {
	var d QADoc
	ok, err := readJSON(st.SongPath(id, song.FileQA), &d)
	if !ok {
		return nil, err
	}
	return &d, nil
}

func readJSON(path string, v any) (bool, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return false, err
	}
	return true, nil
}

// Inspect 讀檔、確認檔案，組出 Evaluate 的輸入。
//
// 來源檔的大小或修改時間變了會重算 sha256，並把新的值寫回 song.json（來源換了，之後的階段自然變成需更新）；
// 只有修改時間變、內容相同時也寫回，下次就不必再算。
func Inspect(st *store.Store, id string, settings library.Settings, font func(string) (wp.Font, bool), versions kara.Versions) (Input, error) {
	sg, ok := st.Song(id)
	if !ok {
		return Input{}, store.ErrNoSong
	}
	in := Input{Song: sg, Settings: settings, Font: font, Versions: versions, Render: map[string]RenderFiles{}}

	if sg.Source.File.Name != "" {
		ref, err := st.RefreshRef(id, sg.Source.File)
		in.SourceOK = err == nil
		if err == nil && ref != sg.Source.File {
			if err := st.EditSong(id, func(s *song.Song) error { s.Source.File = ref; return nil }); err != nil {
				return Input{}, err
			}
			sg.Source.File = ref
		}
	}
	if sep := sg.Stages.Separate; sep != nil {
		in.SeparateFilesOK = same(st, id, sep.Instrumental) && same(st, id, sep.Vocals)
	}
	var err error
	if in.Lyrics, err = ReadLyrics(st, id); err != nil {
		return Input{}, err
	}
	if in.Alignment, err = ReadAlignment(st, id); err != nil {
		return Input{}, err
	}
	if doc, err := ReadQA(st, id); err != nil {
		return Input{}, err
	} else if doc != nil {
		in.QAKey, in.QACounts = doc.Key, doc.Counts()
	}
	for target, rec := range sg.Stages.Render {
		files := RenderFiles{VideoOK: same(st, id, rec.Video)}
		if ref, err := st.RefreshRef(id, rec.ASS.FileRef); err == nil {
			files.ASSOK, files.ASSHash = true, ref.SHA256
		}
		in.Render[target] = files
	}
	return in, nil
}

// same 確認檔案存在、內容和紀錄相同。
func same(st *store.Store, id string, ref song.FileRef) bool {
	if ref.Name == "" {
		return false
	}
	cur, err := st.RefreshRef(id, ref)
	return err == nil && cur.SHA256 == ref.SHA256
}
