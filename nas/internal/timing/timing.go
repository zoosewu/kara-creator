// Package timing 是對時結果（alignment.json）與手動調整時間：移動單句（連鎖推動、壓縮）、
// 套用 AI 重對的結果（v1 songtool/karaoke.py 的 shift_timing、retime；黃金測試見 timing_test.go）。
//
// 調整只改 alignment.json，之後成品變成需更新（只重新產生字幕並燒錄，不重新對時）。
package timing

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// Alignment 是 alignment.json 的內容。
type Alignment struct {
	Key         string       `json:"key"`      // 對時指紋（fingerprint.Align）；變了就整首重新對時
	Lyrics      string       `json:"lyrics"`   // 對時當下的歌詞指紋：AI 重對前確認歌詞沒改過
	Language    string       `json:"language"` // 對時當下的語言、方法（versions.align）、模型：資料備份還原時用
	Method      int          `json:"method"`
	Model       string       `json:"model"`
	Lines       []wp.Line    `json:"lines"`
	Adjustments []Adjustment `json:"adjustments,omitempty"`
	Restored    *Restored    `json:"restored,omitempty"`
}

// Adjustment 是一次手動調整或 AI 重對的紀錄。
type Adjustment struct {
	Line      int      `json:"line"`
	Text      string   `json:"text"`
	Delta     *float64 `json:"delta,omitempty"`     // 移動
	Following *bool    `json:"following,omitempty"` // 移動：連同之後全部
	Pushed    *[]int   `json:"pushed,omitempty"`    // 移動：一起被推動的句子
	Retime    string   `json:"retime,omitempty"`    // AI 重對：from | line
	Anchor    *float64 `json:"anchor,omitempty"`    // AI 重對：以這個時間為開頭
	At        string   `json:"at"`
}

// Restored 表示這份對時是從資料備份還原的：歌詞、語言、方法都相同就沿用（不重新對時）。
type Restored struct {
	Lyrics   string `json:"lyrics"` // 歌詞指紋
	Language string `json:"language"`
	Method   int    `json:"method"` // versions.align
}

// LineText 是一句實際的文字：以逐字的內容為準（Whisper 有時會把整句的 text 切歪，但逐字是照歌詞切的）。
func LineText(ln wp.Line) string {
	if len(ln.Words) == 0 {
		return ln.Text
	}
	var sb strings.Builder
	for _, w := range ln.Words {
		sb.WriteString(w.Text)
	}
	return sb.String()
}

// 一句的最短長度：取兩者較大的。
const (
	MinLine    = 0.3  // 秒
	MinPerWord = 0.08 // 每個字（秒）
)

// round3 同 Python round(x, 3)（正確捨入，剛好一半時取偶數）。
func round3(x float64) float64 {
	v, _ := strconv.ParseFloat(strconv.FormatFloat(x, 'f', 3, 64), 64)
	return v
}

func minLength(ln wp.Line) float64 {
	return math.Max(MinLine, MinPerWord*float64(max(len(ln.Words), 1)))
}

func move(ln *wp.Line, delta float64) {
	ln.Start = round3(ln.Start + delta)
	ln.End = round3(ln.End + delta)
	for i := range ln.Words {
		ln.Words[i].Start = round3(ln.Words[i].Start + delta)
		ln.Words[i].End = round3(ln.Words[i].End + delta)
	}
}

// fitBefore：這句唱到 limit（下一句開頭）之後的話，依比例壓縮每個字的時間，讓它在 limit 前唱完。
func fitBefore(ln *wp.Line, limit float64) {
	end := limit - 0.02
	if ln.End <= end || ln.End <= ln.Start {
		return
	}
	factor := (end - ln.Start) / (ln.End - ln.Start)
	origin := ln.Start
	for i := range ln.Words {
		ln.Words[i].Start = round3(origin + (ln.Words[i].Start-origin)*factor)
		ln.Words[i].End = round3(origin + (ln.Words[i].End-origin)*factor)
	}
	ln.End = round3(end)
}

// ErrInvalid 是使用者操作不合法（訊息直接給使用者看）。
var ErrInvalid = errors.New("")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w%s", ErrInvalid, fmt.Sprintf(format, args...))
}

// Shift 把第 line 句（following 時連同之後所有句子）整句平移 delta 秒，回傳一起被推動的句子（從 0 起算）。
//
// 只移一句時不會被前後的句子擋住：
//   - 往後移：這句太短或撞到下一句，就把下一句往後推到最近的合法位置，再撞到就繼續往後推
//   - 往前移：撞到上一句（上一句剩下的長度不夠），就把上一句往前推到最近的合法位置，再撞到就繼續往前推
//
// 被推動的句子唱不完的部分依比例壓縮到下一句開頭之前。只有推到歌曲開頭都放不下時才會拒絕。
// 出錯時 a 不會被修改。
func (a *Alignment) Shift(line int, delta float64, following bool, now string) ([]int, error) {
	lines := a.Lines
	if line < 0 || line >= len(lines) {
		return nil, invalid("沒有這一句")
	}
	delta = round3(delta)
	if math.Abs(delta) < 0.001 {
		return nil, invalid("移動量是 0")
	}
	start := lines[line].Start
	newStart := start + delta
	// 往前最多推到：前面每一句都只剩最短長度、第一句從 0 秒開始（只移一句時）
	floor := 0.0
	if !following {
		for _, ln := range lines[:line] {
			floor += minLength(ln)
		}
	}
	if following && line > 0 {
		floor = lines[line-1].Start + minLength(lines[line-1])
	}
	if newStart < floor {
		where := "歌曲開頭（前面的句子都已經擠到最短）"
		if following && line > 0 {
			where = "上一句開始"
		}
		return nil, invalid("最多只能往前移 %.2f 秒（不能早於%s）", math.Max(0, start-floor), where)
	}

	pushed := []int{}
	if following {
		for k := line; k < len(lines); k++ {
			move(&lines[k], delta)
		}
	} else {
		move(&lines[line], delta)
		// 往後推：這句太短或撞到下一句時，下一句移到最近的合法位置，連鎖往後
		for k := line; k+1 < len(lines); k++ {
			need := lines[k].Start + minLength(lines[k])
			if lines[k+1].Start >= need {
				break
			}
			move(&lines[k+1], need-lines[k+1].Start)
			pushed = append(pushed, k+1)
		}
		// 往前推：撞到上一句（上一句剩下的長度不夠）時，上一句往前移到最近的合法位置，連鎖往前
		for k := line; k > 0; k-- {
			latest := lines[k].Start - minLength(lines[k-1])
			if lines[k-1].Start <= latest {
				break
			}
			move(&lines[k-1], latest-lines[k-1].Start)
			pushed = append(pushed, k-1)
		}
		// 被移動的句子（和它的上一句）唱不完的部分壓縮到下一句開頭之前
		fit := map[int]bool{line: true}
		if line > 0 {
			fit[line-1] = true
		}
		for _, m := range pushed {
			fit[m] = true
			if m > 0 {
				fit[m-1] = true
			}
		}
		var order []int
		for k := range fit {
			order = append(order, k)
		}
		sort.Ints(order)
		for _, k := range order {
			if k+1 < len(lines) {
				fitBefore(&lines[k], lines[k+1].Start)
			}
		}
	}
	p := append(make([]int, 0, len(pushed)), pushed...)
	a.Adjustments = append(a.Adjustments, Adjustment{Line: line, Text: lines[line].Text, Delta: &delta,
		Following: &following, Pushed: &p, At: now})
	return pushed, nil
}

// 重對方式。
const (
	RetimeFrom = "from" // 重新對時這句及之後全部
	RetimeLine = "line" // 只重對這一句
)

// RetimeModes 是重對方式的說明。
var RetimeModes = map[string]string{RetimeFrom: "重新對時這句及之後全部", RetimeLine: "只重對這一句"}

// ApplyFrom 套用 align_from 的結果：第 line 句之後換成 fresh，上一句的尾音截到 anchor（不能拖過新的起點）。
func (a *Alignment) ApplyFrom(line int, anchor float64, fresh []wp.Line, now string) error {
	if line < 0 || line >= len(a.Lines) {
		return invalid("沒有這一句")
	}
	if line+len(fresh) != len(a.Lines) {
		return invalid("AI 重對的結果句數不符")
	}
	if line > 0 {
		prev := &a.Lines[line-1]
		if n := len(prev.Words); n > 0 {
			tail := &prev.Words[n-1]
			tail.End = round3(math.Max(tail.Start+0.05, math.Min(tail.End, anchor)))
			prev.End = tail.End
		}
	}
	a.Lines = append(a.Lines[:line], fresh...)
	a.Adjustments = append(a.Adjustments, Adjustment{Line: line, Text: a.Lines[line].Text, Retime: RetimeFrom, Anchor: &anchor, At: now})
	return nil
}

// ApplyLine 套用 align_line 的結果。
func (a *Alignment) ApplyLine(line int, anchor float64, fresh wp.Line, now string) error {
	if line < 0 || line >= len(a.Lines) {
		return invalid("沒有這一句")
	}
	a.Lines[line] = fresh
	a.Adjustments = append(a.Adjustments, Adjustment{Line: line, Text: fresh.Text, Retime: RetimeLine, Anchor: &anchor, At: now})
	return nil
}

// Clone 回傳深複本。
func (a *Alignment) Clone() *Alignment {
	c := *a
	c.Lines = make([]wp.Line, len(a.Lines))
	for i, ln := range a.Lines {
		ln.Words = append([]wp.Word(nil), ln.Words...)
		c.Lines[i] = ln
	}
	c.Adjustments = append([]Adjustment(nil), a.Adjustments...)
	if a.Restored != nil {
		r := *a.Restored
		c.Restored = &r
	}
	return &c
}
