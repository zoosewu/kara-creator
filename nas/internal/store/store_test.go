package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/song"
)

func TestOpenRequiresInit(t *testing.T) {
	root := filepath.Join(t.TempDir(), "沒掛上的硬碟")
	if _, err := Open(root, false); !errors.Is(err, ErrNoLibrary) {
		t.Fatalf("沒有曲庫又沒加 --init 應該失敗：%v", err)
	}
	if _, err := os.Stat(root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("失敗時不該建立任何資料夾")
	}
	if _, err := Open(root, true); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{SongsDir, InboxDir, ExportDir, FontsDir, WorkDir} {
		if _, err := os.Stat(filepath.Join(root, d)); err != nil {
			t.Errorf("應該建立 %s：%v", d, err)
		}
	}
	if _, err := Open(root, false); err != nil {
		t.Fatalf("建立後應該可以直接開啟：%v", err)
	}
}

func TestSongsPersist(t *testing.T) {
	root := t.TempDir()
	s, err := Open(root, true)
	if err != nil {
		t.Fatal(err)
	}
	var changes []Change
	s.OnChange = func(c Change) { changes = append(changes, c) }

	var folder string
	if err := s.EditLibrary(func(l *library.Library) error {
		f, err := l.AddFolder("", "日文", library.Root, 0)
		folder = f.ID
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSong(song.New("abc", song.Source{Kind: song.KindURL, Title: "自己編的標題"}), folder); err != nil {
		t.Fatal(err)
	}
	if err := s.AddSong(song.New("abc", song.Source{}), folder); !errors.Is(err, ErrSongExists) {
		t.Fatalf("重複新增應該失敗：%v", err)
	}
	title := "  手動  歌名 "
	if err := s.EditSong("abc", func(sg *song.Song) error { return sg.ApplyInfo(song.InfoUpdate{Title: &title}) }); err != nil {
		t.Fatal(err)
	}
	bad := "xx"
	if err := s.EditSong("abc", func(sg *song.Song) error { return sg.ApplyInfo(song.InfoUpdate{Language: &bad}) }); err == nil {
		t.Fatal("不支援的語言應該失敗")
	}
	got, _ := s.Song("abc")
	got.Info.Title = "改複本不影響原本的"
	if len(changes) != 4 { // library、song + library（新增）、song（改名）
		t.Errorf("通知次數 %d：%+v", len(changes), changes)
	}

	// 重新開啟：讀回來的內容相同；不在 library.json 裡的歌放到最上層。
	if err := os.MkdirAll(filepath.Join(root, SongsDir, "orphan"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteFile(filepath.Join(root, SongsDir, "orphan", song.FileRecord), []byte(`{"version":2,"id":"orphan"}`)); err != nil {
		t.Fatal(err)
	}
	_ = os.MkdirAll(filepath.Join(root, SongsDir, "downloading"), 0o755) // 還沒有紀錄的資料夾
	s2, err := Open(root, false)
	if err != nil {
		t.Fatal(err)
	}
	sg, ok := s2.Song("abc")
	if !ok || sg.Info.Title != "手動 歌名" || !sg.Info.Translation || sg.Info.Targets[0] != song.TargetInstrumental {
		t.Fatalf("%+v", sg)
	}
	lib := s2.Library()
	if lib.Songs["abc"].Folder != folder || lib.Songs["orphan"] == nil || lib.Songs["orphan"].Folder != library.Root {
		t.Fatalf("%+v", lib.Songs)
	}
	if ids := s2.SongIDs(); len(ids) != 2 {
		t.Fatalf("%v", ids)
	}
}

func TestRefresh(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.bin")
	if err := os.WriteFile(path, []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, err := Refresh(path, song.FileRef{})
	if err != nil || ref.SHA256 != "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824" || ref.Size != 5 {
		t.Fatalf("%+v %v", ref, err)
	}
	fake := ref
	fake.SHA256 = "cached"
	if again, _ := Refresh(path, fake); again.SHA256 != "cached" {
		t.Fatal("大小與修改時間沒變時應該沿用紀錄的 sha256")
	}
	future := time.Now().Add(time.Hour)
	_ = os.Chtimes(path, future, future)
	if again, _ := Refresh(path, fake); again.SHA256 != ref.SHA256 {
		t.Fatal("修改時間變了應該重算")
	}
}
