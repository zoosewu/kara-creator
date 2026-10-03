// Package proc 執行外部程式（yt-dlp、ffmpeg）：取消時結束整個 process group。
//
// yt-dlp 的單一執行檔（PyInstaller）會再開一個子程序跑 Python，也會呼叫 ffmpeg；只結束最上層的程序，
// 子程序會留在背景、佔著輸出管線。所以讓它自成一個 process group，取消時整組結束。
package proc

import (
	"context"
	"os/exec"
	"time"
)

// Command 同 exec.CommandContext，但 ctx 結束時結束整個 process group，並且最多等 2 秒輸出管線關閉。
func Command(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	setGroup(cmd)
	cmd.WaitDelay = 2 * time.Second
	return cmd
}
