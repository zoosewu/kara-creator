// kara-nas：伴唱帶工作室的 NAS 伺服器（曲庫、下載、排程、網頁 UI）。
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/zoosewu/kara-creator/nas/internal/api"
	"github.com/zoosewu/kara-creator/nas/internal/config"
)

func main() {
	cfg, err := config.Parse(os.Args[1:], os.Getenv, os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	if err := run(cfg); err != nil {
		log.Fatal(err)
	}
}

func run(cfg config.Config) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	srv := &http.Server{Addr: cfg.Listen, Handler: api.Handler(), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Printf("NAS 伺服器啟動：%s（曲庫 %s）", cfg.Listen, cfg.Library)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Print("關閉中…")
	shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// SSE 等長連線不會自己結束，等不到就直接關。
	if err := srv.Shutdown(shutdown); err != nil {
		srv.Close()
	}
	return nil
}
