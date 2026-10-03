package store

import (
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"

	"github.com/zoosewu/kara-creator/nas/internal/song"
)

// HashFile 算檔案的 sha256 與大小。
func HashFile(path string) (sum string, size int64, err error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	size, err = io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), size, nil
}

// RefreshRef 確認 songs/<id>/<name> 和紀錄相同：大小與修改時間都沒變就沿用紀錄的 sha256，否則重新計算。
// 檔案不存在時回傳 os.ErrNotExist 類的錯誤。
func (s *Store) RefreshRef(id string, old song.FileRef) (song.FileRef, error) {
	return Refresh(s.SongPath(id, old.Name), old)
}

// Refresh 同 RefreshRef，路徑直接指定（ref.Name 只取檔名）。
func Refresh(path string, old song.FileRef) (song.FileRef, error) {
	st, err := os.Stat(path)
	if err != nil {
		return song.FileRef{}, err
	}
	ref := song.FileRef{Name: filepath.Base(path), Size: st.Size(), MTimeNs: st.ModTime().UnixNano()}
	if old.SHA256 != "" && old.Size == ref.Size && old.MTimeNs == ref.MTimeNs {
		ref.SHA256 = old.SHA256
		return ref, nil
	}
	sum, size, err := HashFile(path)
	if err != nil {
		return song.FileRef{}, err
	}
	ref.SHA256, ref.Size = sum, size
	return ref, nil
}
