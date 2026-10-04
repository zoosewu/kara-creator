package scheduler

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"time"

	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// Handler 回傳 /worker/v1 的 handler（路徑含前綴）。
func (s *Scheduler) Handler() http.Handler {
	mux := http.NewServeMux()
	p := wp.Prefix
	mux.HandleFunc("POST "+p+"/hello", s.hello)
	mux.HandleFunc("POST "+p+"/lease", s.lease)
	mux.HandleFunc("POST "+p+"/tasks/{id}/progress", s.progress)
	mux.HandleFunc("PUT "+p+"/tasks/{id}/files/{name}", s.upload)
	mux.HandleFunc("POST "+p+"/tasks/{id}/complete", s.complete)
	mux.HandleFunc("POST "+p+"/tasks/{id}/fail", s.fail)
	mux.HandleFunc("GET "+p+"/blobs/{sha}", s.blob)
	mux.HandleFunc("GET "+p+"/fonts/{sha}", s.font)
	mux.HandleFunc("POST "+p+"/bye", s.bye)
	return s.auth(mux)
}

func (s *Scheduler) auth(next http.Handler) http.Handler {
	if s.opt.Token == "" {
		return next
	}
	want := []byte(wp.BearerPrefix + s.opt.Token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), want) != 1 {
			detail(w, http.StatusUnauthorized, "token 不對：請確認 AI 伺服器的 --token 和 NAS 的 --worker-token 相同")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func detail(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"detail": msg})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<20)).Decode(v); err != nil {
		detail(w, http.StatusBadRequest, "內容讀不懂："+err.Error())
		return false
	}
	return true
}

// workerOf 找出這個 instance 的 worker（持有鎖時呼叫）。
func (s *Scheduler) workerOf(instance string) *Worker {
	for _, w := range s.workers {
		if w.Instance == instance {
			return w
		}
	}
	return nil
}

func (s *Scheduler) hello(w http.ResponseWriter, r *http.Request) {
	var req wp.Hello
	if !decode(w, r, &req) {
		return
	}
	if req.Name == "" || req.Instance == "" {
		detail(w, http.StatusBadRequest, "缺少 name 或 instance")
		return
	}
	diff := s.opt.Versions.Diff(req.Versions)
	s.mu.Lock()
	old := s.workers[req.Name]
	if old != nil && old.Instance != req.Instance {
		// 同一台重新啟動了：舊的行程手上的任務立刻改派
		for _, t := range s.tasks {
			if t.state == leased && t.instance == old.Instance {
				s.requeue(t, "AI 伺服器「"+req.Name+"」重新啟動")
			}
		}
	}
	wk := &Worker{Name: req.Name, Instance: req.Instance, Hardware: req.Hardware, Versions: req.Versions,
		VersionOK: diff == nil, Diff: diff, Kinds: req.Kinds, Channels: req.Channels,
		Disabled: s.disabled[req.Name], LastSeen: s.opt.Now(), Tasks: []WorkerTask{}, cached: map[string]bool{}}
	for _, sha := range req.Cached {
		wk.cached[sha] = true
	}
	s.workers[req.Name] = wk
	s.event(req.Name)
	s.mu.Unlock()

	if diff != nil {
		writeJSON(w, http.StatusConflict, wp.VersionMismatch{Detail: "AI 伺服器的版本和 NAS 不同，請更新", Diff: diff, NAS: s.opt.Versions})
		return
	}
	writeJSON(w, http.StatusOK, wp.HelloResponse{Versions: s.opt.Versions,
		HeartbeatSeconds: int(s.opt.Heartbeat / time.Second), LeaseSeconds: int(s.opt.Lease / time.Second)})
}

func (s *Scheduler) lease(w http.ResponseWriter, r *http.Request) {
	var req wp.LeaseRequest
	if !decode(w, r, &req) {
		return
	}
	timeout := time.NewTimer(s.opt.LeaseWait)
	defer timeout.Stop()
	for {
		s.mu.Lock()
		wk := s.workerOf(req.Instance)
		if wk == nil {
			s.mu.Unlock()
			detail(w, http.StatusNotFound, "NAS 不認得這個 AI 伺服器，請重新連線（hello）")
			return
		}
		if !wk.VersionOK {
			s.mu.Unlock()
			writeJSON(w, http.StatusConflict, wp.VersionMismatch{Detail: "AI 伺服器的版本和 NAS 不同，請更新", Diff: wk.Diff, NAS: s.opt.Versions})
			return
		}
		wk.LastSeen = s.opt.Now()
		for _, sha := range req.Cached {
			wk.cached[sha] = true
		}
		req.Cached = nil
		if t := s.pick(wk, req.Channel); t != nil {
			t.state, t.worker, t.instance = leased, wk.Name, wk.Instance
			t.deadline = s.opt.Now().Add(s.opt.Lease)
			wk.Tasks = append(wk.Tasks, WorkerTask{ID: t.ID, Kind: t.Kind, Song: t.Song})
			for _, b := range t.Inputs {
				wk.cached[b.SHA256] = true // 它會下載輸入檔
			}
			task, started, name := t.Task, t.spec.Started, wk.Name
			s.event(wk.Name)
			s.mu.Unlock()
			if started != nil {
				started(name)
			}
			writeJSON(w, http.StatusOK, task)
			return
		}
		wake := s.wake
		s.mu.Unlock()
		select {
		case <-wake:
		case <-timeout.C:
			w.WriteHeader(http.StatusNoContent)
			return
		case <-r.Context().Done():
			return
		}
	}
}

// leased 找出這個 instance 領走的任務（持有鎖時呼叫）。不存在回 404、已經不是它的回 409。
func (s *Scheduler) leased(w http.ResponseWriter, id, instance string) *task {
	t := s.tasks[id]
	if t == nil || t.state != leased {
		detail(w, http.StatusNotFound, "沒有這件任務（可能已經取消或 NAS 重新啟動過）")
		return nil
	}
	if instance != "" && t.instance != instance {
		detail(w, http.StatusConflict, "這件任務已經改派給其他 AI 伺服器")
		return nil
	}
	return t
}

func (s *Scheduler) progress(w http.ResponseWriter, r *http.Request) {
	var req wp.Progress
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	t := s.leased(w, r.PathValue("id"), req.Instance)
	if t == nil {
		s.mu.Unlock()
		return
	}
	t.deadline = s.opt.Now().Add(s.opt.Lease)
	if wk := s.workers[t.worker]; wk != nil {
		wk.LastSeen = s.opt.Now()
	}
	cancel := t.cancel
	spec, name := t.spec, t.worker
	if cancel {
		// 告訴 worker 取消之後這件任務就結束了；它之後送來的結果會被拒絕（404）
		s.dropUploads(t)
		delete(s.tasks, t.ID)
		if wk := s.workers[name]; wk != nil {
			wk.Tasks = removeTask(wk.Tasks, t.ID)
			s.event(name)
		}
	}
	s.mu.Unlock()
	if !cancel {
		for _, line := range req.Logs {
			if spec.Log != nil {
				spec.Log("[" + name + "] " + line)
			}
		}
		if req.Progress != nil && spec.Progress != nil {
			spec.Progress(*req.Progress)
		}
	}
	writeJSON(w, http.StatusOK, wp.ProgressResponse{Cancel: cancel})
}

func (s *Scheduler) upload(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	s.mu.Lock()
	t := s.leased(w, r.PathValue("id"), r.URL.Query().Get("instance"))
	if t == nil {
		s.mu.Unlock()
		return
	}
	if !slices.Contains(t.Outputs, name) || name != filepath.Base(name) {
		s.mu.Unlock()
		detail(w, http.StatusBadRequest, "這件任務不能上傳 "+name)
		return
	}
	dir, err := s.taskDir(t)
	s.mu.Unlock()
	if err != nil {
		detail(w, http.StatusInternalServerError, err.Error())
		return
	}
	f, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		detail(w, http.StatusInternalServerError, err.Error())
		return
	}
	h := sha256.New()
	size, err := io.Copy(io.MultiWriter(f, h), r.Body)
	if err == nil {
		// CreateTemp 是 0600；成品會被硬連結到匯出資料夾，要讓 SMB 的其他使用者讀得到
		err = f.Chmod(0o644)
	}
	f.Close()
	if err != nil {
		os.Remove(f.Name())
		detail(w, http.StatusBadRequest, "上傳中斷："+err.Error())
		return
	}
	sum := hex.EncodeToString(h.Sum(nil))
	s.mu.Lock()
	if s.tasks[t.ID] != t || t.dir != dir {
		s.mu.Unlock()
		os.Remove(f.Name())
		detail(w, http.StatusConflict, "這件任務已經改派給其他 AI 伺服器")
		return
	}
	err = os.Rename(f.Name(), filepath.Join(dir, name))
	if err == nil {
		t.uploads[name] = sum
	}
	s.mu.Unlock()
	if err != nil {
		detail(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, wp.Uploaded{SHA256: sum, Size: size})
}

func (s *Scheduler) complete(w http.ResponseWriter, r *http.Request) {
	var req wp.Complete
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.leased(w, r.PathValue("id"), req.Instance)
	if t == nil {
		return
	}
	files := map[string]string{}
	for name, sum := range req.Files {
		if t.uploads[name] != sum {
			detail(w, http.StatusBadRequest, "檔案 "+name+" 沒有上傳完整（sha256 不符），請重新上傳")
			return
		}
		files[name] = filepath.Join(t.dir, name)
	}
	res := &Result{Worker: t.worker, Raw: req.Result, Files: files, dir: t.dir}
	t.dir = "" // 交給呼叫端清
	s.finish(t, res, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Scheduler) fail(w http.ResponseWriter, r *http.Request) {
	var req wp.Fail
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	t := s.leased(w, r.PathValue("id"), req.Instance)
	if t == nil {
		return
	}
	name := t.worker
	if wk := s.workers[name]; wk != nil {
		wk.Tasks = removeTask(wk.Tasks, t.ID)
		s.event(name)
	}
	if req.Retryable {
		s.requeue(t, "AI 伺服器「"+name+"」處理失敗："+req.Error)
	} else {
		s.dropUploads(t)
		s.finish(t, nil, &TaskError{Message: req.Error})
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Scheduler) blob(w http.ResponseWriter, r *http.Request) {
	sha := r.PathValue("sha")
	s.mu.Lock()
	var path string
	for _, t := range s.tasks {
		if t.state != leased {
			continue
		}
		for _, in := range t.spec.Inputs {
			if in.SHA256 == sha {
				path = in.Path
			}
		}
	}
	s.mu.Unlock()
	if path == "" {
		detail(w, http.StatusNotFound, "沒有這個輸入檔（只能取派給你的任務的輸入檔）")
		return
	}
	serveFile(w, r, path)
}

func (s *Scheduler) font(w http.ResponseWriter, r *http.Request) {
	if s.opt.Fonts == nil {
		detail(w, http.StatusNotFound, "沒有字型")
		return
	}
	path, ok := s.opt.Fonts(r.PathValue("sha"))
	if !ok {
		detail(w, http.StatusNotFound, "沒有這個字型")
		return
	}
	serveFile(w, r, path)
}

func serveFile(w http.ResponseWriter, r *http.Request, path string) {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		detail(w, http.StatusNotFound, "檔案不見了")
		return
	}
	if err != nil {
		detail(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		detail(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	http.ServeContent(w, r, "", st.ModTime(), f) // 支援 Range（斷線續傳）
}

func (s *Scheduler) bye(w http.ResponseWriter, r *http.Request) {
	var req wp.Bye
	if !decode(w, r, &req) {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	wk := s.workerOf(req.Instance)
	if wk == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	for _, t := range s.tasks {
		if t.state == leased && t.instance == req.Instance {
			s.requeue(t, "AI 伺服器「"+wk.Name+"」關閉了")
		}
	}
	wk.Tasks = []WorkerTask{}
	wk.LastSeen, wk.gone = s.opt.Now(), true
	s.event(wk.Name)
	w.WriteHeader(http.StatusNoContent)
}
