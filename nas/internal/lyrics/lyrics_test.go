package lyrics

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// spec 讀 testdata/spec 的規格資料（每個情況的輸入與應有的結果）。
func spec(t *testing.T, name string, v any) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "spec", name+".json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatal(err)
	}
}

func TestParseSpec(t *testing.T) {
	var cases []struct {
		Input      string          `json:"input"`
		Doc        json.RawMessage `json:"doc"`
		Serialized string          `json:"serialized"`
		Language   *string         `json:"language"`
	}
	spec(t, "lyrics_parse", &cases)
	for i, c := range cases {
		doc := Parse(strings.TrimPrefix(c.Input, "\ufeff"))
		var want Document
		if err := json.Unmarshal(c.Doc, &want); err != nil {
			t.Fatal(err)
		}
		got, _ := json.Marshal(doc)
		wantJSON, _ := json.Marshal(want)
		if string(got) != string(wantJSON) {
			t.Errorf("#%d Parse(%q)\n得到 %s\n應該 %s", i, c.Input, got, wantJSON)
			continue
		}
		if s := Serialize(doc); s != c.Serialized {
			t.Errorf("#%d Serialize(%q)\n得到 %q\n應該 %q", i, c.Input, s, c.Serialized)
		}
		lang := ""
		if c.Language != nil {
			lang = *c.Language
		}
		if got := DetectLanguage(doc.Texts()); got != lang {
			t.Errorf("#%d 語言 %q，應該 %q", i, got, lang)
		}
	}
}

func TestParenSpec(t *testing.T) {
	var cases []struct {
		Input     string   `json:"input"`
		Readings  []string `json:"readings"`
		Converted string   `json:"converted"`
	}
	spec(t, "paren", &cases)
	for i, c := range cases {
		if got := ParenReadings(c.Input); !reflect.DeepEqual(got, c.Readings) {
			t.Errorf("#%d ParenReadings(%q) = %q，應該 %q", i, c.Input, got, c.Readings)
		}
		if got := ParenToRuby(c.Input); got != c.Converted {
			t.Errorf("#%d ParenToRuby(%q) = %q，應該 %q", i, c.Input, got, c.Converted)
		}
	}
}

func TestLanguageSpec(t *testing.T) {
	var cases []struct {
		Lines    []string `json:"lines"`
		Language *string  `json:"language"`
	}
	spec(t, "language", &cases)
	for _, c := range cases {
		want := ""
		if c.Language != nil {
			want = *c.Language
		}
		if got := DetectLanguage(c.Lines); got != want {
			t.Errorf("DetectLanguage(%q) = %q，應該 %q", c.Lines, got, want)
		}
	}
}

func TestFromPlainSpec(t *testing.T) {
	var cases []struct {
		Base  Document        `json:"base"`
		Plain string          `json:"plain"`
		Doc   json.RawMessage `json:"doc"`
	}
	spec(t, "lyrics_plain", &cases)
	for i, c := range cases {
		var want Document
		if err := json.Unmarshal(c.Doc, &want); err != nil {
			t.Fatal(err)
		}
		got, _ := json.Marshal(FromPlain(c.Plain, c.Base))
		wantJSON, _ := json.Marshal(want)
		if string(got) != string(wantJSON) {
			t.Errorf("#%d FromPlain(%q)\n得到 %s\n應該 %s", i, c.Plain, got, wantJSON)
		}
	}
}
