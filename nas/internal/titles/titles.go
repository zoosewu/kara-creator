// Package titles 從影片資訊推測歌名與演唱者（純規則、不用 LLM；規格測試見 titles_test.go）。
//
// 依序嘗試：
//  1. yt-dlp 的歌曲資訊（track / artists）：YouTube Music、「- Topic」頻道等自動產生的影片會有，最準。
//  2. 標題規則解析：
//     - 先去掉只含雜訊的括號，例如 【Official】［MV］（Official Video）
//     - 『』「」《》〈〉“” 裡的是歌名，括號前面是演唱者
//     - 沒有引號時，【】［］[] 裡不是雜訊的內容當歌名
//     - 「演唱者 - 歌名」
//     - 都不符合就整個標題當歌名
//     演唱者找不到時用頻道名稱（去掉 - Topic、Official Channel、VEVO 等）。
//
// 中英並列時去掉英文譯名；演唱者只留主要歌手（去掉 ft. / feat. 之後的合作歌手），歌名裡的「(feat. …)」也去掉。
//
// 有些規則需要前後文斷言（lookbehind / lookahead），Go 的 regexp 不支援，改成手寫的比對，
// 並照 Python re.IGNORECASE 的規則比對大小寫（İ、ı 算 i，ſ 算 s，K（克氏溫標）算 k）。
package titles

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/zoosewu/kara-creator/nas/internal/pystr"
)

// Info 是辨識用的影片資訊（yt-dlp 的欄位；手動放入的檔案只有 Title = 檔名）。
type Info struct {
	Title    string   `json:"title"`
	Track    string   `json:"track"`
	Artist   string   `json:"artist"`
	Artists  []string `json:"artists"`
	Channel  string   `json:"channel"`
	Uploader string   `json:"uploader"`
}

// 辨識結果的來源。
const (
	SourceMetadata = "metadata" // yt-dlp 的歌曲資訊
	SourceTitle    = "title"    // 標題規則
	SourceFallback = "fallback" // 整個標題當歌名（不顯示標題畫面）
)

// Guess 是辨識結果。
type Guess struct {
	Title  string `json:"title"`
	Artist string `json:"artist"`
	Source string `json:"source"`
}

// FromInfo 推測歌名與演唱者。
func FromInfo(info Info) Guess {
	g := guess(info)
	title := stripFeatTitle(g.Title)
	if title == "" {
		title = g.Title
	}
	return Guess{title, mainArtist(g.Artist), g.Source}
}

func guess(info Info) Guess {
	track := pystr.Strip(info.Track)
	artists := info.Artists
	if len(artists) == 0 && info.Artist != "" {
		artists = []string{info.Artist}
	}
	if track != "" {
		for _, a := range artists {
			if a := pystr.Strip(a); a != "" {
				return Guess{track, a, SourceMetadata}
			}
		}
	}

	raw := pystr.JoinFields(info.Title)
	channel := info.Channel
	if channel == "" {
		channel = info.Uploader
	}
	channel = CleanChannel(channel)
	text := dropNoiseBrackets(raw)

	for _, pairs := range [][][2]string{quotes, square} {
		if start, _, inner, ok := firstBracket(text, pairs); ok {
			title := stripTranslation(clean(inner), true)
			artist := stripTranslation(clean(text[:start]), false)
			if artist == "" {
				artist = channel
			}
			if title != "" {
				return Guess{title, artist, SourceTitle}
			}
		}
	}

	var parts []string
	for _, p := range separatorRe.Split(text, -1) {
		if clean(p) != "" {
			parts = append(parts, p)
		}
	}
	if len(parts) >= 2 {
		left, right := clean(parts[0]), clean(parts[1])
		// 頻道名稱出現在右邊時代表是「歌名 - 演唱者」。
		if channel != "" && similar(right, channel) && !similar(left, channel) {
			left, right = right, left
		}
		return Guess{stripTranslation(right, true), stripTranslation(left, false), SourceTitle}
	}

	title := clean(text)
	if title == "" {
		title = raw
	}
	return Guess{title, channel, SourceFallback}
}

// ---- 雜訊詞 --------------------------------------------------------------------

// 括號內或頭尾出現時視為雜訊的詞（不分大小寫，長的要排前面：依序嘗試，先符合的優先）。
var noisePhrases = []string{
	"official music video", "official lyric video", "official lyrics video", "official audio",
	"official video", "official mv", "official m/v", "music video", "lyric video", "lyrics video",
	"music clip", "visualizer", "remastered", "remaster", "full version", "full ver.", "full ver",
	"short version", "short ver.", "short ver", "official", "audio", "lyrics", "lyric", "video",
	"m/v", "mv", "pv", "hd", "hq", "4k", "1080p", "720p", "teaser", "premiere",
	"官方完整版", "官方版", "官方", "完整版", "高音質", "高畫質", "動態歌詞", "歌詞版", "歌詞",
	"中日字幕", "中文字幕", "中字", "字幕", "首播",
}

var noiseRunes = func() [][]rune {
	out := make([][]rune, len(noisePhrases))
	for i, p := range noisePhrases {
		out[i] = []rune(p)
	}
	return out
}()

// isWord 同 Python re 的 \w（str）：字母、數字、底線（含中日韓文字）。
func isWord(r rune) bool { return r == '_' || unicode.IsLetter(r) || unicode.IsNumber(r) }

// foldEq 是 Python re.IGNORECASE 的單字元比對：pattern 是 ASCII 小寫字母或其他字元。
func foldEq(pattern, r rune) bool {
	if pattern == r {
		return true
	}
	if pattern < 'a' || pattern > 'z' {
		return false
	}
	switch {
	case r == pattern-'a'+'A':
		return true
	case pattern == 'i':
		return r == 'İ' || r == 'ı'
	case pattern == 's':
		return r == 'ſ'
	case pattern == 'k':
		return r == 'K'
	}
	return false
}

// isLatinCI 同 Python 加了 IGNORECASE 的 [A-Za-z]。
func isLatinCI(r rune) bool {
	return r < 0x80 && unicode.IsLetter(r) || r == 'İ' || r == 'ı' || r == 'ſ' || r == 'K'
}

// matchCI 從 s[i:] 比對 pattern（不分大小寫）；成功時回傳結尾位置。
func matchCI(s string, i int, pattern []rune) (int, bool) {
	for _, p := range pattern {
		if i >= len(s) {
			return 0, false
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if !foldEq(p, r) {
			return 0, false
		}
		i += size
	}
	return i, true
}

func runeBefore(s string, i int) (rune, bool) {
	if i == 0 {
		return 0, false
	}
	r, _ := utf8.DecodeLastRuneInString(s[:i])
	return r, true
}

func runeAt(s string, i int) (rune, bool) {
	if i >= len(s) {
		return 0, false
	}
	r, _ := utf8.DecodeRuneInString(s[i:])
	return r, true
}

// noiseAt 同 Python 的 (?<![\w])(?:雜訊詞…)(?![\w])：在位置 i 比對，回傳結尾位置。
func noiseAt(s string, i int) (int, bool) {
	if r, ok := runeBefore(s, i); ok && isWord(r) {
		return 0, false
	}
	for _, p := range noiseRunes {
		if e, ok := matchCI(s, i, p); ok {
			if r, ok := runeAt(s, e); !ok || !isWord(r) {
				return e, true
			}
		}
	}
	return 0, false
}

// replaceNoise 同 Python _NOISE.sub(repl, s)。
func replaceNoise(s, repl string) string {
	var sb strings.Builder
	for i := 0; i < len(s); {
		if e, ok := noiseAt(s, i); ok {
			sb.WriteString(repl)
			i = e
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		sb.WriteString(s[i : i+size])
		i += size
	}
	return sb.String()
}

func isTailDelim(r rune) bool {
	return pystr.IsSpace(r) || strings.ContainsRune("-–—|｜:：/", r)
}

func skip(s string, i int, f func(rune) bool) int {
	for i < len(s) {
		r, size := utf8.DecodeRuneInString(s[i:])
		if !f(r) {
			break
		}
		i += size
	}
	return i
}

// cutNoiseTail 同 Python re.sub(r"[分隔]*(?:雜訊詞)[分隔]*$", "", s)：去掉結尾的「- Official Video」這類雜訊。
func cutNoiseTail(s string) string {
	for p := 0; p < len(s); {
		// 雜訊詞不會以分隔字元開頭，所以前面的分隔字元一定整段吃掉（Python 回溯也只有這一種可能）。
		q := skip(s, p, isTailDelim)
		if r, ok := runeBefore(s, q); !ok || !isWord(r) {
			for _, phrase := range noiseRunes {
				e, ok := matchCI(s, q, phrase)
				if !ok {
					continue
				}
				if r, ok := runeAt(s, e); ok && isWord(r) {
					continue
				}
				if skip(s, e, isTailDelim) == len(s) {
					return s[:p]
				}
			}
		}
		_, size := utf8.DecodeRuneInString(s[p:])
		p += size
	}
	return s
}

func isNoise(inner string) bool {
	rest := replaceNoise(inner, "")
	return strings.IndexFunc(rest, func(r rune) bool {
		return !pystr.IsSpace(r) && !strings.ContainsRune("-–—|｜:：/.,&+", r)
	}) == -1
}

// ---- 括號 ----------------------------------------------------------------------

var (
	quotes = [][2]string{{"『", "』"}, {"「", "」"}, {"《", "》"}, {"〈", "〉"}, {"“", "”"}, {`"`, `"`}}
	square = [][2]string{{"【", "】"}, {"［", "］"}, {"[", "]"}, {"〔", "〕"}}
	round  = [][2]string{{"(", ")"}, {"（", "）"}}

	// 去掉雜訊括號時依序處理的括號（方括號、圓括號、引號類）。
	noiseBracketRes = func() []*regexp.Regexp {
		var out []*regexp.Regexp
		for _, pair := range slices.Concat(square, round, quotes[2:]) {
			o, c := pair[0], pair[1]
			out = append(out, regexp.MustCompile(regexp.QuoteMeta(o)+"([^"+regexp.QuoteMeta(o+c)+"]*)"+regexp.QuoteMeta(c)))
		}
		return out
	}()
)

// dropNoiseBrackets 去掉內容全是雜訊的括號，例如【Official】（Official Video）［MV］。
func dropNoiseBrackets(text string) string {
	for _, re := range noiseBracketRes {
		text = re.ReplaceAllStringFunc(text, func(m string) string {
			if isNoise(re.FindStringSubmatch(m)[1]) {
				return " "
			}
			return m
		})
	}
	return pystr.JoinFields(cutNoiseTail(text))
}

// firstBracket 找最前面的一組括號（內容不是雜訊）；回傳開括號的位置、閉括號的位置與內容。
func firstBracket(text string, pairs [][2]string) (start, end int, inner string, ok bool) {
	start = -1
	for _, pair := range pairs {
		i := strings.Index(text, pair[0])
		if i == -1 {
			continue
		}
		from := i + len(pair[0])
		j := strings.Index(text[from:], pair[1])
		if j == -1 {
			continue
		}
		j += from
		in := text[from:j]
		if !isNoise(in) && (start == -1 || i < start) {
			start, end, inner = i, j, in
		}
	}
	return start, end, inner, start != -1
}

var bracketCharsRe = regexp.MustCompile(`[\(\)（）\[\]［］【】]`)

// clean 去掉雜訊詞與頭尾的分隔符號、括號殘留。
func clean(text string) string {
	text = replaceNoise(text, " ")
	text = bracketCharsRe.ReplaceAllString(text, " ")
	return strings.Trim(pystr.JoinFields(text), " -–—|｜:：/,.")
}

// ---- 分隔、譯名、合作歌手、頻道 ------------------------------------------------

var (
	separatorRe = regexp.MustCompile(`[` + pystr.SpaceClass + `]+[-–—|｜][` + pystr.SpaceClass + `]+|[` + pystr.SpaceClass + `]*[｜|][` + pystr.SpaceClass + `]*`)
	cjkRe       = regexp.MustCompile(`[\x{3040}-\x{30ff}\x{3400}-\x{4dbf}\x{4e00}-\x{9fff}\x{ac00}-\x{d7af}]`)
)

// stripTranslation 中英並列時去掉英文譯名：「周杰倫 Jay Chou」→「周杰倫」。
// 歌名只去掉後面的英文（keepLeading），避免「I love you 伝えたい」這類歌名被切掉。
func stripTranslation(text string, keepLeading bool) string {
	tokens := pystr.Fields(text)
	if !slices.ContainsFunc(tokens, cjkRe.MatchString) {
		return text
	}
	latin := func(t string) bool { return !cjkRe.MatchString(t) }
	for len(tokens) > 0 && latin(tokens[len(tokens)-1]) {
		tokens = tokens[:len(tokens)-1]
	}
	for !keepLeading && len(tokens) > 0 && latin(tokens[0]) {
		tokens = tokens[1:]
	}
	return strings.Join(tokens, " ")
}

func similar(a, b string) bool {
	a, b = strings.ReplaceAll(pystr.Casefold(a), " ", ""), strings.ReplaceAll(pystr.Casefold(b), " ", "")
	return a != "" && b != "" && (strings.Contains(b, a) || strings.Contains(a, b))
}

var featWords = [][]rune{[]rune("ft"), []rune("feat"), []rune("featuring")}

// featAt 同 Python 的 (?<![A-Za-z])(?:ft|feat|featuring)(?![A-Za-z])（IGNORECASE）：回傳結尾位置。
func featAt(s string, k int) (int, bool) {
	if r, ok := runeBefore(s, k); ok && isLatinCI(r) {
		return 0, false
	}
	for _, w := range featWords {
		if e, ok := matchCI(s, k, w); ok {
			if r, ok := runeAt(s, e); !ok || !isLatinCI(r) {
				return e, true
			}
		}
	}
	return 0, false
}

// cutFeatTail 同 Python re.sub(r"\s*" + FEAT + r"\.?.*$", "", s)：去掉 ft. 之後的全部。
func cutFeatTail(s string) string {
	runStart := 0 // 目前這段連續空白的開頭
	for k := 0; k < len(s); {
		r, size := utf8.DecodeRuneInString(s[k:])
		if e, ok := featAt(s, k); ok {
			// \.?.*$：. 不含換行；$ 可以在結尾或結尾的換行之前。
			if e < len(s) && s[e] == '.' {
				e++
			}
			if nl := strings.IndexByte(s[e:], '\n'); nl == -1 {
				return s[:runStart]
			} else if e+nl == len(s)-1 {
				return s[:runStart] + "\n"
			}
		}
		k += size
		if !pystr.IsSpace(r) {
			runStart = k
		}
	}
	return s
}

func isFeatOpen(r rune) bool  { return strings.ContainsRune("([（【", r) }
func isFeatClose(r rune) bool { return strings.ContainsRune(")]）】", r) }

// cutFeatBrackets 同 Python re.sub(r"\s*[\(\[（【]\s*" + FEAT + r"[^\)\]）】]*[\)\]）】]", "", s)。
func cutFeatBrackets(s string) string {
	var sb strings.Builder
	for p := 0; p < len(s); {
		q := skip(s, p, pystr.IsSpace)
		if r, ok := runeAt(s, q); ok && isFeatOpen(r) {
			k := skip(s, q+utf8.RuneLen(r), pystr.IsSpace)
			if e, ok := featAt(s, k); ok {
				if c := strings.IndexFunc(s[e:], isFeatClose); c != -1 {
					_, size := utf8.DecodeRuneInString(s[e+c:])
					p = e + c + size
					continue
				}
			}
		}
		_, size := utf8.DecodeRuneInString(s[p:])
		sb.WriteString(s[p : p+size])
		p += size
	}
	return sb.String()
}

// mainArtist：「李榮浩 Ronghao Li ft. 張惠妹 aMEI」→「李榮浩」：去掉 ft. 之後的合作歌手與英文譯名。
func mainArtist(name string) string {
	if s := stripTranslation(strings.Trim(cutFeatTail(name), " -–—,，、&"), false); s != "" {
		return s
	}
	return name
}

// stripFeatTitle：「Uptown Funk (feat. Bruno Mars)」、「Uptown Funk ft. Bruno Mars」→「Uptown Funk」。
func stripFeatTitle(title string) string {
	return strings.Trim(cutFeatTail(cutFeatBrackets(title)), " -–—,，")
}

// ci 把 ASCII 字母換成照 Python IGNORECASE 規則不分大小寫的字元類別。
func ci(pattern string) string {
	var sb strings.Builder
	for _, r := range pattern {
		if r < 'a' || r > 'z' {
			sb.WriteRune(r)
			continue
		}
		sb.WriteString("[" + string(r) + string(r-'a'+'A'))
		switch r {
		case 'i':
			sb.WriteString("İı")
		case 's':
			sb.WriteString("ſ")
		case 'k':
			sb.WriteString("K")
		}
		sb.WriteString("]")
	}
	return sb.String()
}

var channelSuffixRe = func() *regexp.Regexp {
	sp := `[` + pystr.SpaceClass + `]`
	return regexp.MustCompile(sp + `*(?:-` + sp + `*` + ci("topic") + `|` + ci("official") + sp + `+` + ci("youtube") + sp + `+` + ci("channel") +
		`|` + ci("youtube") + sp + `+` + ci("channel") + `|` + ci("official") + sp + `+` + ci("channel") + `|` + ci("official") +
		`|` + ci("vevo") + `|官方頻道|官方|` + ci("channel") + `)` + sp + `*$`)
}()

// CleanChannel 去掉頻道名稱結尾的 - Topic、Official Channel、VEVO 等，以及英文譯名。
func CleanChannel(name string) string {
	name = pystr.JoinFields(name)
	for {
		stripped := pystr.Strip(channelSuffixRe.ReplaceAllString(name, ""))
		if stripped == name || stripped == "" {
			return stripTranslation(name, false)
		}
		name = stripped
	}
}
