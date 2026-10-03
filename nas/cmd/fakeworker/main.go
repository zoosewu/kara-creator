// fakeworker：假的 AI worker，回傳固定的結果，不需要 GPU 與模型。開發 NAS 伺服器與前端時用。
//
//	go run ./nas/cmd/fakeworker --nas http://127.0.0.1:8765 --delay 2s
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/zoosewu/kara-creator/nas/internal/fakeworker"
)

func main() {
	var opt fakeworker.Options
	var channels string
	flag.StringVar(&opt.NAS, "nas", "http://127.0.0.1:8765", "NAS 的網址")
	flag.StringVar(&opt.Name, "name", "fake", "worker 名稱")
	flag.StringVar(&opt.Token, "token", os.Getenv("KARA_WORKER_TOKEN"), "共用 token")
	flag.StringVar(&channels, "channels", "heavy,interactive", "開哪些通道")
	flag.DurationVar(&opt.Delay, "delay", 0, "每件任務假裝要算多久")
	flag.Parse()
	opt.Channels = strings.Split(channels, ",")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := fakeworker.Run(ctx, opt); err != nil {
		log.Fatal(err)
	}
}
