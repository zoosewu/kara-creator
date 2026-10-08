// Package media 包裝 NAS 上的輕量 ffmpeg / ffprobe 操作：
// 探測媒體資訊、抽音軌、轉 16kHz 人聲、不重新編碼影像的封裝。**不重新編碼影片、不燒錄**（那是 AI worker 的事）。
package media

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/zoosewu/kara-creator/nas/internal/proc"
)

// 副檔名。
var (
	AudioExts = map[string]bool{".mp3": true, ".wav": true, ".flac": true, ".m4a": true, ".aac": true, ".ogg": true,
		".opus": true, ".wma": true, ".aiff": true}
	VideoExts = map[string]bool{".mp4": true, ".mkv": true, ".mov": true, ".webm": true, ".avi": true, ".m4v": true, ".flv": true}
)

// IsMedia 判斷副檔名是不是支援的影音檔。
func IsMedia(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	return AudioExts[ext] || VideoExts[ext]
}

// 每個容器對應的音訊編碼器；沒列到的容器一律以 aac 輸出。
var audioCodecs = map[string][]string{
	".mp3":  {"-c:a", "libmp3lame", "-q:a", "0"},
	".m4a":  {"-c:a", "aac", "-b:a", "320k"},
	".aac":  {"-c:a", "aac", "-b:a", "320k"},
	".ogg":  {"-c:a", "libvorbis", "-q:a", "8"},
	".opus": {"-c:a", "libopus", "-b:a", "256k"},
	".flac": {"-c:a", "flac"},
	".wav":  {"-c:a", "pcm_s16le"},
	".aiff": {"-c:a", "pcm_s16be"},
	".wma":  {"-c:a", "aac", "-b:a", "320k"},
}

// Tools 是 ffmpeg / ffprobe 的位置。
type Tools struct {
	FFmpeg  string
	FFprobe string
}

// DefaultTools 從 PATH 找（或用 dir 底下的執行檔）。
func DefaultTools(dir string) Tools {
	find := func(name string) string {
		if dir != "" {
			if p, err := exec.LookPath(filepath.Join(dir, name)); err == nil {
				return p
			}
		}
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
		return name
	}
	return Tools{FFmpeg: find("ffmpeg"), FFprobe: find("ffprobe")}
}

// Info 是 ffprobe 的結果。
type Info struct {
	Duration float64 // 秒（小數三位）；讀不到時為 0
	HasVideo bool    // 有真正的影像軌（內嵌封面圖不算）
	Width    int
	Height   int
	Audio    string // 第一條音軌的編碼（例如 aac、opus）；沒有音軌時為空
}

// Probe 讀媒體資訊。
func (t Tools) Probe(ctx context.Context, path string) (Info, error) {
	out, err := t.run(ctx, t.FFprobe, "-v", "error", "-show_entries",
		"format=duration:stream=codec_type,codec_name,width,height:stream_disposition=attached_pic", "-of", "json", path)
	if err != nil {
		return Info{}, err
	}
	var p struct {
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
		Streams []struct {
			CodecType   string `json:"codec_type"`
			CodecName   string `json:"codec_name"`
			Width       int    `json:"width"`
			Height      int    `json:"height"`
			Disposition struct {
				AttachedPic int `json:"attached_pic"`
			} `json:"disposition"`
		} `json:"streams"`
	}
	if err := json.Unmarshal(out, &p); err != nil {
		return Info{}, fmt.Errorf("ffprobe 的結果讀不懂：%w", err)
	}
	var info Info
	if d, err := strconv.ParseFloat(p.Format.Duration, 64); err == nil {
		info.Duration = math.Round(d*1000) / 1000
	}
	for _, s := range p.Streams {
		if s.CodecType == "video" && s.Disposition.AttachedPic != 1 && !info.HasVideo {
			info.HasVideo, info.Width, info.Height = true, s.Width, s.Height
		}
		if s.CodecType == "audio" && info.Audio == "" {
			info.Audio = s.CodecName
		}
	}
	return info, nil
}

// ExtractWav 抽出音軌成 44.1kHz 立體聲 16-bit wav（Demucs 的輸入）。
// -bitexact：同一個來源每次轉出來完全一樣，AI worker 的快取才認得。
func (t Tools) ExtractWav(ctx context.Context, src, dst string) error {
	_, err := t.run(ctx, t.FFmpeg, "-y", "-v", "error", "-i", src, "-vn", "-map", "0:a:0", "-ac", "2", "-ar", "44100",
		"-c:a", "pcm_s16le", "-map_metadata", "-1", "-fflags", "+bitexact", "-flags:a", "+bitexact", dst)
	return err
}

// SpeechWav 把人聲轉成 16kHz 單聲道 16-bit wav（對時與檢查的輸入）。
func (t Tools) SpeechWav(ctx context.Context, src, dst string) error {
	_, err := t.run(ctx, t.FFmpeg, "-y", "-v", "error", "-i", src, "-vn", "-map", "0:a:0", "-ac", "1", "-ar", "16000",
		"-c:a", "pcm_s16le", "-map_metadata", "-1", "-fflags", "+bitexact", "-flags:a", "+bitexact", dst)
	return err
}

// ExportAudio 把來源的第一條音軌存成 m4a（匯出原曲音訊用）：AAC 直接複製（不重新編碼），其他編碼轉成 AAC 320k。
// 先寫到暫存檔再改名，寫到一半中斷不會留下壞掉的檔案。
func (t Tools) ExportAudio(ctx context.Context, src, dst string) error {
	info, err := t.Probe(ctx, src)
	if err != nil {
		return err
	}
	if info.Audio == "" {
		return errors.New("來源沒有音軌")
	}
	codec := []string{"-c:a", "aac", "-b:a", "320k"}
	if info.Audio == "aac" {
		codec = []string{"-c:a", "copy"}
	}
	tmp := dst + ".tmp"
	args := append([]string{"-y", "-v", "error", "-i", src, "-vn", "-map", "0:a:0"}, codec...)
	if _, err := t.run(ctx, t.FFmpeg, append(args, "-movflags", "+faststart", "-f", "ipod", tmp)...); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

// Clip 剪出 wav 的 [t0, t1)（t1 為 nil 代表到結尾），格式不變（局部重對時只把那一段人聲交給 AI）。
func (t Tools) Clip(ctx context.Context, src, dst string, t0 float64, t1 *float64) error {
	args := []string{"-y", "-v", "error", "-ss", strconv.FormatFloat(t0, 'f', 3, 64)}
	if t1 != nil {
		args = append(args, "-t", strconv.FormatFloat(*t1-t0, 'f', 3, 64))
	}
	args = append(args, "-i", src, "-c:a", "pcm_s16le", "-map_metadata", "-1", "-fflags", "+bitexact", "-flags:a", "+bitexact", dst)
	_, err := t.run(ctx, t.FFmpeg, args...)
	return err
}

// EncodeFLAC 把 wav 存成 FLAC（人聲）。
func (t Tools) EncodeFLAC(ctx context.Context, src, dst string) error {
	_, err := t.run(ctx, t.FFmpeg, "-y", "-v", "error", "-i", src, "-map", "0:a:0", "-c:a", "flac", dst)
	return err
}

// Mux 把音軌封裝成 dest 的容器；keepVideo 時沿用 source 的影像軌（不重新編碼）。
func (t Tools) Mux(ctx context.Context, audio, source, dest string, keepVideo bool) error {
	ext := strings.ToLower(filepath.Ext(dest))
	codec, ok := audioCodecs[ext]
	if !ok {
		codec = []string{"-c:a", "aac", "-b:a", "320k"}
	}
	args := []string{"-y", "-v", "error"}
	if keepVideo {
		args = append(args, "-i", source, "-i", audio, "-map", "0:v:0", "-map", "1:a:0", "-c:v", "copy")
		args = append(args, codec...)
		args = append(args, "-map_metadata", "0", "-shortest")
		if ext == ".mp4" || ext == ".m4v" || ext == ".mov" {
			args = append(args, "-movflags", "+faststart")
		}
	} else {
		args = append(args, "-i", audio)
		args = append(args, codec...)
	}
	_, err := t.run(ctx, t.FFmpeg, append(args, dest)...)
	return err
}

// ErrCancelled 表示處理被取消（ctx 結束）。
var ErrCancelled = errors.New("已取消")

// run 執行外部指令；ctx 結束時終止子程序。失敗時錯誤訊息帶 stderr 的最後一段。
func (t Tools) run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := proc.Command(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ErrCancelled
		}
		msg := stderr.String()
		if len(msg) > 2000 {
			msg = msg[len(msg)-2000:]
		}
		return nil, fmt.Errorf("%s 失敗（%v）：%s", filepath.Base(name), err, strings.TrimSpace(msg))
	}
	return stdout.Bytes(), nil
}
