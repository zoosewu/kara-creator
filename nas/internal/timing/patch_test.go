package timing

import (
	"errors"
	"reflect"
	"testing"

	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

func pl(text string, start, end float64) wp.Line {
	return wp.Line{Text: text, Start: start, End: end, Words: []wp.Word{{Text: text, Start: start, End: end}}}
}

// 四句：甲 1–3、乙 4–6、丙 8–10、丁 12–14（逐句的歌詞指紋就用文字代替）
func fourLines() *Alignment {
	return &Alignment{Lines: []wp.Line{pl("甲", 1, 3), pl("乙", 4, 6), pl("丙", 8, 10), pl("丁", 12, 14)},
		LineLyrics: []string{"甲", "乙", "丙", "丁"}}
}

func f(v float64) *float64 { return &v }

func span(g Gap) [2]int { return [2]int{g.From, g.To} }

func TestPatchEditOneLine(t *testing.T) {
	a := fourLines()
	p, err := a.PlanPatch([]string{"甲", "乙", "丙改", "丁"})
	if err != nil || len(p.Gaps) != 1 || span(p.Gaps[0]) != [2]int{2, 3} || p.Gaps[0].Pins != nil || p.Removed != 0 || p.Realign() != 1 {
		t.Fatalf("%+v %v", p, err)
	}
	t0, t1, err := p.Window(p.Gaps[0])
	if err != nil || t0 != 6 || *t1 != 12 {
		t.Fatalf("範圍要在前後沒改的句子之間：%v %v %v", t0, t1, err)
	}
	if err := p.Fill(p.Gaps[0], []wp.Line{pl("丙改", 7.5, 13)}); err != nil {
		t.Fatal(err)
	}
	if err := a.Apply(p, "新指紋", "新歌詞", []string{"甲", "乙", "丙改", "丁"}, "now"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Lines[0], pl("甲", 1, 3)) || !reflect.DeepEqual(a.Lines[3], pl("丁", 12, 14)) {
		t.Fatal("沒改的句子不動")
	}
	if a.Lines[2].Start != 7.5 || a.Lines[2].End >= 12 {
		t.Fatalf("唱過下一句開頭的部分要壓縮：%+v", a.Lines[2])
	}
	if a.Key != "新指紋" || a.LineLyrics[2] != "丙改" || a.Adjustments[0].Retime != RetimeLyrics {
		t.Fatalf("%+v", a)
	}
}

func TestPatchKeepsAdjustedStart(t *testing.T) {
	a := fourLines()
	// 手動把丙往後移 0.5 秒（8.5 開始），之後改丙的字：保留 8.5 的開頭，只重對句內的字
	if _, err := a.Shift(2, 0.5, false, "t1"); err != nil {
		t.Fatal(err)
	}
	p, _ := a.PlanPatch([]string{"甲", "乙", "丙改", "丁"})
	if len(p.Gaps) != 1 || p.Gaps[0].Pins[2] != 8.5 {
		t.Fatalf("%+v", p.Gaps)
	}
	if t0, _, _ := p.Window(p.Gaps[0]); t0 != 8.5 {
		t.Fatalf("從手動調的開頭開始對：%v", t0)
	}
	// AI 的結果比 8.5 早開始：開頭拉回 8.5
	_ = p.Fill(p.Gaps[0], []wp.Line{{Text: "丙改", Start: 8.2, End: 10, Words: []wp.Word{{Text: "丙", Start: 8.2, End: 9}, {Text: "改", Start: 9, End: 10}}}})
	_ = a.Apply(p, "k", "l", []string{"甲", "乙", "丙改", "丁"}, "t2")
	if got := a.Lines[2]; got.Start != 8.5 || got.Words[0].Start != 8.5 || got.Words[1].Start != 9 {
		t.Fatalf("%+v", got)
	}
	// 再改一次：還是算手動調過（保留開頭的重對不會清掉）
	p, _ = a.PlanPatch([]string{"甲", "乙", "丙又改", "丁"})
	if _, ok := p.Gaps[0].Pins[2]; !ok {
		t.Fatal("手動調過的句子一直保留開頭")
	}
}

func TestPatchInsertAndDelete(t *testing.T) {
	a := fourLines()
	_, _ = a.Shift(2, 0.5, false, "t") // 丙手動調過
	// 乙和丙之間插一句，刪掉丁
	p, err := a.PlanPatch([]string{"甲", "乙", "新", "丙"})
	if err != nil || p.Removed != 1 || len(p.Gaps) != 1 || span(p.Gaps[0]) != [2]int{2, 3} {
		t.Fatalf("%+v %v", p, err)
	}
	if t0, t1, _ := p.Window(p.Gaps[0]); t0 != 6 || *t1 != 8.5 {
		t.Fatalf("新句子放在乙的結尾和丙（手動調過的開頭）之間：%v %v", t0, *t1)
	}
	_ = p.Fill(p.Gaps[0], []wp.Line{pl("新", 6.2, 8)})
	_ = a.Apply(p, "k", "l", []string{"甲", "乙", "新", "丙"}, "t")
	if len(a.Lines) != 4 || a.Lines[3].Start != 8.5 {
		t.Fatalf("%+v", a.Lines)
	}
	if adj := a.Adjustments[0]; adj.Line != 3 || adj.Delta == nil {
		t.Fatalf("手動調整的紀錄跟著換到新的句子編號：%+v", a.Adjustments)
	}
	// 只刪句子：不需要 AI
	p, _ = a.PlanPatch([]string{"甲", "新", "丙"})
	if p.Realign() != 0 || p.Removed != 1 || a.Apply(p, "k", "l", []string{"甲", "新", "丙"}, "t") != nil || len(a.Lines) != 3 {
		t.Fatalf("%+v", p)
	}
}

func TestPatchConsecutiveLines(t *testing.T) {
	a := fourLines()
	_, _ = a.Shift(1, -0.5, false, "t") // 乙手動調過（3.5 開始）
	// 乙、丙都改了：同一段，從乙手動調的開頭到丁的開頭；乙保留開頭
	p, _ := a.PlanPatch([]string{"甲", "乙改", "丙改", "丁"})
	if len(p.Gaps) != 1 || span(p.Gaps[0]) != [2]int{1, 3} || p.Gaps[0].Pins[1] != 3.5 || len(p.Gaps[0].Pins) != 1 {
		t.Fatalf("%+v", p.Gaps)
	}
	if t0, t1, _ := p.Window(p.Gaps[0]); t0 != 3.5 || *t1 != 12 {
		t.Fatalf("%v %v", t0, *t1)
	}
	// AI 把乙放在 3.9：開頭拉回 3.5；甲唱過 3.5 的部分壓縮
	_ = p.Fill(p.Gaps[0], []wp.Line{pl("乙改", 3.9, 6), pl("丙改", 7, 13)})
	if p.Lines[1].Start != 3.5 || p.Lines[0].End > 3.5 || p.Lines[2].End >= 12 {
		t.Fatalf("%+v", p.Lines)
	}
	// 新增好幾句：同一段
	p, _ = fourLines().PlanPatch([]string{"甲", "一", "二", "乙", "丙", "丁"})
	if len(p.Gaps) != 1 || span(p.Gaps[0]) != [2]int{1, 3} {
		t.Fatalf("%+v", p.Gaps)
	}
}

func TestPatchNoRoomOrRecord(t *testing.T) {
	a := fourLines()
	a.Lines[1].End = 8 // 乙唱到丙開頭：中間沒有空檔
	p, _ := a.PlanPatch([]string{"甲", "乙", "新", "丙", "丁"})
	if _, _, err := p.Window(p.Gaps[0]); !errors.Is(err, ErrNoRoom) {
		t.Fatalf("%v", err)
	}
	a.LineLyrics = nil
	if _, err := a.PlanPatch([]string{"甲"}); !errors.Is(err, ErrNoPatch) {
		t.Fatal("沒有逐句的歌詞紀錄就整首重對")
	}
}

func TestAdjusted(t *testing.T) {
	a := fourLines()
	_, _ = a.Shift(1, 0.2, true, "t") // 乙及之後全部
	if got := a.adjusted(); !got[1] || !got[2] || !got[3] || got[0] {
		t.Fatalf("%v", got)
	}
	a.Adjustments = append(a.Adjustments, Adjustment{Line: 2, Retime: RetimeLine, Anchor: f(8)})
	if got := a.adjusted(); got[2] || !got[3] {
		t.Fatalf("之後 AI 重對過的不算手動調過：%v", got)
	}
	a.Adjustments = append(a.Adjustments, Adjustment{Line: 1, Retime: RetimeFrom, Anchor: f(4)})
	if got := a.adjusted(); len(got) != 0 {
		t.Fatalf("%v", got)
	}
}
