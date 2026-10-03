// Package config 解析 kara-nas 的啟動參數。每個參數都有對應的環境變數（container 用），參數優先。
package config

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"path/filepath"
)

// Config 是 NAS 伺服器的設定。
type Config struct {
	Library     string // 曲庫根目錄（KARA_LIBRARY）
	Data        string // 資料備份的 git repo（KARA_DATA；預設 <library>/data）
	Listen      string // 監聽位址（KARA_LISTEN）
	WorkerToken string // worker 連線用的共用 token，空字串 = 不檢查（KARA_WORKER_TOKEN）
	Tools       string // 放 yt-dlp、deno 的資料夾，空字串 = 從 PATH 找（KARA_TOOLS）
	Init        bool   // 曲庫不存在時建立新的（避免外接硬碟沒掛上時誤開一個空曲庫）
}

// Parse 解析參數；getenv 通常是 os.Getenv（測試時可以換掉）。
func Parse(args []string, getenv func(string) string, stderr io.Writer) (Config, error) {
	env := func(name, fallback string) string {
		if v := getenv(name); v != "" {
			return v
		}
		return fallback
	}

	var c Config
	fs := flag.NewFlagSet("kara-nas", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&c.Library, "library", env("KARA_LIBRARY", ""), "曲庫根目錄（KARA_LIBRARY）")
	fs.StringVar(&c.Data, "data", env("KARA_DATA", ""), "資料備份的 git repo（KARA_DATA，預設 <library>/data）")
	fs.StringVar(&c.Listen, "listen", env("KARA_LISTEN", ":8765"), "監聽位址（KARA_LISTEN）")
	fs.StringVar(&c.WorkerToken, "worker-token", env("KARA_WORKER_TOKEN", ""), "AI worker 連線用的共用 token（KARA_WORKER_TOKEN，預設不檢查）")
	fs.StringVar(&c.Tools, "tools", env("KARA_TOOLS", ""), "放 yt-dlp、deno 的資料夾（KARA_TOOLS，預設從 PATH 找）")
	fs.BoolVar(&c.Init, "init", false, "曲庫不存在時建立新的曲庫")
	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}
	if fs.NArg() > 0 {
		return Config{}, fmt.Errorf("不認得的參數：%v", fs.Args())
	}
	if c.Library == "" {
		return Config{}, errors.New("請用 --library（或環境變數 KARA_LIBRARY）指定曲庫的資料夾")
	}
	c.Library = filepath.Clean(c.Library)
	if c.Data == "" {
		c.Data = filepath.Join(c.Library, "data")
	}
	return c, nil
}
