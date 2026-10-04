//go:build !unix

package store

import (
	"errors"
	"os"
)

// ErrLocked 表示另一個 kara-nas 正在使用這個曲庫。
var ErrLocked = errors.New("另一個 kara-nas 正在使用這個曲庫")

func lock(string) (*os.File, error) { return nil, nil } // NAS 只跑在 macOS / Linux
