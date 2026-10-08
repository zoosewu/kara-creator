package fonts

import (
	"os"
	"path/filepath"
	"testing"
)

const notoBold = "/usr/share/fonts/opentype/noto/NotoSansCJK-Bold.ttc"

func TestScanNoto(t *testing.T) {
	if _, err := os.Stat(notoBold); err != nil {
		t.Skip("沒有 Noto Sans CJK（開發環境的系統字型）")
	}
	dir := t.TempDir()
	if err := os.Symlink(notoBold, filepath.Join(dir, "NotoSansCJK-Bold.ttc")); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "壞掉的.ttf"), []byte("not a font"), 0o644)
	// 內建字型資料夾裡有同一個檔案：只算一次，曲庫的優先
	builtin := t.TempDir()
	if err := os.Symlink(notoBold, filepath.Join(builtin, "內建.ttc")); err != nil {
		t.Fatal(err)
	}
	cache := map[string]string{}
	c, err := Scan([]string{dir, builtin, filepath.Join(dir, "不存在")}, cache)
	if err != nil {
		t.Fatal(err)
	}
	families := map[string]Font{} // 完整名稱 → 字型
	for _, f := range c.List() {
		families[f.FullName] = f
	}
	for _, want := range []string{"Noto Sans CJK JP Bold", "Noto Sans CJK TC Bold", "Noto Sans CJK KR Bold"} {
		f, ok := families[want]
		// Noto Sans CJK：unitsPerEm 1000、winAscent + winDescent 1448 → libass 縮放約 0.69
		if !ok || f.Weight != 700 || !f.Default || f.Scale < 0.68 || f.Scale > 0.70 {
			t.Errorf("%s：%+v（全部：%v）", want, f, families)
		}
	}
	ja, ok := c.ForLanguage("ja", nil)
	if !ok || ja.Family != "Noto Sans CJK JP" || ja.FullName != "Noto Sans CJK JP Bold" || ja.Index != 0 {
		t.Fatalf("%+v", ja)
	}
	if nan, _ := c.ForLanguage("nan", nil); nan.FullName != "Noto Sans CJK TC Bold" {
		t.Fatalf("%+v", nan)
	}
	if other, _ := c.ForLanguage("xx", nil); other.FullName != "Noto Sans CJK JP Bold" {
		t.Fatal("不認得的語言用日文版")
	}
	kr := families["Noto Sans CJK KR Bold"]
	if f, _ := c.ForLanguage("ja", map[string]string{"ja": kr.ID}); f.ID != kr.ID {
		t.Fatal("設定指定的字型優先")
	}
	if p, ok := c.Path(ja.SHA256); !ok || filepath.Base(p) != "NotoSansCJK-Bold.ttc" {
		t.Fatal(p)
	}
	if single, _ := Scan([]string{dir}, nil); len(c.List()) != len(single.List()) {
		t.Fatalf("同一個檔案只算一次：%d ≠ %d", len(c.List()), len(single.List()))
	}
	if len(cache) != 2 {
		t.Fatalf("sha256 要快取：%v", cache)
	}
}

func TestEmpty(t *testing.T) {
	c, err := Scan([]string{t.TempDir()}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := c.ForLanguage("ja", nil); ok {
		t.Fatal("沒有字型時 ok 要是 false")
	}
}
