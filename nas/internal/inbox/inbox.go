// Package inbox 匯入手動放進 <library>/inbox/ 的影音檔。
//
//   - 直接放在 inbox 的檔案，或放在 inbox 子資料夾裡的第一個影音檔都可以
//   - 匯入時搬到 songs/<id>/source.<副檔名>（原檔名記在 original_name），子資料夾變空就刪掉
//   - id = local- + sha1(「檔名:大小」)[:8]：放回同一個檔案就是同一個 id，重複放入會略過並記錄
//   - 歌名與歌手從檔名辨識（「歌手 - 歌名.mp4」最準）
//
// 為了不匯入還沒寫完的檔案：大小要連續 5 秒沒變；yt-dlp 的暫存檔（.part、.ytdl、.f137.mp4 這類分段檔）一律略過。
// 用檔案監看（fsnotify）觸發，不輪詢，NAS 的硬碟才能休眠。
package inbox

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/fsnotify/fsnotify"

	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/media"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
)

// Stable 是檔案大小要維持不變多久才算寫完。
const Stable = 5 * time.Second

var partial = regexp.MustCompile(`(?i)\.(part|ytdl|temp)$|\.f\d+\.\w+$`)

// LocalID 是手動放入的檔案的歌曲 id（同一個檔案放回來就是同一個 id，資料備份還原時才對得上）。
func LocalID(name string, size int64) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s:%d", name, size)))
	return "local-" + hex.EncodeToString(sum[:])[:8]
}

// Inbox 監看並匯入手動放入的檔案。
type Inbox struct {
	Store *store.Store
	Media media.Tools
	Log   func(line string) // 匯入、略過的紀錄
	Now   func() time.Time

	mu     sync.Mutex
	seen   map[string]sighting // 路徑 → 大小與第一次看到這個大小的時間
	warned map[string]bool
}

type sighting struct {
	size  int64
	since time.Time
}

func (b *Inbox) now() time.Time {
	if b.Now != nil {
		return b.Now()
	}
	return time.Now()
}

func (b *Inbox) log(format string, a ...any) {
	if b.Log != nil {
		b.Log(fmt.Sprintf(format, a...))
	}
}

func isMedia(name string) bool { return media.IsMedia(name) && !partial.MatchString(name) }

// Scan 掃描 inbox 一次，匯入寫完的檔案；回傳新匯入的歌曲 id，以及是否還有沒寫完的檔案（要稍後再掃）。
func (b *Inbox) Scan(ctx context.Context) (added []string, waiting bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.seen == nil {
		b.seen, b.warned = map[string]sighting{}, map[string]bool{}
	}
	root := b.Store.Path(store.InboxDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		b.log("讀不到 inbox 資料夾：%v", err)
		return nil, false
	}
	for _, e := range entries {
		path := filepath.Join(root, e.Name())
		var file string
		switch {
		case !e.IsDir() && isMedia(e.Name()):
			file = path
		case e.IsDir():
			sub, err := os.ReadDir(path)
			if err != nil {
				continue
			}
			var media []string
			busy := false
			for _, s := range sub {
				busy = busy || partial.MatchString(s.Name())
				if !s.IsDir() && isMedia(s.Name()) {
					media = append(media, s.Name())
				}
			}
			if busy || len(media) == 0 {
				waiting = waiting || busy
				continue // yt-dlp 還在下載或合併
			}
			sort.Strings(media)
			file = filepath.Join(path, media[0])
		default:
			continue
		}
		ready, err := b.ready(file)
		if err != nil {
			continue
		}
		if !ready {
			waiting = true
			continue
		}
		if id, ok := b.register(ctx, file); ok {
			added = append(added, id)
		}
	}
	return added, waiting
}

// ready：檔案大小持續不變一段時間了嗎。第一次看到、但早就放好的檔案（伺服器沒開時放進來的）也算。
func (b *Inbox) ready(path string) (bool, error) {
	st, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	now := b.now()
	prev, ok := b.seen[path]
	if !ok || prev.size != st.Size() {
		b.seen[path] = sighting{st.Size(), now}
		return now.Sub(st.ModTime()) >= 30*time.Second, nil
	}
	return now.Sub(prev.since) >= Stable, nil
}

func (b *Inbox) register(ctx context.Context, path string) (string, bool) {
	st, err := os.Stat(path)
	if err != nil {
		return "", false
	}
	name := filepath.Base(path)
	id := LocalID(name, st.Size())
	if _, exists := b.Store.Song(id); exists {
		if !b.warned[path] {
			b.log("略過重複的檔案（曲庫裡已經有同一個檔案）：%s", name)
			b.warned[path] = true
		}
		return "", false
	}
	info, err := b.Media.Probe(ctx, path)
	if err != nil {
		b.log("無法讀取 %s，略過：%v", name, err)
		b.warned[path] = true
		return "", false
	}
	if err := os.MkdirAll(b.Store.SongPath(id), 0o755); err != nil {
		b.log("匯入 %s 失敗：%v", name, err)
		return "", false
	}
	dst := b.Store.SongPath(id, "source"+strings.ToLower(filepath.Ext(name)))
	if err := os.Rename(path, dst); err != nil {
		b.log("匯入 %s 失敗（檔案被佔用？下次再試）：%v", name, err)
		return "", false
	}
	delete(b.seen, path)
	if dir := filepath.Dir(path); dir != b.Store.Path(store.InboxDir) {
		_ = os.Remove(dir) // 子資料夾變空就刪掉（還有其他檔案時刪不掉，沒關係）
	}
	ref, err := store.Refresh(dst, song.FileRef{})
	if err != nil {
		b.log("匯入 %s 失敗：%v", name, err)
		return "", false
	}
	src := song.Source{Kind: song.KindLocal, Title: strings.TrimSuffix(name, filepath.Ext(name)), OriginalName: name,
		Duration: info.Duration, Mode: "audio", File: ref, AddedAt: b.now().Format(time.RFC3339)}
	if info.HasVideo {
		src.Mode, src.Width, src.Height = "video", info.Width, info.Height
	}
	if err := b.Store.AddSong(song.New(id, src), library.Root); err != nil {
		b.log("匯入 %s 失敗：%v", name, err)
		return "", false
	}
	b.log("匯入手動放入的檔案：%s", name)
	return id, true
}

// Watch 監看 inbox，有變動時（檔案寫完後）匯入，直到 ctx 結束。onAdded 在每次有新歌時呼叫。
func (b *Inbox) Watch(ctx context.Context, onAdded func(ids []string)) error {
	w, err := fsnotify.NewWatcher()
	if err != nil {
		return err
	}
	defer w.Close()
	root := b.Store.Path(store.InboxDir)
	if err := w.Add(root); err != nil {
		return err
	}
	watchSubdirs(w, root)

	scan := func() time.Duration {
		added, waiting := b.Scan(ctx)
		if len(added) > 0 && onAdded != nil {
			onAdded(added)
		}
		if waiting {
			return Stable + 500*time.Millisecond
		}
		return 0
	}
	timer := time.NewTimer(0) // 啟動時先掃一次（伺服器沒開時放進來的）
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case ev, ok := <-w.Events:
			if !ok {
				return nil
			}
			if ev.Has(fsnotify.Create) {
				if st, err := os.Stat(ev.Name); err == nil && st.IsDir() {
					_ = w.Add(ev.Name)
				}
			}
			timer.Reset(Stable + 500*time.Millisecond) // 檔案還在寫入時事件會一直來：安靜下來後再掃
		case err, ok := <-w.Errors:
			if !ok {
				return nil
			}
			b.log("監看 inbox 出錯：%v", err)
		case <-timer.C:
			if again := scan(); again > 0 {
				timer.Reset(again)
			}
		}
	}
}

func watchSubdirs(w *fsnotify.Watcher, root string) {
	entries, _ := os.ReadDir(root)
	for _, e := range entries {
		if e.IsDir() {
			_ = w.Add(filepath.Join(root, e.Name()))
		}
	}
}
