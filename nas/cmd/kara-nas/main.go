// kara-nas：伴唱帶工作室的 NAS 伺服器（曲庫、下載、排程、網頁 UI）。
//
//	kara-nas --library /library [--init]   啟動伺服器
//	kara-nas openapi                       輸出 OpenAPI 規格（docs/openapi.json）
//	kara-nas restore --library /library    從資料備份重建曲庫（伺服器要先關閉）
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
	"github.com/zoosewu/kara-creator/nas/internal/app"
	"github.com/zoosewu/kara-creator/nas/internal/config"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "openapi" {
		data, err := api.OpenAPI()
		if err != nil {
			log.Fatal(err)
		}
		os.Stdout.Write(append(data, '\n'))
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "restore" {
		if err := runRestore(os.Args[2:]); err != nil && !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
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
	a, err := app.New(cfg)
	if err != nil {
		return err
	}
	log.Printf("曲庫：%s（%d 首歌）", a.Store.Root(), len(a.Store.SongIDs()))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	a.Run(ctx)

	srv := &http.Server{Addr: cfg.Listen, Handler: api.Handler(a), ReadHeaderTimeout: 10 * time.Second}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	log.Printf("NAS 伺服器啟動：%s", cfg.Listen)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	log.Print("關閉中…")
	shutdown, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// SSE、worker 的 long-poll 等長連線不會自己結束，等不到就直接關。
	if err := srv.Shutdown(shutdown); err != nil {
		srv.Close()
	}
	a.Shutdown()
	return nil
}
