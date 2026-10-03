//go:build !unix

package proc

import "os/exec"

func setGroup(cmd *exec.Cmd) {} // NAS 只跑在 macOS / Linux；其他平台只結束最上層的程序
