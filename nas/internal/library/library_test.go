package library

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

type goldenOp struct {
	Op        string
	Name      *string
	Parent    *string
	Number    *int
	ID        *string
	NewID     string `json:"new_id"`
	SetParent bool   `json:"set_parent"`
	Key       string
	Folder    *string
	SetFolder bool `json:"set_folder"`
	Kind      string
	Before    *string
}

type goldenStep struct {
	Op      goldenOp
	Error   *string
	Folders []struct {
		ID     string
		Name   string
		Number int
		Parent *string
	}
	Songs map[string]struct {
		Folder *string
		Number int
	}
}

func s(p *string) string {
	if p == nil {
		return Root
	}
	return *p
}

func apply(l *Library, op goldenOp) error {
	switch op.Op {
	case "add_folder":
		order := 0
		if op.Number != nil {
			order = *op.Number
		}
		_, err := l.AddFolder(op.NewID, s(op.Name), s(op.Parent), order)
		return err
	case "update_folder":
		u := FolderUpdate{Name: op.Name, Order: op.Number}
		if op.SetParent {
			p := s(op.Parent)
			u.Parent = &p
		}
		_, err := l.UpdateFolder(s(op.ID), u)
		return err
	case "delete_folder":
		return l.DeleteFolder(s(op.ID))
	case "ensure_song":
		l.EnsureSong(op.Key, s(op.Folder))
		return nil
	case "update_song":
		u := SongUpdate{Order: op.Number}
		if op.SetFolder {
			f := s(op.Folder)
			u.Folder = &f
		}
		return l.UpdateSong(op.Key, u)
	case "place":
		return l.Place(op.Kind, s(op.ID), s(op.Parent), s(op.Before))
	}
	return fmt.Errorf("不認得的操作 %s", op.Op)
}

// 黃金測試：v1 catalog 的隨機操作序列，每一步的結果（含錯誤訊息與出錯前已做的修改）都要相同。
func TestGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	var seqs [][]goldenStep
	if err := json.Unmarshal(data, &seqs); err != nil {
		t.Fatal(err)
	}
	for n, seq := range seqs {
		l := New()
		for k, step := range seq {
			err := apply(l, step.Op)
			where := fmt.Sprintf("序列 %d 第 %d 步 %+v", n, k, step.Op)
			switch {
			case step.Error == nil && err != nil:
				t.Fatalf("%s：不該出錯：%v", where, err)
			case step.Error != nil && (err == nil || Message(err) != *step.Error):
				t.Fatalf("%s：錯誤應該是 %q，得到 %v", where, *step.Error, err)
			}
			if len(l.Folders) != len(step.Folders) || len(l.Songs) != len(step.Songs) {
				t.Fatalf("%s：數量不同", where)
			}
			for _, f := range step.Folders {
				got := l.Folders[f.ID]
				if got == nil || got.Name != f.Name || got.Order != f.Number || got.Parent != s(f.Parent) {
					t.Fatalf("%s：資料夾 %s 得到 %+v，應該 %+v", where, f.ID, got, f)
				}
			}
			for id, p := range step.Songs {
				got := l.Songs[id]
				if got == nil || got.Order != p.Number || got.Folder != s(p.Folder) {
					t.Fatalf("%s：歌曲 %s 得到 %+v，應該 %+v", where, id, got, p)
				}
			}
		}
	}
}

func TestJSONRoundTrip(t *testing.T) {
	l := New()
	f, _ := l.AddFolder("", "日文", Root, 0)
	l.EnsureSong("abc", f.ID)
	l.EnsureSong("def", Root)
	l.Settings.Fonts["ja"] = "sha"
	data, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	var back Library
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	again, _ := json.Marshal(&back)
	if string(again) != string(data) {
		t.Fatalf("存檔再讀回不同：\n%s\n%s", data, again)
	}
	if back.Songs["abc"].Folder != f.ID || back.Songs["def"].Folder != Root {
		t.Fatal(string(data))
	}
	if err := json.Unmarshal([]byte(`{"version":1}`), &back); err == nil {
		t.Fatal("v1 的格式應該拒絕")
	}
}
