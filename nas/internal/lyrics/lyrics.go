// Package lyrics 解析與寫回歌詞檔（v1 songtool/lyrics.py 的移植，行為要逐字相同，黃金測試見 lyrics_test.go）。
//
// 格式（UTF-8，一行就是畫面上的一句，空行會忽略）：
//
//	# title: 歌名              歌曲資訊，前奏時顯示成標題畫面
//	# artist: 歌手名
//	# 副歌                     其他 # 開頭的行是註解，可用來標段落
//	[女] 窗外的花開了          行首 [男] / [女] / [合] 標註由誰唱
//	> 上一句的翻譯             不參與對時
//	漢字{よみ}                 讀音：標在前面連續的漢字（或英數字）上
//	{原字|よみ}                明確指定範圍
//
// 所有位置（Ruby.Start、Ruby.End）都以 Unicode code point 計，和 Python 的字串索引相同。
package lyrics

import (
	"regexp"
	"sort"
	"strings"

	"github.com/zoosewu/kara-creator/nas/internal/pystr"
)

// Singers 是可以標的演唱者。
var Singers = []string{"男", "女", "合"}

// 行種類。
const (
	KindLyric   = "lyric"
	KindComment = "comment"
	KindBlank   = "blank"
)

// Ruby 是一句裡手動標的讀音，位置 [Start, End)。
type Ruby struct {
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Reading string `json:"reading"`
}

// Line 是歌詞檔的一行。
type Line struct {
	Kind        string  `json:"kind"`        // lyric | comment | blank
	Text        string  `json:"text"`        // lyric：去掉標記後的純文字；comment：整行原文
	Singer      *string `json:"singer"`      // 男 | 女 | 合 | null
	Rubies      []Ruby  `json:"rubies"`      // 依 Start 排序
	Translation string  `json:"translation"` // 下一行的「> 翻譯」
}

// Document 是整份歌詞檔。
type Document struct {
	Meta  map[string]string `json:"meta"` // title、artist
	Lines []Line            `json:"lines"`
}

// LyricLines 回傳要唱的句子（不含註解、空行）。
func (d Document) LyricLines() []Line {
	var out []Line
	for _, ln := range d.Lines {
		if ln.Kind == KindLyric {
			out = append(out, ln)
		}
	}
	return out
}

// Texts 回傳每句要唱的文字。
func (d Document) Texts() []string {
	var out []string
	for _, ln := range d.LyricLines() {
		out = append(out, ln.Text)
	}
	return out
}

const (
	hanClass  = `\x{4e00}-\x{9fff}` // v1 的 _HAN 後來被重新定義成只有這一段（不含擴充 A），照抄
	baseClass = `\x{4e00}-\x{9fff}\x{3400}-\x{4dbf}\x{3005}\x{3006}\x{30f6}A-Za-z0-9`
)

var (
	metaRe   = regexp.MustCompile(`^(?i:#[` + pystr.SpaceClass + `]*(title|artist)[` + pystr.SpaceClass + `]*[:：][` + pystr.SpaceClass + `]*)(.*)$`)
	singerRe = regexp.MustCompile(`^\[(男|女|合)\][` + pystr.SpaceClass + `]*`)
	// 羅馬字讀音（台羅、粵拼、拼音等，可含聲調符號與數字）；用空白或連字號分隔音節。
	latinReadingRe = regexp.MustCompile(`^[A-Za-z0-9\x{c0}-\x{24f}\x{1e00}-\x{1eff}\x{300}-\x{36f}\x{358}\x{207f}'` + pystr.SpaceClass + `\-]+$`)
	syllableSepRe  = regexp.MustCompile(`[` + pystr.SpaceClass + `\-]+`)
	baseRe         = regexp.MustCompile(`^[` + baseClass + `]$`)
	hanRe          = regexp.MustCompile(`^[` + hanClass + `]$`)
)

func isBase(r rune) bool { return baseRe.MatchString(string(r)) }
func isHan(r rune) bool  { return hanRe.MatchString(string(r)) }

// syllables 把羅馬字讀音拆成音節；不是羅馬字（例如假名）時 ok 為 false。
func syllables(reading string) (out []string, ok bool) {
	if !latinReadingRe.MatchString(reading) {
		return nil, false
	}
	for _, s := range syllableSepRe.Split(pystr.Strip(reading), -1) {
		if s != "" {
			out = append(out, s)
		}
	}
	return out, true
}

// Parse 解析歌詞檔的內容（呼叫前先去掉 UTF-8 BOM）。
func Parse(text string) Document {
	doc := Document{Meta: map[string]string{}, Lines: []Line{}}
	for _, raw := range pystr.SplitLines(text) {
		line := pystr.Strip(raw)
		switch {
		case line == "":
			doc.Lines = append(doc.Lines, Line{Kind: KindBlank, Rubies: []Ruby{}})
		case strings.HasPrefix(line, "#"):
			if m := metaRe.FindStringSubmatch(line); m != nil {
				doc.Meta[strings.ToLower(m[1])] = pystr.Strip(m[2])
			} else {
				doc.Lines = append(doc.Lines, Line{Kind: KindComment, Text: line, Rubies: []Ruby{}})
			}
		case strings.HasPrefix(line, ">"):
			// 「> 翻譯」：上一句歌詞的翻譯；前面不是歌詞的話當成註解保留下來。
			if n := len(doc.Lines); n > 0 && doc.Lines[n-1].Kind == KindLyric {
				// 同一句寫了好幾行翻譯就接在一起（顯示時只有一行）。
				prev := &doc.Lines[n-1]
				prev.Translation = pystr.JoinFields(prev.Translation + " " + line[1:])
			} else {
				doc.Lines = append(doc.Lines, Line{Kind: KindComment, Text: line, Rubies: []Ruby{}})
			}
		default:
			doc.Lines = append(doc.Lines, parseLyric(line))
		}
	}
	// 頭尾的空行沒有意義，去掉以免寫回時越積越多。
	for len(doc.Lines) > 0 && doc.Lines[0].Kind == KindBlank {
		doc.Lines = doc.Lines[1:]
	}
	for len(doc.Lines) > 0 && doc.Lines[len(doc.Lines)-1].Kind == KindBlank {
		doc.Lines = doc.Lines[:len(doc.Lines)-1]
	}
	return doc
}

func parseLyric(line string) Line {
	var singer *string
	body := line
	if m := singerRe.FindStringSubmatchIndex(line); m != nil {
		s := line[m[2]:m[3]]
		singer = &s
		body = line[m[1]:]
	}
	b := []rune(pystr.JoinFields(body)) // 全形空白也一併收斂成單一半形空白

	var text []rune
	rubies := []Ruby{}
	for i := 0; i < len(b); {
		if b[i] != '{' {
			text = append(text, b[i])
			i++
			continue
		}
		j := indexRune(b, '}', i)
		if j == -1 { // 沒有收尾的大括號當成一般文字
			text = append(text, b[i:]...)
			break
		}
		inner := string(b[i+1 : j])
		var reading string
		start := len(text)
		if base, rd, ok := strings.Cut(inner, "|"); ok {
			reading = rd
			text = append(text, []rune(base)...)
		} else {
			reading = inner
			// 簡寫標在前面連續的漢字上，但不越過前一個標註。
			floor := 0
			for _, r := range rubies {
				floor = max(floor, r.End)
			}
			for start > floor && isBase(text[start-1]) {
				start--
			}
			// 中文（台語、粵語）一字一音：羅馬字讀音有幾個音節，就只標最後幾個漢字（你佇{tī} 只標「佇」）。
			first := ' '
			if start < len(text) {
				first = text[start]
			}
			if syl, _ := syllables(reading); len(syl) > 0 && isHan(first) && len(syl) < len(text)-start {
				start = len(text) - len(syl)
			}
		}
		if pystr.Strip(reading) != "" && start < len(text) {
			ruby := Ruby{Start: start, End: len(text), Reading: pystr.Strip(reading)}
			kept := rubies[:0]
			for _, r := range rubies {
				if r.End <= ruby.Start || r.Start >= ruby.End {
					kept = append(kept, r)
				}
			}
			rubies = append(kept, ruby)
		}
		i = j + 1
	}
	sort.SliceStable(rubies, func(a, b int) bool { return rubies[a].Start < rubies[b].Start })
	return Line{Kind: KindLyric, Text: string(text), Singer: singer, Rubies: rubies}
}

func indexRune(rs []rune, r rune, from int) int {
	for k := from; k < len(rs); k++ {
		if rs[k] == r {
			return k
		}
	}
	return -1
}

// Serialize 寫回歌詞檔的文字（結尾有換行）。
func Serialize(doc Document) string {
	var out []string
	for _, key := range []string{"title", "artist"} {
		if v := doc.Meta[key]; v != "" {
			out = append(out, "# "+key+": "+v)
		}
	}
	for _, ln := range doc.Lines {
		switch ln.Kind {
		case KindBlank:
			out = append(out, "")
		case KindComment:
			out = append(out, ln.Text)
		default:
			out = append(out, serializeLyric(ln))
			if ln.Translation != "" {
				out = append(out, "> "+ln.Translation)
			}
		}
	}
	return strings.Join(out, "\n") + "\n"
}

func serializeLyric(ln Line) string {
	text := []rune(ln.Text)
	rubies := append([]Ruby(nil), ln.Rubies...)
	sort.SliceStable(rubies, func(a, b int) bool { return rubies[a].Start < rubies[b].Start })

	var sb strings.Builder
	if ln.Singer != nil && *ln.Singer != "" {
		sb.WriteString("[" + *ln.Singer + "] ")
	}
	pos := 0
	for _, r := range rubies {
		base := slice(text, r.Start, r.End)
		before := slice(text, 0, r.Start)
		// 能用簡寫（前面連續漢字剛好就是要標注的範圍）就用簡寫，比較好讀。
		short := all(base, isBase) && !(len(before) > 0 && isBase(before[len(before)-1]))
		// 漢字配羅馬字、音節數等於字數時，簡寫也不會標錯範圍（見 parseLyric）。
		if syl, _ := syllables(r.Reading); len(syl) > 0 && all(base, isHan) && len(syl) == len(base) {
			short = true
		}
		sb.WriteString(string(slice(text, pos, r.Start)))
		if short {
			sb.WriteString(string(base) + "{" + r.Reading + "}")
		} else {
			sb.WriteString("{" + string(base) + "|" + r.Reading + "}")
		}
		pos = r.End
	}
	sb.WriteString(string(slice(text, pos, len(text))))
	return sb.String()
}

// slice 同 Python 的 s[a:b]（超出範圍時截掉，不會 panic）。
func slice(rs []rune, a, b int) []rune {
	a, b = min(max(a, 0), len(rs)), min(max(b, 0), len(rs))
	if a >= b {
		return nil
	}
	return rs[a:b]
}

func all(rs []rune, f func(rune) bool) bool {
	for _, r := range rs {
		if !f(r) {
			return false
		}
	}
	return true
}

var (
	kanaRe   = regexp.MustCompile(`[\x{3040}-\x{30ff}]`)
	hangulRe = regexp.MustCompile(`[\x{ac00}-\x{d7af}]`)
	hanAnyRe = regexp.MustCompile(`[` + hanClass + `]`)
)

// DetectLanguage 由文字判斷語言（ja / ko / zh / en）；判斷不出來時回傳空字串。
func DetectLanguage(lines []string) string {
	text := strings.Join(lines, "")
	switch {
	case kanaRe.MatchString(text):
		return "ja"
	case hangulRe.MatchString(text):
		return "ko"
	case hanAnyRe.MatchString(text):
		return "zh"
	}
	// 以拉丁字母為主的歌詞當成英文。
	letters := 0
	for _, r := range text {
		if r < 0x80 && (r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z') {
			letters++
		}
	}
	if letters > 0 && float64(letters) >= 0.5*float64(len([]rune(strings.ReplaceAll(text, " ", "")))) {
		return "en"
	}
	return ""
}

// 日文歌詞常把特殊念法寫在括號裡：運命(さだめ)、本気（マジ）。
// 只認「漢字緊接著括號、括號裡全是假名」；前面已經有 {讀音} 的話一併取代。
var parenRe = regexp.MustCompile(`([\x{4e00}-\x{9fff}\x{3400}-\x{4dbf}\x{3005}\x{3006}\x{30f6}]+)(?:\{[^{}|]*\})?[(\x{ff08}]([\x{3040}-\x{30ff}\x{30fc}\x{30fb}]+)[)\x{ff09}]`)

// ParenReadings 列出歌詞文字裡寫在括號中的讀音（例如「運命(さだめ)」），依出現順序、不重複。
func ParenReadings(text string) []string {
	found := []string{}
	seen := map[string]bool{}
	for _, line := range pystr.SplitLines(text) {
		if strings.HasPrefix(pystr.LStrip(line), "#") {
			continue
		}
		for _, m := range parenRe.FindAllString(line, -1) {
			if !seen[m] {
				seen[m] = true
				found = append(found, m)
			}
		}
	}
	return found
}

// ParenToRuby 把括號讀音轉成讀音標註：運命(さだめ) → {運命|さだめ}（註解行不動）。
func ParenToRuby(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if !strings.HasPrefix(pystr.LStrip(line), "#") {
			lines[i] = parenRe.ReplaceAllString(line, "{$1|$2}")
		}
	}
	return strings.Join(lines, "\n")
}

// Sanitize 整理從 API 送來的結構（v1 lyrics.from_dict）：去掉空的讀音、不認得的演唱者，翻譯的空白收斂。
func Sanitize(doc Document) Document {
	out := Document{Meta: map[string]string{}, Lines: []Line{}}
	for k, v := range doc.Meta {
		if v != "" {
			out.Meta[k] = v
		}
	}
	for _, ln := range doc.Lines {
		kind := ln.Kind
		if kind == "" {
			kind = KindLyric
		}
		rubies := []Ruby{}
		for _, r := range ln.Rubies {
			if pystr.Strip(r.Reading) != "" {
				rubies = append(rubies, r)
			}
		}
		var singer *string
		if ln.Singer != nil {
			for _, s := range Singers {
				if *ln.Singer == s {
					v := s
					singer = &v
				}
			}
		}
		out.Lines = append(out.Lines, Line{Kind: kind, Text: ln.Text, Singer: singer, Rubies: rubies,
			Translation: pystr.JoinFields(ln.Translation)})
	}
	return out
}
