// Package fingerprint 算各階段的指紋（docs/data.md「指紋」）。指紋只看內容，不看路徑和修改時間，
// 整個曲庫搬到哪裡都一樣（規格測試見 fingerprint_test.go）。
package fingerprint

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	"github.com/zoosewu/kara-creator/nas/internal/lyrics"
	"github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

func sum(text string) string {
	s := sha256.Sum256([]byte(text))
	return hex.EncodeToString(s[:])
}

// Ms 把秒換成整數毫秒（四捨五入，.5 進位）。
func Ms(t float64) int64 { return int64(math.Floor(t*1000 + 0.5)) }

// Field 是指紋的一個欄位。
type Field struct {
	Name  string
	Value any
}

var escaper = strings.NewReplacer(`\`, `\\`, "\n", `\n`)

// H 依序串接「名稱=值\n」再算 sha256。值裡的換行與反斜線會跳脫。
func H(fields ...Field) string {
	var sb strings.Builder
	for _, f := range fields {
		sb.WriteString(f.Name + "=" + escaper.Replace(fmt.Sprint(f.Value)) + "\n")
	}
	return sum(sb.String())
}

// Lyrics 是歌詞指紋：要唱的句子文字 + 手動讀音（演唱者、翻譯、註解、標題不算——它們不影響對時）。
func Lyrics(texts []string, rubies [][]lyrics.Ruby) string {
	parts := make([]string, len(texts))
	for i, text := range texts {
		var rs []lyrics.Ruby
		if i < len(rubies) {
			rs = append(rs, rubies[i]...)
		}
		sort.SliceStable(rs, func(a, b int) bool { return rs[a].Start < rs[b].Start })
		var sb strings.Builder
		sb.WriteString(text)
		for _, r := range rs {
			fmt.Fprintf(&sb, "\x1f%d\x1f%d\x1f%s", r.Start, r.End, r.Reading)
		}
		parts[i] = sb.String()
	}
	return sum(strings.Join(parts, "\x1e"))
}

// Alignment 是對時內容指紋：每句與每個字的時間（毫秒）與字。
func Alignment(lines []workerproto.Line) string {
	parts := make([]string, len(lines))
	for i, ln := range lines {
		var sb strings.Builder
		fmt.Fprintf(&sb, "%d\x1f%d", Ms(ln.Start), Ms(ln.End))
		for _, w := range ln.Words {
			fmt.Fprintf(&sb, "\x1d%s\x1f%d\x1f%d", w.Text, Ms(w.Start), Ms(w.End))
		}
		parts[i] = sb.String()
	}
	return sum(strings.Join(parts, "\x1e"))
}

// Items 是清單：各項以 \x1f 串接，nil 當成空字串。
func Items(values []*string) string {
	parts := make([]string, len(values))
	for i, v := range values {
		if v != nil {
			parts[i] = *v
		}
	}
	return strings.Join(parts, "\x1f")
}

// Separate 是去人聲的指紋。
func Separate(source, model string, stems, version int) string {
	return H(Field{"stage", "separate"}, Field{"source", source}, Field{"model", model}, Field{"stems", stems}, Field{"version", version})
}

// Align 是對時的指紋：變了就整首重新對時。
func Align(lyricsFP, vocals, model, language string, version int) string {
	return H(Field{"stage", "align"}, Field{"lyrics", lyricsFP}, Field{"vocals", vocals}, Field{"model", model},
		Field{"language", language}, Field{"version", version})
}

// RenderInput 是成品指紋的內容。
type RenderInput struct {
	Target       string
	Media        string // 伴奏或來源的 sha256
	Alignment    string // 對時內容指紋
	Lyrics       string // 歌詞指紋
	Singers      string // Items
	Translations string // Items；不燒翻譯時為空
	Title        string // 標題畫面不顯示時為空
	Artist       string
	Note         string
	Scale        float64 // 字幕大小
	Font         string  // 字型檔 sha256
	Size         string  // 寬x高
	Version      int     // versions.render
	Reading      int     // versions.reading：日文的假名由 worker 燒錄時自己算，讀音規則改了成品也會變
	ASS          string  // 使用者手動改過的 ASS 的 sha256（沒有時為空）
}

// Render 是成品（字幕 + 燒錄）的指紋：變了只重新產生字幕與燒錄。
// 手動改過 ASS 時畫面完全由那份 ASS 決定，對時、歌詞、標題畫面、樣式、字型都不算。
func Render(in RenderInput) string {
	if in.ASS != "" {
		return H(Field{"stage", "render"}, Field{"target", in.Target}, Field{"media", in.Media}, Field{"ass", in.ASS},
			Field{"size", in.Size}, Field{"version", in.Version})
	}
	return H(Field{"stage", "render"}, Field{"target", in.Target}, Field{"media", in.Media}, Field{"alignment", in.Alignment},
		Field{"lyrics", in.Lyrics}, Field{"singers", in.Singers}, Field{"translations", in.Translations},
		Field{"title", in.Title}, Field{"artist", in.Artist}, Field{"note", in.Note},
		Field{"scale", strconv.FormatFloat(in.Scale, 'f', 2, 64)}, Field{"font", in.Font}, Field{"size", in.Size},
		Field{"version", in.Version}, Field{"reading", in.Reading})
}

// QA 是對時檢查的指紋。
func QA(alignment, lyricsFP, vocals, language, model string, version int) string {
	return H(Field{"stage", "qa"}, Field{"alignment", alignment}, Field{"lyrics", lyricsFP}, Field{"vocals", vocals},
		Field{"language", language}, Field{"model", model}, Field{"version", version})
}

// Approve 是「已確認」的指紋：歌詞指紋、語言、對時方法、這次整首對時的編號。
// 只需重燒的更新（演唱者、翻譯、標題畫面、字型、字幕大小、手動調時間、AI 重對某幾句）不影響確認；
// 整首重新對時會換編號，所以確認失效（確認紀錄另外保留）。不含人聲：從資料備份還原、沿用對時時確認才能延續
// （需要重新對時的狀態由 planner 另外判定為失效）。
func Approve(lyricsFP, language string, method int, run string) string {
	return H(Field{"stage", "approve"}, Field{"lyrics", lyricsFP}, Field{"language", language}, Field{"method", method},
		Field{"run", run})
}
