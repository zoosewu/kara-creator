package export

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/zoosewu/kara-creator/nas/internal/library"
)

func TestGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "export_names.json"))
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Names []struct {
			Title, Artist string
			Copy          int
			Name          string
		}
		Libraries []struct {
			Folders []struct {
				ID, Name string
				Number   int
				Parent   *string
			}
			Songs map[string]struct {
				Folder *string
				Number int
			}
			Display map[string][2]string
			Targets map[string]string
		}
	}
	if err := json.Unmarshal(data, &g); err != nil {
		t.Fatal(err)
	}
	for _, c := range g.Names {
		if got := FileName(c.Title, c.Artist, c.Copy); got != c.Name {
			t.Errorf("FileName(%q, %q, %d) = %q，應該 %q", c.Title, c.Artist, c.Copy, got, c.Name)
		}
	}
	val := func(p *string) string {
		if p == nil {
			return library.Root
		}
		return *p
	}
	for i, l := range g.Libraries {
		lib := library.New()
		for _, f := range l.Folders {
			lib.Folders[f.ID] = &library.Folder{ID: f.ID, Name: f.Name, Parent: val(f.Parent), Order: f.Number}
		}
		var songs []Song
		for id, p := range l.Songs {
			lib.Songs[id] = &library.Place{Folder: val(p.Folder), Order: p.Number}
			songs = append(songs, Song{ID: id, Title: l.Display[id][0], Artist: l.Display[id][1]})
		}
		got := Names(lib, songs)
		for id, want := range l.Targets {
			if got[id] != want {
				t.Errorf("曲庫 #%d %s：%q，應該 %q", i, id, got[id], want)
			}
		}
	}
}

func TestSync(t *testing.T) {
	dir := t.TempDir()
	root := filepath.Join(dir, "export")
	src := func(name, content string) string {
		p := filepath.Join(dir, name)
		_ = os.WriteFile(p, []byte(content), 0o644)
		return p
	}
	a, b := src("a.mp4", "影片 a"), src("b.mp4", "影片 b")
	lib := library.New()
	f, _ := lib.AddFolder("", "日文", library.Root, 0)
	lib.EnsureSong("a", f.ID)
	lib.EnsureSong("b", library.Root)
	lib.EnsureSong("c", library.Root)
	x := &Exporter{Root: root}
	songs := []Song{{ID: "a", Title: "歌", Artist: "歌手", Video: a}, {ID: "b", Title: "歌b", Video: b}, {ID: "c", Title: "還沒做"}}

	// 使用者自己放的同名檔：不動
	_ = os.MkdirAll(root, 0o755)
	_ = os.WriteFile(filepath.Join(root, "歌b.mp4"), []byte("使用者的"), 0o644)

	res, err := x.Sync(lib, songs)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Added) != 1 || res.Added[0] != "日文/歌手 - 歌.mp4" || len(res.Skipped) != 1 || res.Method == "" {
		t.Fatalf("%+v", res)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "日文", "歌手 - 歌.mp4")); string(data) != "影片 a" {
		t.Fatal(string(data))
	}
	if data, _ := os.ReadFile(filepath.Join(root, "歌b.mp4")); string(data) != "使用者的" {
		t.Fatal("使用者自己放的檔案不能覆蓋")
	}
	t.Logf("放置方式：%s", res.Method)

	// 再同步一次：沒有變動
	if res, _ := x.Sync(lib, songs); res.Kept != 1 || len(res.Added) != 0 {
		t.Fatalf("%+v", res)
	}

	// 改名：移到新位置，舊的刪掉，空資料夾也刪掉
	songs[0].Title = "改了名"
	_ = lib.UpdateSong("a", library.SongUpdate{Folder: ptr(library.Root)})
	res, _ = x.Sync(lib, songs)
	if len(res.Added) != 1 || res.Added[0] != "歌手 - 改了名.mp4" || len(res.Removed) != 1 {
		t.Fatalf("%+v", res)
	}
	if _, err := os.Stat(filepath.Join(root, "日文")); !os.IsNotExist(err) {
		t.Fatal("空資料夾要刪掉")
	}

	// 重新製作過：換成新檔（pipeline 是把新檔改名蓋過去，硬連結的舊檔不受影響）
	_ = os.Rename(src("new.mp4", "新的影片 a，長度不同"), a)
	res, _ = x.Sync(lib, songs)
	if len(res.Added) != 1 {
		t.Fatalf("%+v", res)
	}
	if data, _ := os.ReadFile(filepath.Join(root, "歌手 - 改了名.mp4")); string(data) != "新的影片 a，長度不同" {
		t.Fatal(string(data))
	}
}

func ptr(s string) *string { return &s }
