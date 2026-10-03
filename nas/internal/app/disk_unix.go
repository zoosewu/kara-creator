//go:build unix

package app

import "syscall"

func diskUsage(path string) *Disk {
	var fs syscall.Statfs_t
	if syscall.Statfs(path, &fs) != nil {
		return nil
	}
	return &Disk{Total: uint64(fs.Blocks) * uint64(fs.Bsize), Free: uint64(fs.Bavail) * uint64(fs.Bsize)}
}
