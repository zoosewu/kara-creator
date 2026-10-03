package planner

import (
	"os"
	"testing"
	"time"

	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
)

func TestInspect(t *testing.T) {
	st, err := store.Open(t.TempDir(), true)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.AddSong(song.New("abc", song.Source{Kind: song.KindLocal, Title: "x"}), library.Root); err != nil {
		t.Fatal(err)
	}
	src := st.SongPath("abc", "source.mp4")
	if err := os.WriteFile(src, []byte("video"), 0o644); err != nil {
		t.Fatal(err)
	}
	ref, _ := store.Refresh(src, song.FileRef{})
	_ = st.EditSong("abc", func(s *song.Song) error { s.Source.File = ref; return nil })
	if err := os.WriteFile(st.SongPath("abc", song.FileLyrics), []byte("\ufeff# title: 編的\n一句\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	settings := library.New().Settings
	in, err := Inspect(st, "abc", settings, fonts, versions)
	if err != nil {
		t.Fatal(err)
	}
	if !in.SourceOK || in.Lyrics == nil || in.Lyrics.Meta["title"] != "編的" || in.Alignment != nil {
		t.Fatalf("%+v", in)
	}

	// 來源檔換了：重算 sha256 並寫回 song.json
	if err := os.WriteFile(src, []byte("another video"), 0o644); err != nil {
		t.Fatal(err)
	}
	later := time.Now().Add(time.Minute)
	_ = os.Chtimes(src, later, later)
	if _, err := Inspect(st, "abc", settings, fonts, versions); err != nil {
		t.Fatal(err)
	}
	sg, _ := st.Song("abc")
	if sg.Source.File.SHA256 == ref.SHA256 || sg.Source.File.Size != int64(len("another video")) {
		t.Fatalf("來源換了應該更新紀錄：%+v", sg.Source.File)
	}
	_ = os.Remove(src)
	if in, _ := Inspect(st, "abc", settings, fonts, versions); in.SourceOK {
		t.Fatal("來源不見了")
	}
}
