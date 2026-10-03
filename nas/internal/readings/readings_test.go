package readings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zoosewu/kara-creator/nas/internal/lyrics"
)

func normalize(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var generic any
	if err := json.Unmarshal(data, &generic); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(generic)
	return string(out)
}

func TestGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "readings.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Language      string
		Text          string
		Auto          map[string][][]any
		Views         json.RawMessage
		Annotated     string
		FromAnnotated json.RawMessage `json:"from_annotated"`
		Edited        string
		FromEdited    json.RawMessage `json:"from_edited"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for i, c := range cases {
		lookup := func(text string) ([]Span, bool) {
			raw, ok := c.Auto[text]
			if !ok {
				t.Fatalf("#%d 黃金資料沒有 %q 的自動讀音", i, text)
			}
			spans := []Span{}
			for _, r := range raw {
				spans = append(spans, Span{Start: int(r[0].(float64)), End: int(r[1].(float64)), Ruby: r[2].(string)})
			}
			return spans, true
		}
		views := BuildViews(lyrics.Parse(c.Text), c.Language, lookup)
		var want any
		_ = json.Unmarshal(c.Views, &want)
		if got, w := normalize(t, views), normalize(t, want); got != w {
			t.Errorf("#%d 檢視\n得到 %s\n應該 %s", i, got, w)
		}
		for _, pair := range []struct {
			text string
			want json.RawMessage
		}{{c.Annotated, c.FromAnnotated}, {c.Edited, c.FromEdited}} {
			doc, missing := FromAnnotated(pair.text, c.Language, lookup)
			var w any
			_ = json.Unmarshal(pair.want, &w)
			if got, ww := normalize(t, doc), normalize(t, w); got != ww || missing != nil {
				t.Errorf("#%d 標註原文轉回\n得到 %s\n應該 %s", i, got, ww)
			}
		}
	}
}

func TestPending(t *testing.T) {
	offline := func(string) ([]Span, bool) { return nil, false }
	doc := lyrics.Parse("空を見る\n海{うみ}へ\n")
	v := BuildViews(doc, "ja", offline)
	if len(v.Pending) != 2 {
		t.Fatalf("AI 不在時兩句都應該標成假名稍後補上：%v", v.Pending)
	}
	if v.Annotated != "空を見る\n海{うみ}へ\n" {
		t.Fatalf("沒有自動讀音時只列手動讀音：%q", v.Annotated)
	}
	if _, missing := FromAnnotated(v.Annotated, "ja", offline); len(missing) != 2 {
		t.Fatal("沒有自動讀音時標註原文不能轉回（不能猜哪些是手動的）")
	}
	if v := BuildViews(lyrics.Parse("中文\n"), "zh", offline); v.Pending != nil {
		t.Fatal("中文沒有自動讀音，不需要等 AI")
	}
}

func TestCache(t *testing.T) {
	path := filepath.Join(t.TempDir(), "readings.jsonl")
	c, err := OpenCache(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.Get("ja", "空"); ok {
		t.Fatal("空的快取不該有資料")
	}
	if err := c.Put("ja", "空", []Span{{Start: 0, End: 1, Ruby: "そら"}}); err != nil {
		t.Fatal(err)
	}
	_ = c.Put("ja", "かな", nil)
	// 重新開啟：讀回來相同；寫到一半的行忽略
	f, _ := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	_, _ = f.WriteString(`{"k":"broken`)
	f.Close()
	c2, err := OpenCache(path, 1)
	if err != nil {
		t.Fatal(err)
	}
	if spans, ok := c2.Get("ja", "空"); !ok || len(spans) != 1 || spans[0].Ruby != "そら" {
		t.Fatalf("%v %v", spans, ok)
	}
	if spans, ok := c2.Get("ja", "かな"); !ok || spans == nil {
		t.Fatal("沒有讀音的句子也要記住（不必再問 AI）")
	}
	// 讀音規則改版：舊的全部失效
	c3, _ := OpenCache(path, 2)
	if _, ok := c3.Get("ja", "空"); ok {
		t.Fatal("版本不同不該命中")
	}
	data, _ := os.ReadFile(path)
	if len(data) != 0 {
		t.Fatalf("改版後應該清掉舊資料：%q", data)
	}
}
