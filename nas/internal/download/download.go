// Package download 用 yt-dlp 官方的單一執行檔下載影片。
// 它內含 Python，NAS 上不必安裝 Python；需要 deno（解 YouTube 的 JS）與 ffmpeg（合併影音）。
package download

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/lyrics"
	"github.com/zoosewu/kara-creator/nas/internal/media"
	"github.com/zoosewu/kara-creator/nas/internal/proc"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
)

// 格式：優先 H.264 + AAC，相容性最好；只要音訊時直接取原始串流，不轉檔。
const (
	VideoFormat = "bv*[vcodec^=avc1]+ba[ext=m4a]/bv*[ext=mp4]+ba[ext=m4a]/bv*+ba/b"
	AudioFormat = "ba[ext=m4a]/ba/b"
	// MetaVersion 是 song.json 裡歌曲資訊欄位的版本。
	MetaVersion = 1
)

// Downloader 下載影片到曲庫。
type Downloader struct {
	Store  *store.Store
	Media  media.Tools
	YTDLP  string // yt-dlp 執行檔
	Deno   string // deno 執行檔（YouTube 需要 JavaScript runtime；空字串 = 讓 yt-dlp 自己找）
	Node   string // 沒有 deno 時改用 node
	FFmpeg string // ffmpeg 所在的資料夾（空字串 = 從 PATH 找）
	Now    func() time.Time
}

// Request 是一次下載。
type Request struct {
	URL       string
	AudioOnly bool
	Folder    string // 新歌放進曲庫的哪個資料夾
	Lyrics    string // 一起送來的歌詞（選填）
	Log       func(line string)
	Progress  func(p float64)
}

// Result 是下載結果。
type Result struct {
	ID      string
	Title   string
	Skipped bool // 已經下載過
}

// info 是 yt-dlp -J 的欄位（只取用得到的）。
type info struct {
	Type       string   `json:"_type"`
	ID         string   `json:"id"`
	Extractor  string   `json:"extractor_key"`
	WebpageURL string   `json:"webpage_url"`
	Title      string   `json:"title"`
	Uploader   string   `json:"uploader"`
	Channel    string   `json:"channel"`
	Track      string   `json:"track"`
	Artists    []string `json:"artists"`
	Artist     string   `json:"artist"`
	Duration   float64  `json:"duration"`
}

var unsafeID = regexp.MustCompile(`[^A-Za-z0-9_-]`)

// SongID 是歌曲 id：YouTube 用影片 id，其他網站用「網站-影片 id」。
func SongID(extractor, videoID string) string {
	if strings.EqualFold(extractor, "youtube") {
		return unsafeID.ReplaceAllString(videoID, "_")
	}
	return unsafeID.ReplaceAllString(strings.ToLower(extractor)+"-"+videoID, "_")
}

func (d *Downloader) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now()
}

func (d *Downloader) args(extra ...string) []string {
	args := []string{"--no-playlist", "--no-warnings"}
	switch {
	case d.Deno != "":
		args = append(args, "--js-runtimes", "deno:"+d.Deno)
	case d.Node != "":
		args = append(args, "--js-runtimes", "node:"+d.Node)
	}
	if d.FFmpeg != "" {
		args = append(args, "--ffmpeg-location", d.FFmpeg)
	}
	return append(args, extra...)
}

// Download 下載一首歌。已經下載過（來源檔還在）就略過；取消時 .part 留著，下次續傳。
func (d *Downloader) Download(ctx context.Context, req Request) (Result, error) {
	logf := func(format string, a ...any) {
		if req.Log != nil {
			req.Log(fmt.Sprintf(format, a...))
		}
	}
	logf("下載 %s", req.URL)

	// 1. 取得資訊，算出歌曲 id
	raw, err := d.run(ctx, d.args("-J", "--", req.URL)...)
	if err != nil {
		return Result{}, err
	}
	var in info
	if err := json.Unmarshal(raw, &in); err != nil {
		return Result{}, fmt.Errorf("yt-dlp 的資訊讀不懂：%w", err)
	}
	if in.Type == "playlist" {
		return Result{}, errors.New("這是播放清單的網址，請貼單一影片的網址")
	}
	if in.ID == "" {
		return Result{}, errors.New("網址沒有可下載的內容")
	}
	id := SongID(in.Extractor, in.ID)
	sg, existing := d.Store.Song(id)
	if !existing {
		// 還沒在曲庫裡：先把一起送來的歌詞存起來，下載影片失敗也不會遺失，下次用同樣的網址下載就直接有歌詞
		if err := d.KeepLyrics(id, req.Lyrics, logf); err != nil {
			return Result{}, err
		}
	}
	if existing {
		if _, err := os.Stat(d.Store.SongPath(id, sg.Source.File.Name)); err == nil {
			logf("已下載過，略過下載")
			return Result{ID: id, Title: sg.Source.Title, Skipped: true}, d.saveLyrics(id, req.Lyrics, logf)
		}
		logf("來源檔不見了，重新下載")
	}

	// 2. 下載到 songs/<id>/.downloading/，用剛才的資訊（--load-info-json），不必再解析一次
	tmp := d.Store.SongPath(id, ".downloading")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return Result{}, err
	}
	infoPath := filepath.Join(tmp, "info.json")
	if err := os.WriteFile(infoPath, raw, 0o644); err != nil {
		return Result{}, err
	}
	format := VideoFormat
	extra := []string{"--merge-output-format", "mp4"}
	if req.AudioOnly {
		format, extra = AudioFormat, nil
	}
	args := d.args(append(extra, "-f", format, "--newline", "--continue",
		"--progress-template", "download:KARA-PROGRESS %(progress.downloaded_bytes)s %(progress.total_bytes)s %(progress.total_bytes_estimate)s",
		"-o", filepath.Join(tmp, "source.%(ext)s"), "--load-info-json", infoPath)...)
	err = d.stream(ctx, args, logf, req.Progress)
	if err != nil && ctx.Err() == nil && strings.Contains(err.Error(), "HTTP Error 403") {
		// YouTube 有時會拒絕剛才取得的影片網址（403，常常是暫時的）：重新取得一次資訊與網址再試
		logf("YouTube 拒絕下載（403），重新取得影片網址再試一次")
		if fresh, ferr := d.run(ctx, d.args("-J", "--", req.URL)...); ferr == nil {
			if werr := os.WriteFile(infoPath, fresh, 0o644); werr != nil {
				return Result{}, werr
			}
		}
		err = d.stream(ctx, args, logf, req.Progress)
	}
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
		return Result{}, err
	}

	// 3. 搬成 songs/<id>/source.<ext>，寫 song.json
	file, err := findMedia(tmp)
	if err != nil {
		return Result{}, err
	}
	name := "source" + strings.ToLower(filepath.Ext(file))
	dst := d.Store.SongPath(id, name)
	if err := os.Rename(file, dst); err != nil {
		return Result{}, err
	}
	_ = os.RemoveAll(tmp)
	ref, err := store.Refresh(dst, song.FileRef{})
	if err != nil {
		return Result{}, err
	}
	probe, err := d.Media.Probe(ctx, dst)
	if err != nil {
		return Result{}, err
	}
	artists := in.Artists
	if len(artists) == 0 && in.Artist != "" {
		artists = []string{in.Artist}
	}
	src := song.Source{Kind: song.KindURL, URL: first(in.WebpageURL, req.URL), Extractor: in.Extractor, VideoID: in.ID,
		Title: in.Title, Uploader: in.Uploader, Channel: in.Channel, Track: in.Track, Artists: artists,
		Duration: first(probe.Duration, in.Duration), Mode: "video", File: ref, AddedAt: d.now().Format(time.RFC3339),
		MetaVersion: MetaVersion}
	if req.AudioOnly || !probe.HasVideo {
		src.Mode = "audio"
	}
	if probe.HasVideo {
		src.Width, src.Height = probe.Width, probe.Height
	}
	if _, ok := d.Store.Song(id); ok {
		err = d.Store.EditSong(id, func(s *song.Song) error { s.Source = src; return nil })
	} else {
		folder := req.Folder
		if folder == "" {
			folder = library.Root
		}
		err = d.Store.AddSong(song.New(id, src), folder)
	}
	if err != nil {
		return Result{}, err
	}
	logf("下載完成：%s", in.Title)
	if !existing {
		return Result{ID: id, Title: in.Title}, nil // 歌詞在下載之前就存好了
	}
	return Result{ID: id, Title: in.Title}, d.saveLyrics(id, req.Lyrics, logf)
}

// YouTubeID 從 YouTube 的網址取出歌曲 id（不連網）；不是 YouTube 的網址回傳 false。
// 新增歌曲時用它在按下去的當下就存歌詞（其他網站要等 yt-dlp 取得資訊才知道 id）。
func YouTubeID(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	var vid string
	switch host {
	case "youtu.be":
		vid = strings.Trim(u.Path, "/")
	case "youtube.com", "m.youtube.com", "music.youtube.com":
		vid = u.Query().Get("v")
		for _, prefix := range []string{"/shorts/", "/live/", "/embed/"} {
			if rest, ok := strings.CutPrefix(u.Path, prefix); ok {
				vid = strings.Trim(rest, "/")
			}
		}
	}
	if !youtubeID.MatchString(vid) {
		return "", false
	}
	return SongID("Youtube", vid), true
}

var youtubeID = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// KeepLyrics 把新增歌曲時一起送來的歌詞存到 songs/<id>/lyrics.txt（曲庫裡還沒有這首歌時；有的話不動，下載完由 saveLyrics 決定）。
// 還沒下載完的歌只有這個檔、沒有 song.json，曲庫不會顯示它；之後同一個網址下載成功時就直接用這份歌詞。
// 上次留下的歌詞會被這次送來的取代（以最新的為準）。
func (d *Downloader) KeepLyrics(id, text string, logf func(string, ...any)) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	if _, ok := d.Store.Song(id); ok {
		return nil
	}
	if err := os.MkdirAll(d.Store.SongPath(id), 0o755); err != nil {
		return err
	}
	if err := store.WriteFile(d.Store.SongPath(id, song.FileLyrics), []byte(lyrics.Serialize(lyrics.Parse(text)))); err != nil {
		return err
	}
	logf("歌詞已先存進曲庫（下載失敗也不會遺失）")
	return nil
}

// saveLyrics 存一起送來的歌詞（已經有歌詞時不覆蓋）。
func (d *Downloader) saveLyrics(id, text string, logf func(string, ...any)) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	path := d.Store.SongPath(id, song.FileLyrics)
	if _, err := os.Stat(path); err == nil {
		logf("已經有歌詞，不覆蓋一起送來的歌詞")
		return nil
	}
	if err := store.WriteFile(path, []byte(lyrics.Serialize(lyrics.Parse(text)))); err != nil {
		return err
	}
	logf("歌詞已存檔")
	return nil
}

func first[T comparable](values ...T) T {
	var zero T
	for _, v := range values {
		if v != zero {
			return v
		}
	}
	return zero
}

var partial = regexp.MustCompile(`(?i)\.(part|ytdl|temp)$|\.f\d+\.\w+$`)

// findMedia 找下載好的媒體檔（略過暫存檔與分段檔）。
func findMedia(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	for _, e := range entries {
		if !e.IsDir() && media.IsMedia(e.Name()) && !partial.MatchString(e.Name()) {
			return filepath.Join(dir, e.Name()), nil
		}
	}
	return "", errors.New("下載完成但找不到媒體檔")
}

func (d *Downloader) run(ctx context.Context, args ...string) ([]byte, error) {
	cmd := proc.Command(ctx, d.YTDLP, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New(message(stderr.String(), err))
	}
	return stdout.Bytes(), nil
}

// message 從 yt-dlp 的錯誤輸出挑出給人看的那一行。
func message(stderr string, err error) string {
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		if line := strings.TrimSpace(lines[i]); strings.HasPrefix(line, "ERROR:") {
			return "下載失敗：" + strings.TrimSpace(strings.TrimPrefix(line, "ERROR:"))
		}
	}
	if s := strings.TrimSpace(stderr); s != "" {
		return "下載失敗：" + lines[len(lines)-1]
	}
	return "下載失敗：" + err.Error()
}

// stream 執行下載，逐行轉給紀錄、解析進度。
func (d *Downloader) stream(ctx context.Context, args []string, logf func(string, ...any), progress func(float64)) error {
	cmd := proc.Command(ctx, d.YTDLP, args...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	readLines(out, func(line string) {
		if rest, ok := strings.CutPrefix(line, "KARA-PROGRESS "); ok {
			if p, ok := parseProgress(rest); ok && progress != nil {
				progress(p)
			}
			return
		}
		if line = strings.TrimSpace(line); line != "" {
			logf("    %s", line)
		}
	})
	if err := cmd.Wait(); err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return errors.New(message(stderr.String(), err))
	}
	return nil
}

func readLines(r io.Reader, fn func(string)) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		fn(sc.Text())
	}
}

// parseProgress 解析「已下載 總大小 估計大小」（沒有的欄位是 NA）。
func parseProgress(s string) (float64, bool) {
	f := strings.Fields(s)
	if len(f) != 3 {
		return 0, false
	}
	done, err := strconv.ParseFloat(f[0], 64)
	if err != nil {
		return 0, false
	}
	for _, t := range f[1:] {
		if total, err := strconv.ParseFloat(t, 64); err == nil && total > 0 {
			return min(1, done/total), true
		}
	}
	return 0, false
}
