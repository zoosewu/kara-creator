// Package fakeworker 是假的 AI worker：照 worker 協定向 NAS 領任務，回傳固定的結果，不需要 GPU 與模型。
// 用來測試 NAS 的整條流程、取消、改派、版本不符、斷線。
//
// 假的結果：去人聲把輸入音訊原封不動當成人聲和伴奏；對時把每句平均分配在音訊長度裡；
// 檢查一律沒問題；讀音一律沒有自動讀音；燒錄把輸入影片直接當成成品。
package fakeworker

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	kara "github.com/zoosewu/kara-creator"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// Options 設定假的 worker。
type Options struct {
	NAS      string             // NAS 的網址，例如 http://127.0.0.1:8765
	Name     string             // worker 名稱
	Token    string             // 共用 token（NAS 沒設時留空）
	Channels []string           // 預設兩個通道都開
	Versions *kara.Versions     // 預設 kara.Current；測試版本不符時換掉
	Delay    time.Duration      // 每件任務假裝要算多久（測試取消、心跳用）
	Fail     map[string]wp.Fail // 任務種類 → 回報失敗（Instance 會自動填）
	Client   *http.Client
	Logf     func(format string, args ...any)
}

// ErrVersion 表示 NAS 拒絕了這個 worker 的版本。
var ErrVersion = errors.New("版本和 NAS 不同")

type worker struct {
	opt      Options
	instance string
	hello    wp.HelloResponse

	mu    sync.Mutex
	blobs map[string][]byte // 輸入檔快取（sha256 → 內容）
	fresh []string          // 上次 lease 之後新增到快取的 sha256
}

// Run 連上 NAS 並處理任務，直到 ctx 結束（結束前送 bye）。
func Run(ctx context.Context, opt Options) error {
	if opt.Name == "" {
		opt.Name = "fake"
	}
	if len(opt.Channels) == 0 {
		opt.Channels = []string{wp.ChannelHeavy, wp.ChannelInteractive}
	}
	if opt.Versions == nil {
		opt.Versions = &kara.Current
	}
	if opt.Client == nil {
		opt.Client = &http.Client{}
	}
	if opt.Logf == nil {
		opt.Logf = log.Printf
	}
	w := &worker{opt: opt, instance: randomID(), blobs: map[string][]byte{}}

	if err := w.sayHello(ctx); err != nil {
		return err
	}
	var wg sync.WaitGroup
	errc := make(chan error, len(opt.Channels))
	for _, ch := range opt.Channels {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := w.loop(ctx, ch); err != nil {
				errc <- err
			}
		}()
	}
	wg.Wait()
	close(errc)

	bye, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, _ = w.post(bye, "/bye", wp.Bye{Instance: w.instance}, nil)
	return <-errc
}

func (w *worker) sayHello(ctx context.Context) error {
	kinds := []string{}
	for _, ch := range w.opt.Channels {
		if ch == wp.ChannelHeavy {
			kinds = append(kinds, wp.KindSeparate, wp.KindAlign, wp.KindAlignFrom, wp.KindAlignLine, wp.KindQA, wp.KindRender)
		} else {
			kinds = append(kinds, wp.KindReading)
		}
	}
	req := wp.Hello{Name: w.opt.Name, Instance: w.instance, Versions: *w.opt.Versions,
		Kinds: kinds, Channels: w.opt.Channels, Cached: []string{}}
	status, err := w.post(ctx, "/hello", req, &w.hello)
	if status == http.StatusConflict {
		return fmt.Errorf("%w：%v", ErrVersion, err)
	}
	return err
}

// loop 在一個通道上不斷領任務；同一個通道一次一件。
func (w *worker) loop(ctx context.Context, channel string) error {
	for ctx.Err() == nil {
		w.mu.Lock()
		cached := w.fresh
		w.fresh = nil
		w.mu.Unlock()

		var task wp.Task
		status, err := w.post(ctx, "/lease", wp.LeaseRequest{Instance: w.instance, Channel: channel, Cached: cached}, &task)
		switch {
		case ctx.Err() != nil:
			return nil
		case status == http.StatusConflict:
			return fmt.Errorf("%w：%v", ErrVersion, err)
		case err != nil:
			w.opt.Logf("fakeworker：領任務失敗：%v", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Second):
			}
			continue
		case status == http.StatusNoContent:
			continue
		}
		w.run(ctx, task)
	}
	return nil
}

// run 執行一件任務：心跳、取輸入檔、算結果、上傳、完成。
func (w *worker) run(ctx context.Context, task wp.Task) {
	tctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go w.heartbeat(tctx, cancel, task.ID)

	if f, ok := w.opt.Fail[task.Kind]; ok {
		f.Instance = w.instance
		w.report(ctx, task.ID, "fail", f)
		return
	}
	inputs := map[string][]byte{}
	for name, blob := range task.Inputs {
		data, err := w.blob(tctx, blob)
		if err != nil {
			w.fail(ctx, task, err)
			return
		}
		inputs[name] = data
	}
	select {
	case <-tctx.Done():
		return // 取消或任務被收回：丟掉
	case <-time.After(w.opt.Delay):
	}
	result, files, err := compute(task, inputs)
	if err != nil {
		w.fail(ctx, task, err)
		return
	}
	shas := map[string]string{}
	for name, data := range files {
		var up wp.Uploaded
		if _, err := w.do(tctx, http.MethodPut, "/tasks/"+task.ID+"/files/"+name+"?instance="+w.instance, bytes.NewReader(data), &up); err != nil {
			w.opt.Logf("fakeworker：上傳 %s 失敗：%v", name, err)
			return
		}
		shas[name] = up.SHA256
	}
	raw, _ := json.Marshal(result)
	if tctx.Err() != nil {
		return
	}
	w.report(ctx, task.ID, "complete", wp.Complete{Instance: w.instance, Result: raw, Files: shas})
}

func (w *worker) fail(ctx context.Context, task wp.Task, err error) {
	w.report(ctx, task.ID, "fail", wp.Fail{Instance: w.instance, Error: err.Error(), Retryable: false})
}

// report 送出 complete / fail。正在關閉時也要送出去（算好的結果不要丟掉），所以不跟著 ctx 取消。
func (w *worker) report(ctx context.Context, id, what string, body any) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := w.post(ctx, "/tasks/"+id+"/"+what, body, nil); err != nil {
		w.opt.Logf("fakeworker：回報 %s 失敗：%v", what, err)
	}
}

// heartbeat 定期送 progress；NAS 要取消、或任務已經不是自己的（404 / 409）就停止這件任務。
func (w *worker) heartbeat(ctx context.Context, stop context.CancelFunc, id string) {
	every := time.Duration(w.hello.HeartbeatSeconds) * time.Second
	if every <= 0 || every > time.Second {
		every = time.Second
	}
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		var resp wp.ProgressResponse
		status, _ := w.post(ctx, "/tasks/"+id+"/progress", wp.Progress{Instance: w.instance, Logs: []string{"  . 假的 worker 處理中..."}}, &resp)
		if resp.Cancel || status == http.StatusNotFound || status == http.StatusConflict {
			stop()
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (w *worker) blob(ctx context.Context, b wp.Blob) ([]byte, error) {
	w.mu.Lock()
	data, ok := w.blobs[b.SHA256]
	w.mu.Unlock()
	if ok {
		return data, nil
	}
	var buf bytes.Buffer
	if _, err := w.do(ctx, http.MethodGet, "/blobs/"+b.SHA256, nil, &buf); err != nil {
		return nil, err
	}
	data = buf.Bytes()
	if sum := sha256.Sum256(data); hex.EncodeToString(sum[:]) != b.SHA256 {
		return nil, fmt.Errorf("輸入檔 %s 的內容和 sha256 不符", b.SHA256[:12])
	}
	w.mu.Lock()
	w.blobs[b.SHA256] = data
	w.fresh = append(w.fresh, b.SHA256)
	w.mu.Unlock()
	return data, nil
}

// ---- HTTP ----------------------------------------------------------------------

func (w *worker) post(ctx context.Context, path string, body, out any) (int, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return 0, err
	}
	return w.do(ctx, http.MethodPost, path, bytes.NewReader(raw), out)
}

// do 送出請求；out 是 *bytes.Buffer 時放原始內容，否則當 JSON 解析。非 2xx 時回傳錯誤（內容是 NAS 的 detail）。
func (w *worker) do(ctx context.Context, method, path string, body io.Reader, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(w.opt.NAS, "/")+wp.Prefix+path, body)
	if err != nil {
		return 0, err
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
	}
	if w.opt.Token != "" {
		req.Header.Set("Authorization", wp.BearerPrefix+w.opt.Token)
	}
	resp, err := w.opt.Client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return resp.StatusCode, fmt.Errorf("%s %s：HTTP %d %s", method, path, resp.StatusCode, bytes.TrimSpace(msg))
	}
	if resp.StatusCode == http.StatusNoContent || out == nil {
		return resp.StatusCode, nil
	}
	if buf, ok := out.(*bytes.Buffer); ok {
		_, err = buf.ReadFrom(resp.Body)
		return resp.StatusCode, err
	}
	return resp.StatusCode, json.NewDecoder(resp.Body).Decode(out)
}

func randomID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ---- 假的結果 ------------------------------------------------------------------

func compute(task wp.Task, inputs map[string][]byte) (any, map[string][]byte, error) {
	audio := inputs[wp.InputAudio]
	switch task.Kind {
	case wp.KindSeparate:
		return struct{}{}, map[string][]byte{wp.FileVocals: audio, wp.FileNoVocals: audio}, nil
	case wp.KindAlign:
		var p wp.AlignParams
		if err := json.Unmarshal(task.Params, &p); err != nil {
			return nil, nil, err
		}
		return wp.AlignResult{Lines: spread(p.Texts, 0, wavSeconds(audio, len(p.Texts)))}, nil, nil
	case wp.KindAlignFrom:
		var p wp.AlignFromParams
		if err := json.Unmarshal(task.Params, &p); err != nil {
			return nil, nil, err
		}
		if p.First < 0 || p.First >= len(p.Texts) {
			return nil, nil, fmt.Errorf("first=%d 超出歌詞範圍", p.First)
		}
		end := max(wavSeconds(audio, len(p.Texts)), p.Anchor+float64(len(p.Texts)-p.First))
		return wp.AlignResult{Lines: spread(p.Texts[p.First:], p.Anchor, end)}, nil, nil
	case wp.KindAlignLine:
		var p wp.AlignLineParams
		if err := json.Unmarshal(task.Params, &p); err != nil {
			return nil, nil, err
		}
		end := p.T0 + 3
		if p.T1 != nil {
			end = *p.T1
		}
		return wp.AlignLineResult{Line: spread([]string{p.Text}, p.T0, end)[0]}, nil, nil
	case wp.KindQA:
		var p wp.QAParams
		if err := json.Unmarshal(task.Params, &p); err != nil {
			return nil, nil, err
		}
		var r wp.QAResult
		r.Doc.Lines = []wp.QALine{}
		for i, l := range p.Lines {
			r.Doc.Lines = append(r.Doc.Lines, wp.QALine{Index: i, Text: l.Text, Start: l.Start, End: l.End,
				Status: "ok", Reasons: []string{}, Match: 1})
		}
		return r, nil, nil
	case wp.KindReading:
		var p wp.ReadingParams
		if err := json.Unmarshal(task.Params, &p); err != nil {
			return nil, nil, err
		}
		r := wp.ReadingResult{Lines: make([][]wp.Span, len(p.Texts))}
		for i := range r.Lines {
			r.Lines[i] = []wp.Span{}
		}
		return r, nil, nil
	case wp.KindRender:
		ass, manual := inputs[wp.InputASS]
		files := map[string][]byte{wp.FileVideo: inputs[wp.InputMedia]}
		if !manual {
			ass = []byte("[Script Info]\nScriptType: v4.00+\n; 假的 worker 產生\n")
			files[wp.FileASS] = ass
		}
		sum := sha256.Sum256(ass)
		return wp.RenderResult{Width: 1920, Height: 1080, AssSHA256: hex.EncodeToString(sum[:])}, files, nil
	}
	return nil, nil, fmt.Errorf("不支援的任務種類：%s", task.Kind)
}

// spread 把每句平均分配在 [t0, t1)，句內每個字也平均分配。
func spread(texts []string, t0, t1 float64) []wp.Line {
	lines := make([]wp.Line, len(texts))
	if len(texts) == 0 {
		return lines
	}
	step := (t1 - t0) / float64(len(texts))
	for i, text := range texts {
		start := t0 + step*float64(i)
		end := start + step*0.9
		units := splitUnits(text)
		words := make([]wp.Word, len(units))
		for k, u := range units {
			ws := start + (end-start)*float64(k)/float64(len(units))
			we := start + (end-start)*float64(k+1)/float64(len(units))
			words[k] = wp.Word{Text: u, Start: round3(ws), End: round3(we)}
		}
		lines[i] = wp.Line{Text: text, Start: round3(start), End: round3(end), Words: words}
	}
	return lines
}

// splitUnits 切成變色單位：拉丁字母連在一起算一個字（含後面的空白），其他字元一個一個。
func splitUnits(text string) []string {
	var out []string
	runes := []rune(text)
	for i := 0; i < len(runes); {
		j := i + 1
		if isLatin(runes[i]) {
			for j < len(runes) && (isLatin(runes[j]) || runes[j] == '\'') {
				j++
			}
		}
		for j < len(runes) && unicode.IsSpace(runes[j]) {
			j++
		}
		out = append(out, string(runes[i:j]))
		i = j
	}
	if len(out) == 0 {
		out = []string{text}
	}
	return out
}

func isLatin(r rune) bool { return r < 0x250 && unicode.IsLetter(r) }

func round3(x float64) float64 { return float64(int64(x*1000+0.5)) / 1000 }

// wavSeconds 讀 wav 的長度；讀不出來時假設每句 3 秒。
func wavSeconds(data []byte, lines int) float64 {
	fallback := float64(max(lines, 1)) * 3
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		return fallback
	}
	var byteRate uint32
	for pos := 12; pos+8 <= len(data); {
		id, size := string(data[pos:pos+4]), binary.LittleEndian.Uint32(data[pos+4:pos+8])
		body := pos + 8
		switch id {
		case "fmt ":
			if body+12 <= len(data) {
				byteRate = binary.LittleEndian.Uint32(data[body+8 : body+12])
			}
		case "data":
			if byteRate == 0 {
				return fallback
			}
			return float64(size) / float64(byteRate)
		}
		pos = body + int(size) + int(size&1)
	}
	return fallback
}
