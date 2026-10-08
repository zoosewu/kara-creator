package timing

import (
	"errors"
	"math"

	"github.com/zoosewu/kara-creator/nas/internal/difflib"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// RetimeLyrics 是改了歌詞之後的局部重對（調整紀錄的 retime）。
const RetimeLyrics = "lyrics"

// 局部重對：改了歌詞時只重對改到的句子，其他句子的時間（含手動調整）一律不動。
//
//  1. 用每句歌詞的指紋（LineLyrics）逐句比對新舊歌詞（difflib）
//  2. 沒改的句子沿用原本的時間；刪掉的句子拿掉；改了或新增的句子交給 AI，範圍在前後沒改的句子之間
//  3. 手動調過的句子（移動過、被連鎖推動）改了字時保留開頭，只重對句內的字

// Gap 是要交給 AI 的一段：新歌詞的第 From～To-1 句（連續改到的句子算同一段，前後都是沒改的句子或歌曲的頭尾）。
// Pins 是段落裡手動調過的句子（新的句子編號 → 原本的開頭）：保留開頭，只重對句內的字。
type Gap struct {
	From, To int
	Pins     map[int]float64
}

// Patch 是局部重對的計畫。Lines 是新歌詞每一句的時間：沒改的已經填好，Gaps 裡的句子等 Fill 填入。
type Patch struct {
	Lines   []wp.Line
	Gaps    []Gap
	Removed int // 刪掉的句子數
	keep    []int
	adj     []Adjustment
	filled  []bool
}

// ErrNoPatch 表示沒辦法局部重對（沒有逐句的歌詞紀錄），要整首重新對時。
var ErrNoPatch = errors.New("沒有逐句的歌詞紀錄")

// ErrNoRoom 表示前後句之間沒有空檔放要重對的句子，要整首重新對時。
var ErrNoRoom = errors.New("前後句之間沒有空檔")

// Realign 是要交給 AI 的句子數。
func (p *Patch) Realign() int {
	n := 0
	for _, g := range p.Gaps {
		n += g.To - g.From
	}
	return n
}

// PlanPatch 比對對時當下的歌詞（a.LineLyrics）和新歌詞（每句的指紋），產生局部重對的計畫。
func (a *Alignment) PlanPatch(newKeys []string) (*Patch, error) {
	if len(a.LineLyrics) != len(a.Lines) || len(a.Lines) == 0 {
		return nil, ErrNoPatch
	}
	adjusted := a.adjusted()
	p := &Patch{Lines: make([]wp.Line, len(newKeys)), keep: make([]int, len(a.Lines)), filled: make([]bool, len(newKeys))}
	for i := range p.keep {
		p.keep[i] = -1 // 舊的第 i 句對應到新的第幾句（-1 = 刪掉了）
	}
	pins := map[int]float64{}
	for _, op := range difflib.Opcodes(a.LineLyrics, newKeys) {
		switch op.Tag {
		case difflib.Equal:
			for k := 0; k < op.I2-op.I1; k++ {
				p.Lines[op.J1+k] = cloneLine(a.Lines[op.I1+k])
				p.filled[op.J1+k] = true
				p.keep[op.I1+k] = op.J1 + k
			}
		case difflib.Delete:
			p.Removed += op.I2 - op.I1
		case difflib.Insert:
		case difflib.Replace:
			if op.I2-op.I1 != op.J2-op.J1 { // 句數不同：沒辦法一句對一句，當成刪掉再新增
				p.Removed += max(0, (op.I2-op.I1)-(op.J2-op.J1))
				continue
			}
			for k := 0; k < op.I2-op.I1; k++ { // 改了字：一句對一句，手動調過的保留開頭
				p.keep[op.I1+k] = op.J1 + k
				if adjusted[op.I1+k] {
					pins[op.J1+k] = a.Lines[op.I1+k].Start
				}
			}
		}
	}
	for j := 0; j < len(newKeys); j++ { // 連續沒有時間的句子算同一段
		if p.filled[j] {
			continue
		}
		g := Gap{From: j}
		for j < len(newKeys) && !p.filled[j] {
			if pin, ok := pins[j]; ok {
				if g.Pins == nil {
					g.Pins = map[int]float64{}
				}
				g.Pins[j] = pin
			}
			j++
		}
		g.To = j
		p.Gaps = append(p.Gaps, g)
	}
	// 調整紀錄的句子編號跟著換（刪掉的句子的紀錄拿掉），之後才知道哪幾句是手動調過的
	for _, adj := range a.Adjustments {
		if adj.Line < 0 || adj.Line >= len(p.keep) || p.keep[adj.Line] < 0 {
			continue
		}
		adj.Line = p.keep[adj.Line]
		if adj.Pushed != nil {
			pushed := []int{}
			for _, k := range *adj.Pushed {
				if k >= 0 && k < len(p.keep) && p.keep[k] >= 0 {
					pushed = append(pushed, p.keep[k])
				}
			}
			adj.Pushed = &pushed
		}
		p.adj = append(p.adj, adj)
	}
	return p, nil
}

// Window 是一段的範圍：從前一句的結尾（第一句是手動調過的句子時從它的開頭）到下一句的開頭；t1 為 nil 代表到歌曲結尾。
func (p *Patch) Window(g Gap) (t0 float64, t1 *float64, err error) {
	if pin, ok := g.Pins[g.From]; ok {
		t0 = pin
	} else if g.From > 0 {
		t0 = p.Lines[g.From-1].End
	}
	if g.To < len(p.Lines) {
		next := p.Lines[g.To].Start
		t1 = &next
		if next-t0 < MinLine*float64(g.To-g.From) {
			return 0, nil, ErrNoRoom
		}
	}
	return t0, t1, nil
}

// Fill 填入 AI 對這一段的結果（整首的絕對時間）：手動調過的句子開頭拉回原本的時間（前一句唱過這個開頭的部分壓縮到它之前），
// 最後一句唱過下一句開頭的部分也壓縮到它之前。
func (p *Patch) Fill(g Gap, lines []wp.Line) error {
	if len(lines) != g.To-g.From {
		return errors.New("AI 重對的結果句數不符")
	}
	for k, ln := range lines {
		p.Lines[g.From+k] = cloneLine(ln)
		p.filled[g.From+k] = true
	}
	for j := g.From; j < g.To; j++ {
		if pin, ok := g.Pins[j]; ok {
			pinStart(&p.Lines[j], pin)
			if j > 0 {
				fitBefore(&p.Lines[j-1], pin)
			}
		}
	}
	if g.To < len(p.Lines) {
		fitBefore(&p.Lines[g.To-1], p.Lines[g.To].Start)
	}
	return nil
}

// Apply 把填好的計畫寫回對時：換成新的句子與歌詞紀錄，「整首對時的編號」（Run）不變（已確認的判斷靠它）。
func (a *Alignment) Apply(p *Patch, key, lyrics string, lineLyrics []string, now string) error {
	for _, ok := range p.filled {
		if !ok {
			return errors.New("還有句子沒有對時結果")
		}
	}
	a.Key, a.Lyrics, a.LineLyrics = key, lyrics, lineLyrics
	a.Lines = p.Lines
	a.Adjustments = p.adj
	for _, g := range p.Gaps {
		for j := g.From; j < g.To; j++ {
			adj := Adjustment{Line: j, Text: LineText(a.Lines[j]), Retime: RetimeLyrics, At: now}
			if pin, ok := g.Pins[j]; ok {
				adj.Anchor = &pin // 保留開頭：之後還是算手動調過
			}
			a.Adjustments = append(a.Adjustments, adj)
		}
	}
	return nil
}

// adjusted 回傳手動調過時間的句子：移動過、被連鎖推動、連同之後全部移動；之後又被 AI 重對（不保留開頭）的不算。
func (a *Alignment) adjusted() map[int]bool {
	out := map[int]bool{}
	for _, adj := range a.Adjustments {
		switch {
		case adj.Delta != nil:
			out[adj.Line] = true
			if adj.Following != nil && *adj.Following {
				for k := adj.Line; k < len(a.Lines); k++ {
					out[k] = true
				}
			}
			if adj.Pushed != nil {
				for _, k := range *adj.Pushed {
					out[k] = true
				}
			}
		case adj.Retime == RetimeFrom:
			for k := adj.Line; k < len(a.Lines); k++ {
				delete(out, k)
			}
		case adj.Retime == RetimeLine, adj.Retime == RetimeLyrics && adj.Anchor == nil:
			delete(out, adj.Line)
		}
	}
	return out
}

// pinStart 讓這句從 start 開始：第一個字的開頭拉到 start（不超過它自己的結尾），早於 start 的字一起往後挪。
func pinStart(ln *wp.Line, start float64) {
	start = round3(start)
	for i := range ln.Words {
		w := &ln.Words[i]
		if w.Start < start {
			w.Start = start
		}
		if w.End < w.Start {
			w.End = w.Start
		}
	}
	if len(ln.Words) > 0 {
		ln.Words[0].Start = start
	}
	ln.Start = start
	ln.End = math.Max(ln.End, start)
	if n := len(ln.Words); n > 0 {
		ln.End = math.Max(ln.End, ln.Words[n-1].End)
	}
}

// Offset 把每一句整句平移 dt 秒（剪出來的片段對時之後換回整首的時間）。
func Offset(lines []wp.Line, dt float64) {
	for i := range lines {
		move(&lines[i], dt)
	}
}

func cloneLine(ln wp.Line) wp.Line {
	ln.Words = append([]wp.Word(nil), ln.Words...)
	return ln
}
