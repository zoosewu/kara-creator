package song

import "testing"

func TestTrim(t *testing.T) {
	s := New("abc", Source{Duration: 200})
	trim := func(start, end float64) error { return s.ApplyInfo(InfoUpdate{Trim: &Trim{Start: start, End: end}}) }
	if err := trim(12.345, 180); err != nil || *s.Info.Trim != (Trim{Start: 12.35, End: 180}) {
		t.Fatalf("取到 0.01 秒：%+v %v", s.Info.Trim, err)
	}
	if err := trim(5, 199.97); err != nil || *s.Info.Trim != (Trim{Start: 5}) {
		t.Fatalf("結尾在影片最後就是不剪結尾：%+v %v", s.Info.Trim, err)
	}
	if err := trim(0, 0); err != nil || s.Info.Trim != nil {
		t.Fatalf("都是 0 = 不剪：%+v %v", s.Info.Trim, err)
	}
	for _, bad := range [][2]float64{{-1, 0}, {0, -1}, {100, 103}, {197, 0}} {
		if err := trim(bad[0], bad[1]); err == nil {
			t.Fatalf("%v 要拒絕", bad)
		}
	}
	if err := s.ApplyInfo(InfoUpdate{Note: new("備註")}); err != nil || s.Info.Trim != nil {
		t.Fatal("沒給 trim 就不改")
	}
}
