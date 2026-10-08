package fingerprint

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zoosewu/kara-creator/nas/internal/lyrics"
	"github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// 規格測試：指紋的算法改了會讓所有歌被判定為需要重做，所以固定成規格資料。
func TestSpec(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "spec", "fingerprint.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Lyrics []struct {
			Texts       []string
			Rubies      [][]lyrics.Ruby
			Fingerprint string
		}
		Alignment []struct {
			Lines       []workerproto.Line
			Fingerprint string
		}
		Ms []struct {
			T  float64
			Ms int64
		}
		Items []struct {
			Values []*string
			Items  string
		}
		Stages []struct {
			Kind        string
			Args        map[string]any
			Fingerprint string
		}
	}
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	if len(g.Lyrics) == 0 || len(g.Alignment) == 0 || len(g.Stages) == 0 {
		t.Fatal("規格測試資料是空的")
	}
	for _, c := range g.Lyrics {
		if got := Lyrics(c.Texts, c.Rubies); got != c.Fingerprint {
			t.Errorf("Lyrics(%q, %v) 不同", c.Texts, c.Rubies)
		}
	}
	for _, c := range g.Alignment {
		if got := Alignment(c.Lines); got != c.Fingerprint {
			t.Errorf("Alignment(%+v) 不同", c.Lines)
		}
	}
	for _, c := range g.Ms {
		if got := Ms(c.T); got != c.Ms {
			t.Errorf("Ms(%v) = %d，應該 %d", c.T, got, c.Ms)
		}
	}
	for _, c := range g.Items {
		if got := Items(c.Values); got != c.Items {
			t.Errorf("Items(%v) = %q，應該 %q", c.Values, got, c.Items)
		}
	}
	for _, c := range g.Stages {
		a := c.Args
		s := func(k string) string { v, _ := a[k].(string); return v }
		n := func(k string) int { return int(a[k].(float64)) }
		var got string
		switch c.Kind {
		case "separate":
			got = Separate(s("source"), s("model"), n("stems"), n("version"))
		case "align":
			got = Align(s("lyrics"), s("vocals"), s("model"), s("language"), n("version"))
		case "qa":
			got = QA(s("alignment"), s("lyrics"), s("vocals"), s("language"), s("model"), n("version"))
		case "approve":
			got = Approve(s("lyrics"), s("language"), n("method"), s("run"))
		case "render":
			got = Render(RenderInput{Target: s("target"), Media: s("media"), Alignment: s("alignment"), Lyrics: s("lyrics"),
				Singers: s("singers"), Translations: s("translations"), Title: s("title"), Artist: s("artist"), Note: s("note"),
				Scale: a["scale"].(float64), Font: s("font"), Size: s("size"), Version: n("version"), Reading: n("reading"), ASS: s("ass")})
		}
		if got != c.Fingerprint {
			t.Errorf("%s(%v) 不同", c.Kind, a)
		}
	}
}
