package pystr

import (
	"reflect"
	"testing"
)

func TestSplitLines(t *testing.T) {
	cases := map[string][]string{
		"":                nil,
		"\n":              {""},
		"a":               {"a"},
		"a\n":             {"a"},
		"a\r\nb\rc\n\nd":  {"a", "b", "c", "", "d"},
		"a\x1cb\u2028c":   {"a", "b", "c"},
		"a\x1fb":          {"a\x1fb"}, // \x1f 是空白但不是換行
		"a\r":             {"a"},
		"\r\n\r\n":        {"", ""},
		"中\u0085文\u2029字": {"中", "文", "字"},
	}
	for in, want := range cases {
		if got := SplitLines(in); !reflect.DeepEqual(got, want) {
			t.Errorf("SplitLines(%q) = %q，應該是 %q", in, got, want)
		}
	}
}

func TestFields(t *testing.T) {
	if got := JoinFields("　全形　空白\x1f與\u00a0tab\t "); got != "全形 空白 與 tab" {
		t.Errorf("%q", got)
	}
	if IsSpace(0x200b) {
		t.Error("零寬空白在 Python 不算空白")
	}
}
