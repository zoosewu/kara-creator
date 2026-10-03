package export

import (
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile 用 FICLONE（reflink）複製：不佔空間、各自獨立。檔案系統不支援時回傳錯誤。
func cloneFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	err = unix.IoctlFileClone(int(out.Fd()), int(in.Fd()))
	out.Close()
	if err != nil {
		os.Remove(dest)
	}
	return err
}
