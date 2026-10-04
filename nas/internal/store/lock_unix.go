//go:build unix

package store

import (
	"errors"
	"os"
	"syscall"
)

// ErrLocked 表示另一個 kara-nas（伺服器或 restore）正在使用這個曲庫。
var ErrLocked = errors.New("另一個 kara-nas 正在使用這個曲庫（伺服器還開著？請先關閉再執行）")

// lock 取得曲庫的獨佔鎖；程式結束時自動放開。
func lock(root string) (*os.File, error) {
	f, err := os.OpenFile(root+"/.kara-nas.lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, ErrLocked
	}
	return f, nil
}
