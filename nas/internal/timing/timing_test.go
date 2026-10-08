package timing

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

const now = "2026-10-03T12:00:00+08:00"

func TestShiftSpec(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "spec", "shift_timing.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Lines     []wp.Line
		Line      int
		Delta     float64
		Following bool
		Error     *string
		Pushed    []int
		After     struct {
			Lines       []wp.Line
			Adjustments []Adjustment
		}
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		a := &Alignment{Key: "k", Lines: c.Lines}
		pushed, err := a.Shift(c.Line, c.Delta, c.Following, now)
		if c.Error != nil {
			if err == nil || !errors.Is(err, ErrInvalid) || err.Error() != *c.Error {
				t.Errorf("#%d 錯誤應該是 %q，得到 %v", i, *c.Error, err)
			}
			continue
		}
		if err != nil {
			t.Errorf("#%d 不該出錯：%v", i, err)
			continue
		}
		if !reflect.DeepEqual(pushed, c.Pushed) {
			t.Errorf("#%d pushed = %v，應該 %v", i, pushed, c.Pushed)
		}
		if !reflect.DeepEqual(a.Lines, c.After.Lines) {
			got, _ := json.Marshal(a.Lines)
			want, _ := json.Marshal(c.After.Lines)
			t.Errorf("#%d 移動後\n得到 %s\n應該 %s", i, got, want)
		}
		if !reflect.DeepEqual(a.Adjustments, c.After.Adjustments) {
			t.Errorf("#%d 調整紀錄 %+v，應該 %+v", i, a.Adjustments, c.After.Adjustments)
		}
	}
}

func line(text string, start, end float64, words ...wp.Word) wp.Line {
	return wp.Line{Text: text, Start: start, End: end, Words: words}
}

func TestApplyFrom(t *testing.T) {
	a := &Alignment{Lines: []wp.Line{
		line("一", 1, 3, wp.Word{Text: "一", Start: 1, End: 3}),
		line("二", 4, 5, wp.Word{Text: "二", Start: 4, End: 5}),
		line("三", 6, 7, wp.Word{Text: "三", Start: 6, End: 7}),
	}}
	fresh := []wp.Line{line("二", 2.5, 3.5, wp.Word{Text: "二", Start: 2.5, End: 3.5}), line("三", 4, 5)}
	if err := a.ApplyFrom(1, 2.5, fresh, now); err != nil {
		t.Fatal(err)
	}
	if a.Lines[0].End != 2.5 || a.Lines[0].Words[0].End != 2.5 {
		t.Errorf("上一句的尾音應該截到 anchor：%+v", a.Lines[0])
	}
	if len(a.Lines) != 3 || a.Lines[1].Start != 2.5 || a.Lines[2].Start != 4 {
		t.Errorf("%+v", a.Lines)
	}
	if adj := a.Adjustments[0]; adj.Retime != RetimeFrom || *adj.Anchor != 2.5 || adj.Line != 1 {
		t.Errorf("%+v", adj)
	}
	// 尾音至少留 0.05 秒
	b := &Alignment{Lines: []wp.Line{line("一", 1, 3, wp.Word{Text: "一", Start: 2, End: 3}), line("二", 4, 5)}}
	_ = b.ApplyFrom(1, 1.5, []wp.Line{line("二", 1.5, 2)}, now)
	if b.Lines[0].End != 2.05 {
		t.Errorf("%+v", b.Lines[0])
	}
	if err := b.ApplyFrom(0, 0, nil, now); err == nil {
		t.Error("句數不符應該拒絕")
	}
}

func TestApplyLine(t *testing.T) {
	a := &Alignment{Lines: []wp.Line{line("一", 1, 2), line("二", 3, 4)}}
	if err := a.ApplyLine(1, 3, line("二", 3.2, 3.9), now); err != nil {
		t.Fatal(err)
	}
	if a.Lines[1].Start != 3.2 || a.Adjustments[0].Retime != RetimeLine {
		t.Fatalf("%+v", a)
	}
	if err := a.ApplyLine(5, 0, wp.Line{}, now); err == nil {
		t.Fatal("沒有這一句應該拒絕")
	}
}

func TestClone(t *testing.T) {
	a := &Alignment{Lines: []wp.Line{line("一", 1, 2, wp.Word{Text: "一", Start: 1, End: 2})}}
	c := a.Clone()
	c.Lines[0].Words[0].Start = 9
	if a.Lines[0].Words[0].Start != 1 {
		t.Fatal("修改複本不該影響原本的")
	}
}
