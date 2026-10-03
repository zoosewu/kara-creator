package export

import "golang.org/x/sys/unix"

// cloneFile 用 APFS 的 clonefile：不佔空間、各自獨立。
func cloneFile(src, dest string) error { return unix.Clonefile(src, dest, 0) }
