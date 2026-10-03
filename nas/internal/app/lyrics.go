package app

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/zoosewu/kara-creator/nas/internal/lyrics"
	"github.com/zoosewu/kara-creator/nas/internal/planner"
	"github.com/zoosewu/kara-creator/nas/internal/readings"
	"github.com/zoosewu/kara-creator/nas/internal/scheduler"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// ReadingWait 是編輯歌詞時等 AI 算假名最多等多久（docs/v2/architecture.md「編輯歌詞」）。
var ReadingWait = 3 * time.Second

// lookup 回傳查讀音快取的函式。
func (a *App) lookup(language string) readings.Lookup {
	return func(text string) ([]readings.Span, bool) { return a.Readings.Get(language, text) }
}

// ensureReadings 讓還沒有自動讀音的句子交給 AI 算：最多等 ReadingWait；沒有在線的 AI 或逾時就先回去，
// 算好後送出 readings 事件（song 是哪首歌在等）。
func (a *App) ensureReadings(language string, texts []string, songID string) {
	if !readings.HasAuto(language) {
		return
	}
	var missing []string
	seen := map[string]bool{}
	for _, t := range texts {
		if _, ok := a.Readings.Get(language, t); !ok && !seen[t] {
			missing = append(missing, t)
			seen[t] = true
		}
	}
	if len(missing) == 0 {
		return
	}
	key := readings.Key(language, strings.Join(missing, "\x1e"), a.Versions.Reading)
	a.mu.Lock()
	done, running := a.inflight[key]
	if !running {
		done = make(chan struct{})
		a.inflight[key] = done
	}
	a.mu.Unlock()
	if !running {
		go a.requestReadings(key, language, missing, songID, done)
	}
	if !a.readingWorkerOnline() {
		return
	}
	select {
	case <-done:
	case <-time.After(ReadingWait):
	}
}

func (a *App) readingWorkerOnline() bool {
	for _, w := range a.Sched.Workers() {
		if w.Online && w.VersionOK && !w.Disabled && contains(w.Kinds, wp.KindReading) {
			return true
		}
	}
	return false
}

func (a *App) requestReadings(key, language string, texts []string, songID string, done chan struct{}) {
	defer func() {
		a.mu.Lock()
		delete(a.inflight, key)
		a.mu.Unlock()
		close(done)
	}()
	// 任務留在佇列等 AI 上線；伺服器關閉前都有效
	res, err := a.Sched.Submit(context.Background(), scheduler.Spec{Kind: wp.KindReading, Priority: wp.PriorityRealtime,
		Song: songID, Params: wp.ReadingParams{Language: language, Texts: texts}})
	if err != nil {
		return
	}
	defer res.Cleanup()
	var out wp.ReadingResult
	if json.Unmarshal(res.Raw, &out) != nil || len(out.Lines) != len(texts) {
		return
	}
	for i, t := range texts {
		_ = a.Readings.Put(language, t, out.Lines[i])
	}
	a.Events.Publish("readings", map[string]string{"song": songID})
}

// LyricsViews 是歌詞編輯器的資料。
type LyricsViews struct {
	readings.Views
	Exists bool `json:"exists" doc:"已經有歌詞檔"`
}

// languageOf 是這首歌的演唱語言（手動指定 > 依歌詞文字判斷）。
func (a *App) languageOf(id string, doc lyrics.Document) string {
	if sg, ok := a.Store.Song(id); ok && sg.Info.Language != "" {
		return sg.Info.Language
	}
	return lyrics.DetectLanguage(doc.Texts())
}

// Lyrics 回傳一首歌的歌詞檢視（需要的話先請 AI 算假名）。
func (a *App) Lyrics(id string) (LyricsViews, error) {
	if _, ok := a.Store.Song(id); !ok {
		return LyricsViews{}, errNotFound
	}
	doc, err := planner.ReadLyrics(a.Store, id)
	if err != nil {
		return LyricsViews{}, err
	}
	exists := doc != nil
	if doc == nil {
		empty := lyrics.Parse("")
		doc = &empty
	}
	lang := a.languageOf(id, *doc)
	a.ensureReadings(lang, doc.Texts(), id)
	v := LyricsViews{Views: readings.BuildViews(*doc, lang, a.lookup(lang)), Exists: exists}
	if !exists {
		v.Text, v.Annotated = "", ""
	}
	return v, nil
}

// SaveLyrics 存歌詞（存檔格式的文字）。
func (a *App) SaveLyrics(id, text string) (LyricsViews, error) {
	if _, ok := a.Store.Song(id); !ok {
		return LyricsViews{}, errNotFound
	}
	path := a.Store.SongPath(id, song.FileLyrics)
	if strings.TrimSpace(text) == "" {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return LyricsViews{}, err
		}
	} else if err := store.WriteFile(path, []byte(lyrics.Serialize(lyrics.Parse(text)))); err != nil {
		return LyricsViews{}, err
	}
	a.invalidate(id)
	a.touched()
	return a.Lyrics(id)
}

// ConvertRequest 是文字 ↔ 結構互轉（v1 /api/lyrics/convert）。
type ConvertRequest struct {
	Text        *string          `json:"text,omitempty" doc:"要轉換的文字；不給時從 doc 產生"`
	Doc         *lyrics.Document `json:"doc,omitempty" doc:"結構；format=plain 時是原本的結構（保留演唱者與讀音）"`
	Format      string           `json:"format,omitempty" enum:"file,annotated,plain" doc:"text 的格式：file（存檔格式）、annotated（標註原文）、plain（原始歌詞）"`
	Language    string           `json:"language,omitempty" doc:"演唱語言；空字串 = 依歌詞文字判斷"`
	ParenToRuby bool             `json:"paren_to_ruby,omitempty" doc:"把「運命(さだめ)」這種括號讀音轉成讀音標註（只處理 format=file）"`
	Song        string           `json:"song,omitempty" doc:"這段歌詞是哪首歌的（假名算好時通知用）"`
}

// ErrReadingsPending 表示自動讀音還沒算好，不能把標註原文轉回結構。
var ErrReadingsPending = errors.New("假名還沒算好（AI 伺服器不在線？），暫時無法從標註原文轉回，請稍後再試")

// Convert 文字 ↔ 結構互轉，回傳各種檢視。
func (a *App) Convert(req ConvertRequest) (readings.Views, error) {
	var doc lyrics.Document
	lang := req.Language
	switch {
	case req.Text == nil:
		if req.Doc != nil {
			doc = lyrics.Sanitize(*req.Doc)
		} else {
			doc = lyrics.Parse("")
		}
	case req.Format == "annotated":
		parsed := lyrics.Parse(*req.Text)
		if lang == "" {
			lang = lyrics.DetectLanguage(parsed.Texts())
		}
		a.ensureReadings(lang, parsed.Texts(), req.Song)
		var missing []int
		doc, missing = readings.FromAnnotated(*req.Text, lang, a.lookup(lang))
		if len(missing) > 0 {
			return readings.Views{}, ErrReadingsPending
		}
	case req.Format == "plain":
		base := lyrics.Parse("")
		if req.Doc != nil {
			base = lyrics.Sanitize(*req.Doc)
		}
		doc = lyrics.FromPlain(*req.Text, base)
	default:
		text := *req.Text
		if req.ParenToRuby {
			text = lyrics.ParenToRuby(text)
		}
		doc = lyrics.Parse(text)
	}
	if doc.Meta == nil {
		doc.Meta = map[string]string{}
	}
	if lang == "" {
		lang = lyrics.DetectLanguage(doc.Texts())
	}
	a.ensureReadings(lang, doc.Texts(), req.Song)
	return readings.BuildViews(doc, lang, a.lookup(lang)), nil
}
