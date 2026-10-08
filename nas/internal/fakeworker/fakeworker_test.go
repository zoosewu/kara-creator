package fakeworker

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	kara "github.com/zoosewu/kara-creator"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// stubNAS 是最小的 NAS 端：一個任務佇列，記下 worker 回報的東西。
type stubNAS struct {
	t      *testing.T
	mu     sync.Mutex
	queue  map[string][]wp.Task // channel → 任務
	blobs  map[string][]byte
	files  map[string]map[string][]byte // 任務 → 檔名 → 內容
	done   map[string]wp.Complete
	failed map[string]wp.Fail
	cancel map[string]bool
	byes   int
	events chan string // 任務 id（完成或失敗時）
}

func newStub(t *testing.T) (*stubNAS, *httptest.Server) {
	s := &stubNAS{t: t, queue: map[string][]wp.Task{}, blobs: map[string][]byte{}, files: map[string]map[string][]byte{},
		done: map[string]wp.Complete{}, failed: map[string]wp.Fail{}, cancel: map[string]bool{}, events: make(chan string, 16)}
	mux := http.NewServeMux()
	p := wp.Prefix
	mux.HandleFunc("POST "+p+"/hello", func(w http.ResponseWriter, r *http.Request) {
		var h wp.Hello
		decode(t, r, &h)
		if d := kara.Current.Diff(h.Versions); d != nil {
			w.WriteHeader(http.StatusConflict)
			_ = json.NewEncoder(w).Encode(wp.VersionMismatch{Detail: "版本不同", Diff: d, NAS: kara.Current})
			return
		}
		_ = json.NewEncoder(w).Encode(wp.HelloResponse{Versions: kara.Current, HeartbeatSeconds: 1, LeaseSeconds: 5})
	})
	mux.HandleFunc("POST "+p+"/lease", func(w http.ResponseWriter, r *http.Request) {
		var req wp.LeaseRequest
		decode(t, r, &req)
		deadline := time.After(200 * time.Millisecond)
		for {
			s.mu.Lock()
			if q := s.queue[req.Channel]; len(q) > 0 {
				s.queue[req.Channel] = q[1:]
				s.mu.Unlock()
				_ = json.NewEncoder(w).Encode(q[0])
				return
			}
			s.mu.Unlock()
			select {
			case <-deadline:
				w.WriteHeader(http.StatusNoContent)
				return
			case <-r.Context().Done():
				return
			case <-time.After(10 * time.Millisecond):
			}
		}
	})
	mux.HandleFunc("GET "+p+"/blobs/{sha}", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		data, ok := s.blobs[r.PathValue("sha")]
		s.mu.Unlock()
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	})
	mux.HandleFunc("PUT "+p+"/tasks/{id}/files/{name}", func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		if s.files[r.PathValue("id")] == nil {
			s.files[r.PathValue("id")] = map[string][]byte{}
		}
		s.files[r.PathValue("id")][r.PathValue("name")] = data
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(wp.Uploaded{SHA256: sha(data), Size: int64(len(data))})
	})
	mux.HandleFunc("POST "+p+"/tasks/{id}/progress", func(w http.ResponseWriter, r *http.Request) {
		var req wp.Progress
		decode(t, r, &req)
		s.mu.Lock()
		cancel := s.cancel[r.PathValue("id")]
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(wp.ProgressResponse{Cancel: cancel})
	})
	mux.HandleFunc("POST "+p+"/tasks/{id}/complete", func(w http.ResponseWriter, r *http.Request) {
		var req wp.Complete
		decode(t, r, &req)
		s.mu.Lock()
		s.done[r.PathValue("id")] = req
		s.mu.Unlock()
		s.events <- r.PathValue("id")
	})
	mux.HandleFunc("POST "+p+"/tasks/{id}/fail", func(w http.ResponseWriter, r *http.Request) {
		var req wp.Fail
		decode(t, r, &req)
		s.mu.Lock()
		s.failed[r.PathValue("id")] = req
		s.mu.Unlock()
		s.events <- r.PathValue("id")
	})
	mux.HandleFunc("POST "+p+"/bye", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.byes++
		s.mu.Unlock()
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return s, srv
}

func (s *stubNAS) add(task wp.Task, inputs map[string][]byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	task.Channel = wp.ChannelOf(task.Kind)
	task.Inputs = map[string]wp.Blob{}
	for name, data := range inputs {
		s.blobs[sha(data)] = data
		task.Inputs[name] = wp.Blob{SHA256: sha(data), Size: int64(len(data))}
	}
	s.queue[task.Channel] = append(s.queue[task.Channel], task)
}

func (s *stubNAS) wait(t *testing.T, n int) {
	t.Helper()
	for range n {
		select {
		case <-s.events:
		case <-time.After(5 * time.Second):
			t.Fatal("等不到 worker 回報")
		}
	}
}

// waitTask 等某一件任務回報（完成或失敗），其他任務的回報略過。
func (s *stubNAS) waitTask(t *testing.T, id string) {
	t.Helper()
	timeout := time.After(5 * time.Second)
	for {
		select {
		case got := <-s.events:
			if got == id {
				return
			}
		case <-timeout:
			t.Fatalf("等不到 %s 回報", id)
		}
	}
}

func decode(t *testing.T, r *http.Request, v any) {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		t.Errorf("%s：%v", r.URL.Path, err)
	}
}

func sha(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func params(v any) json.RawMessage {
	raw, _ := json.Marshal(v)
	return raw
}

// wav 產生 seconds 秒的 16kHz 單聲道靜音。
func wav(seconds int) []byte {
	n := uint32(16000 * 2 * seconds)
	var b bytes.Buffer
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, 36+n)
	b.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(16000), uint32(32000), uint16(2), uint16(16)} {
		_ = binary.Write(&b, binary.LittleEndian, v)
	}
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, n)
	b.Write(make([]byte, n))
	return b.Bytes()
}

func start(t *testing.T, opt Options) (stop func() error) {
	ctx, cancel := context.WithCancel(context.Background())
	opt.Logf = t.Logf
	errc := make(chan error, 1)
	go func() { errc <- Run(ctx, opt) }()
	return func() error { cancel(); return <-errc }
}

func TestAllKinds(t *testing.T) {
	s, srv := newStub(t)
	audio := wav(10)
	media := []byte("假的影片")
	manualASS := []byte("[Script Info]\n; 手動改過\n")
	s.add(wp.Task{ID: "sep", Kind: wp.KindSeparate, Params: params(wp.SeparateParams{Model: "htdemucs", Stems: 2}),
		Outputs: []string{wp.FileVocals, wp.FileNoVocals}}, map[string][]byte{wp.InputAudio: audio})
	s.add(wp.Task{ID: "align", Kind: wp.KindAlign, Params: params(wp.AlignParams{Texts: []string{"自己編的歌詞", "made up line"}})},
		map[string][]byte{wp.InputAudio: audio})
	s.add(wp.Task{ID: "from", Kind: wp.KindAlignFrom, Params: params(wp.AlignFromParams{
		AlignParams: wp.AlignParams{Texts: []string{"一", "二", "三"}}, First: 1, Anchor: 4})}, map[string][]byte{wp.InputAudio: audio})
	s.add(wp.Task{ID: "line", Kind: wp.KindAlignLine, Params: params(wp.AlignLineParams{Text: "一句", T0: 2})},
		map[string][]byte{wp.InputAudio: audio})
	s.add(wp.Task{ID: "qa", Kind: wp.KindQA, Params: params(wp.QAParams{Lines: []wp.Line{{Text: "一", Start: 1, End: 2}}})},
		map[string][]byte{wp.InputAudio: audio})
	s.add(wp.Task{ID: "reading", Kind: wp.KindReading, Params: params(wp.ReadingParams{Language: "ja", Texts: []string{"空", "海"}})}, nil)
	s.add(wp.Task{ID: "render", Kind: wp.KindRender, Params: params(wp.RenderParams{Target: "instrumental"}),
		Outputs: []string{wp.FileASS, wp.FileVideo}}, map[string][]byte{wp.InputMedia: media})
	s.add(wp.Task{ID: "render-manual", Kind: wp.KindRender, Params: params(wp.RenderParams{Target: "instrumental"}),
		Outputs: []string{wp.FileVideo}}, map[string][]byte{wp.InputMedia: media, wp.InputASS: manualASS})

	stop := start(t, Options{NAS: srv.URL})
	s.wait(t, 8)
	if err := stop(); err != nil {
		t.Fatal(err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.failed) != 0 {
		t.Fatalf("不該有失敗：%+v", s.failed)
	}
	if !bytes.Equal(s.files["sep"][wp.FileVocals], audio) || !bytes.Equal(s.files["sep"][wp.FileNoVocals], audio) {
		t.Error("去人聲應該上傳兩個和輸入相同的檔案")
	}

	var align wp.AlignResult
	_ = json.Unmarshal(s.done["align"].Result, &align)
	if len(align.Lines) != 2 || align.Lines[1].Start != 5 || len(align.Lines[0].Words) != 6 || len(align.Lines[1].Words) != 3 {
		t.Errorf("對時結果不對：%+v", align)
	}
	var from wp.AlignResult
	_ = json.Unmarshal(s.done["from"].Result, &from)
	if len(from.Lines) != 2 || from.Lines[0].Start != 4 {
		t.Errorf("align_from 應該從 anchor 開始、只回傳 first 之後：%+v", from)
	}
	var line wp.AlignLineResult
	_ = json.Unmarshal(s.done["line"].Result, &line)
	if line.Line.Start != 2 || line.Line.End > 5 {
		t.Errorf("align_line：%+v", line)
	}
	var qa wp.QAResult
	_ = json.Unmarshal(s.done["qa"].Result, &qa)
	if len(qa.Doc.Lines) != 1 || qa.Doc.Lines[0].Status != "ok" {
		t.Errorf("qa：%+v", qa)
	}
	var reading wp.ReadingResult
	_ = json.Unmarshal(s.done["reading"].Result, &reading)
	if len(reading.Lines) != 2 {
		t.Errorf("reading：%+v", reading)
	}

	var render wp.RenderResult
	_ = json.Unmarshal(s.done["render"].Result, &render)
	if render.AssSHA256 != sha(s.files["render"][wp.FileASS]) || !bytes.Equal(s.files["render"][wp.FileVideo], media) {
		t.Errorf("render：%+v", render)
	}
	if s.done["render"].Files[wp.FileVideo] != sha(media) {
		t.Error("complete 應該帶上傳檔的 sha256")
	}
	var manual wp.RenderResult
	_ = json.Unmarshal(s.done["render-manual"].Result, &manual)
	if _, uploaded := s.files["render-manual"][wp.FileASS]; uploaded || manual.AssSHA256 != sha(manualASS) {
		t.Error("有手動 ASS 時不該上傳 karaoke.ass，結果的 ass_sha256 是手動那份")
	}
	if s.byes != 1 {
		t.Errorf("結束時應該送一次 bye，實際 %d 次", s.byes)
	}
}

func TestVersionMismatch(t *testing.T) {
	_, srv := newStub(t)
	old := kara.Current
	old.Align--
	err := Run(context.Background(), Options{NAS: srv.URL, Versions: &old, Logf: t.Logf})
	if !errors.Is(err, ErrVersion) {
		t.Fatalf("版本不同應該回傳 ErrVersion，實際：%v", err)
	}
}

func TestCancel(t *testing.T) {
	s, srv := newStub(t)
	s.cancel["slow"] = true
	s.add(wp.Task{ID: "slow", Kind: wp.KindAlign, Params: params(wp.AlignParams{Texts: []string{"一"}})},
		map[string][]byte{wp.InputAudio: wav(1)})
	s.add(wp.Task{ID: "next", Kind: wp.KindAlign, Params: params(wp.AlignParams{Texts: []string{"二"}})},
		map[string][]byte{wp.InputAudio: wav(1)})

	stop := start(t, Options{NAS: srv.URL, Delay: 300 * time.Millisecond})
	s.waitTask(t, "next") // 取消的 slow 有時會先回報失敗：明確等 next
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.done["slow"]; ok {
		t.Error("取消的任務不該回報完成")
	}
	if _, ok := s.done["next"]; !ok {
		t.Error("取消之後應該繼續做下一件")
	}
}

func TestFail(t *testing.T) {
	s, srv := newStub(t)
	s.add(wp.Task{ID: "q", Kind: wp.KindQA, Params: params(wp.QAParams{})}, map[string][]byte{wp.InputAudio: wav(1)})
	stop := start(t, Options{NAS: srv.URL, Fail: map[string]wp.Fail{wp.KindQA: {Error: "顯示卡記憶體不足", Retryable: true}}})
	s.wait(t, 1)
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if f := s.failed["q"]; f.Error != "顯示卡記憶體不足" || !f.Retryable || f.Instance == "" {
		t.Errorf("fail：%+v", f)
	}
}
