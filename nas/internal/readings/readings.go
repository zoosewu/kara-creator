// Package readings 是假名（讀音）：自動讀音的快取、手動與自動讀音的合併，
// 以及歌詞編輯器的各種檢視。
//
// 自動讀音（MeCab）由 AI worker 的 reading 任務計算，只看「語言 + 句子」；NAS 快取結果，
// PC 關機時看過的歌詞照樣有假名。目前只有日文有自動讀音。
package readings

import (
	"regexp"
	"sort"
	"strings"

	"github.com/zoosewu/kara-creator/nas/internal/lyrics"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// Span 是一段自動讀音（位置以 code point 計）。
type Span = wp.Span

// HasAuto 表示這個語言有自動讀音（需要 AI worker 算）。
func HasAuto(language string) bool { return language == "ja" }

// ManualReading 表示這個語言的讀音只靠手動標註，每個漢字自成一格（台語、粵語）。
func ManualReading(language string) bool { return language == "nan" || language == "yue" }

// Segment 是歌詞編輯器顯示的一個片段；串起來等於原句。
type Segment struct {
	Text   string  `json:"text"`
	Start  int     `json:"start"`
	End    int     `json:"end"`
	Ruby   *string `json:"ruby"`
	Manual bool    `json:"manual"`
	Slot   *bool   `json:"slot,omitempty"` // 台語、粵語：這個字可以點來標讀音
}

var hanRun = regexp.MustCompile(`^[\x{4e00}-\x{9fff}\x{3400}-\x{4dbf}]+$`)

// Segments 把一句切成片段，有讀音的片段附上讀音。手動讀音優先，和它重疊的自動讀音丟掉。
// auto 是這句的自動讀音（沒有自動讀音的語言傳 nil）。
func Segments(text, language string, auto []Span, manual []lyrics.Ruby) []Segment {
	runes := []rune(text)
	if !HasAuto(language) {
		auto = nil
	}
	slots := ManualReading(language)
	type mark struct {
		start, end int
		ruby       string
		manual     bool
	}
	var marked []mark
	for _, sp := range auto {
		overlap := false
		for _, m := range manual {
			if !(sp.End <= m.Start || sp.Start >= m.End) {
				overlap = true
				break
			}
		}
		if !overlap {
			marked = append(marked, mark{sp.Start, sp.End, sp.Ruby, false})
		}
	}
	for _, m := range manual {
		marked = append(marked, mark{m.Start, m.End, m.Reading, true})
	}
	// 同 Python sorted(tuple)：依 start、end、ruby、manual（False 在前）
	sort.SliceStable(marked, func(a, b int) bool {
		x, y := marked[a], marked[b]
		if x.start != y.start {
			return x.start < y.start
		}
		if x.end != y.end {
			return x.end < y.end
		}
		if x.ruby != y.ruby {
			return x.ruby < y.ruby
		}
		return !x.manual && y.manual
	})

	segments := []Segment{}
	sub := func(a, b int) string {
		a, b = min(max(a, 0), len(runes)), min(max(b, 0), len(runes))
		if a >= b {
			return ""
		}
		return string(runes[a:b])
	}
	plain := func(a, b int) {
		if !slots {
			segments = append(segments, Segment{Text: sub(a, b), Start: a, End: b})
			return
		}
		for k := a; k < b; k++ {
			slot := hanRun.MatchString(string(runes[k]))
			segments = append(segments, Segment{Text: string(runes[k]), Start: k, End: k + 1, Slot: &slot})
		}
	}
	pos := 0
	for _, m := range marked {
		if m.start > pos {
			plain(pos, m.start)
		}
		ruby := m.ruby
		segments = append(segments, Segment{Text: sub(m.start, m.end), Start: m.start, End: m.end, Ruby: &ruby, Manual: m.manual})
		pos = m.end
	}
	if pos < len(runes) {
		plain(pos, len(runes))
	}
	return segments
}

// Lookup 回傳一句的自動讀音；ok 為 false 代表還沒算好（AI worker 不在線）。
type Lookup func(text string) (spans []Span, ok bool)

// EditorLine 是歌詞編輯器的一行：歌詞句多了 segments。
type EditorLine struct {
	lyrics.Line
	Segments *[]Segment `json:"segments,omitempty"`
}

// EditorDoc 是歌詞編輯器（標註模式）用的結構。
type EditorDoc struct {
	Meta     map[string]string `json:"meta"`
	Lines    []EditorLine      `json:"lines"`
	Language *string           `json:"language"` // 依歌詞文字也判斷不出來時為 null
}

// Views 是歌詞編輯器的各種檢視。
type Views struct {
	Doc       EditorDoc `json:"doc"`                        // 結構（標註模式用，含每句的假名片段）
	Text      string    `json:"text"`                       // 存檔格式（只記手動指定的讀音）
	Paren     []string  `json:"paren"`                      // 寫在括號裡的讀音（日文歌），編輯器提示可以轉成讀音標註
	Annotated string    `json:"annotated"`                  // 標註原文：演唱者標籤 + 每個漢字的讀音（自動判斷的也列出來）
	Plain     string    `json:"plain"`                      // 原始歌詞：只有歌詞與翻譯，沒有任何標註
	Pending   []int     `json:"pending_readings,omitempty"` // 自動讀音還沒算好的句子（歌詞句的編號，從 0 起算）
}

func languageOr(doc lyrics.Document, language string) string {
	if language != "" {
		return language
	}
	return lyrics.DetectLanguage(doc.Texts())
}

// BuildViews 產生歌詞編輯器的各種檢視。language 為空字串時依歌詞文字判斷。
// 自動讀音還沒算好的句子列在 Pending，那幾句先不顯示自動讀音。
func BuildViews(doc lyrics.Document, language string, lookup Lookup) Views {
	language = languageOr(doc, language)
	var v Views
	v.Doc.Meta = doc.Meta
	if language != "" {
		v.Doc.Language = &language
	}
	full := lyrics.Document{Meta: doc.Meta}
	n := 0
	for _, ln := range doc.Lines {
		if ln.Kind != lyrics.KindLyric {
			v.Doc.Lines = append(v.Doc.Lines, EditorLine{Line: ln})
			full.Lines = append(full.Lines, ln)
			continue
		}
		var auto []Span
		if HasAuto(language) {
			spans, ok := lookup(ln.Text)
			if !ok {
				v.Pending = append(v.Pending, n)
			}
			auto = spans
		}
		segs := Segments(ln.Text, language, auto, ln.Rubies)
		v.Doc.Lines = append(v.Doc.Lines, EditorLine{Line: ln, Segments: &segs})
		rubies := []lyrics.Ruby{}
		for _, s := range segs {
			if s.Ruby != nil && *s.Ruby != "" {
				rubies = append(rubies, lyrics.Ruby{Start: s.Start, End: s.End, Reading: *s.Ruby})
			}
		}
		full.Lines = append(full.Lines, lyrics.Line{Kind: lyrics.KindLyric, Text: ln.Text, Singer: ln.Singer,
			Rubies: rubies, Translation: ln.Translation})
		n++
	}
	if v.Doc.Lines == nil {
		v.Doc.Lines = []EditorLine{}
	}
	// 原始歌詞也帶著翻譯（「> 翻譯」行），方便整段貼上歌詞與翻譯
	var plain []string
	for _, ln := range doc.Lines {
		switch {
		case ln.Kind == lyrics.KindBlank:
			plain = append(plain, "")
		case ln.Translation != "":
			plain = append(plain, ln.Text+"\n> "+ln.Translation)
		default:
			plain = append(plain, ln.Text)
		}
	}
	if p := strings.Join(plain, "\n"); p != "" {
		v.Plain = p + "\n"
	}
	v.Text = lyrics.Serialize(doc)
	v.Paren = []string{}
	if language == "ja" {
		v.Paren = lyrics.ParenReadings(v.Text)
	}
	v.Annotated = lyrics.Serialize(full)
	return v
}

// FromAnnotated 把「標註原文」轉回結構：和自動讀音相同的讀音不另外記成手動指定。
// 有句子的自動讀音還沒算好時列在 missing，呼叫端要拒絕轉換（不能猜：把所有讀音都當手動會讓檔案多出自動讀音）。
func FromAnnotated(text, language string, lookup Lookup) (doc lyrics.Document, missing []int) {
	doc = lyrics.Parse(text)
	language = languageOr(doc, language)
	n := 0
	for i, ln := range doc.Lines {
		if ln.Kind != lyrics.KindLyric {
			continue
		}
		auto := map[Span]bool{}
		if HasAuto(language) {
			spans, ok := lookup(ln.Text)
			if !ok {
				missing = append(missing, n)
			}
			for _, s := range Segments(ln.Text, language, spans, nil) {
				if s.Ruby != nil && *s.Ruby != "" {
					auto[Span{Start: s.Start, End: s.End, Ruby: *s.Ruby}] = true
				}
			}
		}
		kept := []lyrics.Ruby{}
		for _, r := range ln.Rubies {
			if !auto[Span{Start: r.Start, End: r.End, Ruby: r.Reading}] {
				kept = append(kept, r)
			}
		}
		doc.Lines[i].Rubies = kept
		n++
	}
	return doc, missing
}
