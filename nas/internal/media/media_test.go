package media

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func tools(t *testing.T) Tools {
	t.Helper()
	tl := DefaultTools("")
	if _, err := exec.LookPath(tl.FFmpeg); err != nil {
		t.Skip("沒有 ffmpeg")
	}
	return tl
}

// testVideo 用 ffmpeg 產生 2 秒的測試影片（彩條 + 440Hz 正弦波）。
func testVideo(t *testing.T, tl Tools, dir string) string {
	t.Helper()
	path := filepath.Join(dir, "source.mp4")
	if _, err := tl.run(context.Background(), tl.FFmpeg, "-y", "-v", "error",
		"-f", "lavfi", "-i", "testsrc=size=320x240:rate=10:duration=2",
		"-f", "lavfi", "-i", "sine=frequency=440:duration=2",
		"-c:v", "libx264", "-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPipeline(t *testing.T) {
	tl := tools(t)
	dir := t.TempDir()
	ctx := context.Background()
	src := testVideo(t, tl, dir)

	info, err := tl.Probe(ctx, src)
	if err != nil {
		t.Fatal(err)
	}
	if !info.HasVideo || info.Width != 320 || info.Height != 240 || info.Duration < 1.9 || info.Duration > 2.2 {
		t.Fatalf("%+v", info)
	}

	wav := filepath.Join(dir, "track.wav")
	if err := tl.ExtractWav(ctx, src, wav); err != nil {
		t.Fatal(err)
	}
	wav2 := filepath.Join(dir, "track2.wav")
	if err := tl.ExtractWav(ctx, src, wav2); err != nil {
		t.Fatal(err)
	}
	a, _ := os.ReadFile(wav)
	b, _ := os.ReadFile(wav2)
	if !bytes.Equal(a, b) {
		t.Error("同一個來源抽兩次應該完全相同（bitexact）")
	}

	flac := filepath.Join(dir, "vocals.flac")
	if err := tl.EncodeFLAC(ctx, wav, flac); err != nil {
		t.Fatal(err)
	}
	speech := filepath.Join(dir, "speech.wav")
	if err := tl.SpeechWav(ctx, flac, speech); err != nil {
		t.Fatal(err)
	}
	if si, err := tl.Probe(ctx, speech); err != nil || si.HasVideo {
		t.Fatalf("%+v %v", si, err)
	}

	out := filepath.Join(dir, "instrumental.mp4")
	if err := tl.Mux(ctx, wav, src, out, true); err != nil {
		t.Fatal(err)
	}
	if oi, err := tl.Probe(ctx, out); err != nil || !oi.HasVideo || oi.Width != 320 {
		t.Fatalf("封裝後應該保留影像軌：%+v %v", oi, err)
	}
	audioOnly := filepath.Join(dir, "instrumental.m4a")
	if err := tl.Mux(ctx, wav, src, audioOnly, false); err != nil {
		t.Fatal(err)
	}
	if oi, _ := tl.Probe(ctx, audioOnly); oi.HasVideo {
		t.Fatal("純音訊不該有影像軌")
	}
}

func TestErrors(t *testing.T) {
	tl := tools(t)
	if _, err := tl.Probe(context.Background(), "/不存在的檔案.mp4"); err == nil {
		t.Fatal("不存在的檔案應該失敗")
	}
	ctx, cancel := context.WithCancel(context.Background())
	go func() { time.Sleep(100 * time.Millisecond); cancel() }()
	_, err := tl.run(ctx, tl.FFmpeg, "-v", "error", "-re", "-f", "lavfi", "-i", "sine=duration=30", "-f", "null", "-")
	if !errors.Is(err, ErrCancelled) {
		t.Fatalf("取消時應該回傳 ErrCancelled：%v", err)
	}
}
