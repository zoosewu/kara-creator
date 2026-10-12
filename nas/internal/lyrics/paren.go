package lyrics

import (
	"regexp"
	"strings"

	"github.com/zoosewu/kara-creator/nas/internal/pystr"
)

// 日文歌詞常把特殊念法寫在括號裡，存歌詞時自動轉成讀音標註：
//
//	運命(さだめ)、本気（マジ）       → {運命|さだめ}、{本気|マジ}
//	取り戻す(とりもどす)             → {取|と}り{戻|もど}す（送假名、混合詞：假名的部分要和讀音對得上）
//	運命 (さだめ)、運命《さだめ》     → 括號前可以隔一個空白；也認《》
//	｜運命《さだめ》                 → ｜到括號之間就是要標的字
//	Love(ラブ)                       → 英文字（含數字、'）
//
// 括號裡要全是假名。括號前面連續的漢字與假名裡，取「以漢字開頭、而且假名的部分和讀音對得上」的最長一段
// （君の夢(ゆめ) 只標「夢」）；對不上就不轉。前面已經有 {讀音} 的話一併取代。註解行不動。

var parenClose = map[rune]rune{'(': ')', '（': '）', '《': '》'}

type parenMatch struct {
	start, end int    // 行內的 rune 位置 [start, end)
	markup     string // 換成的讀音標註
}

func isKanjiLike(r rune) bool {
	return (r >= 0x4e00 && r <= 0x9fff) || (r >= 0x3400 && r <= 0x4dbf) || r == 0x3005 || r == 0x3006 || r == 0x30f6
}

func isKana(r rune) bool { return r >= 0x3040 && r <= 0x30ff && !isKanjiLike(r) && r != 0x30fb }

func isReading(r rune) bool { return r >= 0x3040 && r <= 0x30ff }

func isWordLatin(r rune) bool {
	return (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '\''
}

// hiragana 把片假名換成平假名（一個字換一個字，位置不變），比對送假名時不分平假名、片假名。
func hiragana(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 0x30a1 && r <= 0x30f5 { // ァ–ヵ（ヶ 當漢字用，不換）
			return r - 0x60
		}
		return r
	}, s)
}

// parenMatches 找出一行裡所有可以轉換的括號讀音。
func parenMatches(line string) []parenMatch {
	rs := []rune(line)
	var out []parenMatch
	floor := 0 // 前一個轉換的結尾：不重疊
	for i := 0; i < len(rs); i++ {
		closeR, ok := parenClose[rs[i]]
		if !ok {
			continue
		}
		j := i + 1
		for j < len(rs) && rs[j] != closeR && isReading(rs[j]) {
			j++
		}
		if j >= len(rs) || rs[j] != closeR || j == i+1 {
			continue
		}
		reading := string(rs[i+1 : j])
		k := i // 要標的字的結尾（不含）
		if k > floor && (rs[k-1] == ' ' || rs[k-1] == '　') {
			k--
		}
		if k > floor && rs[k-1] == '}' { // 前面已經有 {讀音}：一併取代
			for p := k - 2; p >= floor; p-- {
				if rs[p] == '{' {
					k = p
					break
				}
				if rs[p] == '}' || rs[p] == '|' {
					break
				}
			}
		}
		start, markup, ok := parenBase(rs, floor, k, reading)
		if !ok {
			continue
		}
		out = append(out, parenMatch{start: start, end: j + 1, markup: markup})
		floor, i = j+1, j
	}
	return out
}

// parenBase 決定 rs[floor:k] 結尾要標讀音的字，回傳起點與讀音標註。
func parenBase(rs []rune, floor, k int, reading string) (int, string, bool) {
	if k <= floor {
		return 0, "", false
	}
	// ｜原字《讀音》
	for p := k - 1; p >= floor; p-- {
		if rs[p] == '｜' {
			if base := string(rs[p+1 : k]); base != "" && !strings.ContainsAny(base, "{}|｜ 　") {
				return p, "{" + base + "|" + reading + "}", true
			}
			break
		}
		if pystr.IsSpace(rs[p]) || rs[p] == '{' || rs[p] == '}' {
			break
		}
	}
	// 英文字
	if isWordLatin(rs[k-1]) {
		s := k - 1
		for s > floor && isWordLatin(rs[s-1]) {
			s--
		}
		return s, "{" + string(rs[s:k]) + "|" + reading + "}", true
	}
	// 漢字與假名
	s := k
	for s > floor && (isKanjiLike(rs[s-1]) || isKana(rs[s-1])) {
		s--
	}
	for p := s; p < k; p++ { // 從最長的一段開始試
		if !isKanjiLike(rs[p]) {
			continue
		}
		if markup, ok := splitReading(rs[p:k], reading); ok {
			return p, markup, true
		}
	}
	return 0, "", false
}

// splitReading 把讀音分給漢字的部分：假名的部分要和讀音對得上（取り戻す、とりもどす → {取|と}り{戻|もど}す）。
func splitReading(base []rune, reading string) (string, bool) {
	type seg struct {
		text  string
		kanji bool
	}
	var segs []seg
	for _, r := range base {
		k := isKanjiLike(r)
		if n := len(segs); n > 0 && segs[n-1].kanji == k {
			segs[n-1].text += string(r)
		} else {
			segs = append(segs, seg{string(r), k})
		}
	}
	var pat strings.Builder
	pat.WriteString("^")
	for _, s := range segs {
		if s.kanji {
			pat.WriteString("(.+?)")
		} else {
			pat.WriteString(regexp.QuoteMeta(hiragana(s.text)))
		}
	}
	pat.WriteString("$")
	idx := regexp.MustCompile(pat.String()).FindStringSubmatchIndex(hiragana(reading))
	if idx == nil {
		return "", false
	}
	var out strings.Builder
	g := 1
	for _, s := range segs {
		if !s.kanji {
			out.WriteString(s.text)
			continue
		}
		// 片假名換平假名不改變位元組長度：位置可以直接用在原本的讀音上
		out.WriteString("{" + s.text + "|" + reading[idx[2*g]:idx[2*g+1]] + "}")
		g++
	}
	return out.String(), true
}

// ParenReadings 列出歌詞文字裡寫在括號中的讀音（原本的寫法，例如「運命(さだめ)」），依出現順序、不重複。
func ParenReadings(text string) []string {
	found := []string{}
	seen := map[string]bool{}
	for _, line := range pystr.SplitLines(text) {
		if strings.HasPrefix(pystr.LStrip(line), "#") {
			continue
		}
		rs := []rune(line)
		for _, m := range parenMatches(line) {
			if orig := string(rs[m.start:m.end]); !seen[orig] {
				seen[orig] = true
				found = append(found, orig)
			}
		}
	}
	return found
}

// ParenToRuby 把括號讀音轉成讀音標註（註解行不動）。
func ParenToRuby(text string) string {
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		if strings.HasPrefix(pystr.LStrip(line), "#") {
			continue
		}
		matches := parenMatches(line)
		if len(matches) == 0 {
			continue
		}
		rs := []rune(line)
		var sb strings.Builder
		pos := 0
		for _, m := range matches {
			sb.WriteString(string(rs[pos:m.start]))
			sb.WriteString(m.markup)
			pos = m.end
		}
		sb.WriteString(string(rs[pos:]))
		lines[i] = sb.String()
	}
	return strings.Join(lines, "\n")
}
