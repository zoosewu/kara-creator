package download

import (
	"context"
	"errors"
	"os"
	"strings"
)

// Version 回傳 yt-dlp 的版本。
func (d *Downloader) Version(ctx context.Context) (string, error) {
	out, err := d.run(ctx, "--version")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// prev 是更新前留下的上一版執行檔。
func (d *Downloader) prev() string { return d.YTDLP + ".prev" }

// Update 更新 yt-dlp（Q10：啟動時與每天一次）。更新前把目前的執行檔留成 .prev，新版有問題時可以退回。
// 回傳更新前後的版本（相同代表已經是最新版）。
func (d *Downloader) Update(ctx context.Context) (before, after string, err error) {
	if before, err = d.Version(ctx); err != nil {
		return "", "", err
	}
	data, err := os.ReadFile(d.YTDLP)
	if err != nil {
		return before, before, err
	}
	backup := d.prev() + ".tmp"
	if err := os.WriteFile(backup, data, 0o755); err != nil {
		return before, before, err
	}
	defer os.Remove(backup)
	if _, err := d.run(ctx, "-U"); err != nil {
		return before, before, err
	}
	if after, err = d.Version(ctx); err != nil {
		// 新版跑不起來：還原
		_ = os.Rename(backup, d.YTDLP)
		return before, before, errors.New("更新後的 yt-dlp 無法執行，已經還原成 " + before)
	}
	if after != before {
		if err := os.Rename(backup, d.prev()); err != nil {
			return before, after, err
		}
	}
	return before, after, nil
}

// PrevVersion 回傳可以退回的上一版（沒有時為空字串）。
func (d *Downloader) PrevVersion(ctx context.Context) string {
	if _, err := os.Stat(d.prev()); err != nil {
		return ""
	}
	old := *d
	old.YTDLP = d.prev()
	v, _ := old.Version(ctx)
	return v
}

// Rollback 換回上一版（新版下載失敗時用）。目前的版本變成 .prev，可以再換回來。
func (d *Downloader) Rollback() error {
	if _, err := os.Stat(d.prev()); err != nil {
		return errors.New("沒有可以退回的上一版")
	}
	tmp := d.YTDLP + ".swap"
	if err := os.Rename(d.YTDLP, tmp); err != nil {
		return err
	}
	if err := os.Rename(d.prev(), d.YTDLP); err != nil {
		_ = os.Rename(tmp, d.YTDLP)
		return err
	}
	return os.Rename(tmp, d.prev())
}
