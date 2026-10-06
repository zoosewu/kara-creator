package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	kara "github.com/zoosewu/kara-creator"
	"github.com/zoosewu/kara-creator/nas/internal/download"
	"github.com/zoosewu/kara-creator/nas/internal/media"
	"github.com/zoosewu/kara-creator/nas/internal/restore"
	"github.com/zoosewu/kara-creator/nas/internal/store"
)

type replaceFlag map[string]string

func (r replaceFlag) String() string { return "" }

func (r replaceFlag) Set(v string) error {
	id, url, ok := strings.Cut(v, "=")
	if !ok || !(strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://")) {
		return fmt.Errorf("格式是 歌曲id=網址：%s", v)
	}
	r[strings.TrimSpace(id)] = strings.TrimSpace(url)
	return nil
}

// runRestore 是 kara-nas restore：從資料備份重建曲庫（伺服器要先關閉）。
func runRestore(args []string) error {
	fs := flag.NewFlagSet("kara-nas restore", flag.ContinueOnError)
	lib := fs.String("library", os.Getenv("KARA_LIBRARY"), "曲庫根目錄（KARA_LIBRARY）")
	data := fs.String("data", os.Getenv("KARA_DATA"), "資料備份（KARA_DATA，預設 <library>/data）")
	tools := fs.String("tools", os.Getenv("KARA_TOOLS"), "放 yt-dlp、deno 的資料夾（KARA_TOOLS）")
	sources := fs.String("sources", "", "v1 的下載資料夾（output/downloads）：用裡面已經下載好的影片，不重新下載")
	initLib := fs.Bool("init", false, "曲庫不存在時建立新的曲庫")
	overwrite := fs.Bool("overwrite", false, "已經有的歌詞與對時也用備份覆蓋")
	replace := replaceFlag{}
	fs.Var(replace, "replace", "原本的連結失效時指定替代影片：歌曲id=網址（可重複）")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *lib == "" {
		return fmt.Errorf("請用 --library 指定曲庫的資料夾")
	}
	if *data == "" {
		*data = filepath.Join(*lib, "data")
	}
	st, err := store.Open(*lib, *initLib)
	if err != nil {
		return err
	}
	defer st.Close()
	find := func(name string) string {
		if *tools != "" {
			if p, err := exec.LookPath(filepath.Join(*tools, name)); err == nil {
				return p
			}
		}
		p, _ := exec.LookPath(name)
		return p
	}
	tl := media.DefaultTools(*tools)
	d := &download.Downloader{Store: st, Media: tl, YTDLP: find("yt-dlp"), Deno: find("deno"), Node: find("node"),
		FFmpeg: filepath.Dir(tl.FFmpeg)}
	if d.YTDLP == "" {
		d = nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	rep, err := restore.Run(ctx, st, d, restore.Options{Data: *data, Sources: *sources, Media: tl, Overwrite: *overwrite, Replace: replace,
		AlignVer: kara.Current.Align, Log: func(s string) { fmt.Println(s) }})
	if err != nil {
		return err
	}
	fmt.Printf("還原 %d 首、資料夾 %d 個、用舊檔案 %d 首、下載 %d 首、放回歌詞 %d 首、對時 %d 首\n",
		rep.Songs, rep.Folders, len(rep.Imported), len(rep.Downloaded), rep.Lyrics, rep.Timing)
	if len(rep.Realign) > 0 {
		fmt.Printf("其中 %d 首製作時會重新對時（換了來源、歌詞對不上或對時方法較舊）\n", len(rep.Realign))
	}
	line := strings.Repeat("-", 50)
	if len(rep.Failed) > 0 {
		fmt.Println(line)
		fmt.Printf("以下 %d 首沒能取得影片（連結可能失效）。找到替代影片後用 --replace 歌曲id=網址 再執行一次：\n", len(rep.Failed))
		for _, p := range rep.Failed {
			fmt.Println("  " + p.Describe())
			fmt.Printf("      來源：%s（%s）\n", p.URL, p.Error)
		}
	}
	if len(rep.MissingLocal) > 0 {
		fmt.Println(line)
		fmt.Printf("以下 %d 首是手動放入的影片（沒有連結）。把原檔放回 inbox/、啟動伺服器匯入後再執行一次，"+
			"或用 --replace 歌曲id=網址 改用網路上的影片：\n", len(rep.MissingLocal))
		for _, p := range rep.MissingLocal {
			fmt.Println("  " + p.Describe())
			fmt.Printf("      原始檔案：%s（%.1f MB）\n", p.Entry.Source.OriginalName, float64(p.Entry.Source.Size)/1024/1024)
		}
	}
	fmt.Println(line)
	fmt.Println("下一步：啟動伺服器，在 UI 勾選這些歌批次「製作伴唱帶」（沿用備份的對時，不會重新對時）。")
	return nil
}
