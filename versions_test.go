package kara

import "testing"

func TestCurrentVersions(t *testing.T) {
	if Current.Protocol < 1 || Current.Align < 1 || Current.QA < 1 {
		t.Fatalf("versions.json 讀不到版本：%+v", Current)
	}
}

func TestParseVersionsRejectsUnknownField(t *testing.T) {
	if _, err := ParseVersions([]byte(`{"protocol":1,"new_stage":1}`)); err == nil {
		t.Fatal("有不認得的欄位應該要失敗")
	}
}

func TestDiff(t *testing.T) {
	a := Versions{Protocol: 1, Align: 7}
	b := a
	if d := a.Diff(b); d != nil {
		t.Fatalf("相同版本不該有差異：%v", d)
	}
	b.Align = 6
	if d := a.Diff(b); len(d) != 1 || d[0] != "align 7 ≠ 6" {
		t.Fatalf("差異不對：%v", d)
	}
}
