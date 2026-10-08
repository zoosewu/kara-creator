// Package pipeline 是一首歌的處理步驟：去人聲、製作伴唱帶（對時 → 字幕與燒錄 → 檢查）、單獨檢查、AI 重對。
// 要不要做由 planner 的指紋判斷，
// AI 的部分交給排程器，NAS 本機只做便宜的 ffmpeg 操作。
//
// 同一首歌同時只會有一件工作在跑（jobs 保證），這裡不另外加鎖。
package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	kara "github.com/zoosewu/kara-creator"
	"github.com/zoosewu/kara-creator/nas/internal/lyrics"
	"github.com/zoosewu/kara-creator/nas/internal/media"
	"github.com/zoosewu/kara-creator/nas/internal/planner"
	"github.com/zoosewu/kara-creator/nas/internal/scheduler"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
	"github.com/zoosewu/kara-creator/nas/internal/timing"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// AI 是交出 AI 任務的地方（排程器；測試時可以換掉）。
type AI interface {
	Submit(ctx context.Context, spec scheduler.Spec) (*scheduler.Result, error)
}

// Deps 是處理步驟需要的東西。
type Deps struct {
	Store    *store.Store
	Media    media.Tools
	AI       AI
	Fonts    func(language string) (wp.Font, bool)
	Versions kara.Versions
	Now      func() time.Time
	// LocalSlots 限制 NAS 本機同時進行的 ffmpeg 動作（預設 2）。
	LocalSlots chan struct{}
}

// Pipeline 執行處理步驟。
type Pipeline struct {
	d Deps

	mu     sync.Mutex
	speech map[string]scheduler.Input // speech.wav 的路徑 → 雜湊（同一份人聲常常重複用）
}

// New 建立 Pipeline。
func New(d Deps) *Pipeline {
	if d.Now == nil {
		d.Now = time.Now
	}
	if d.LocalSlots == nil {
		d.LocalSlots = make(chan struct{}, 2)
	}
	return &Pipeline{d: d, speech: map[string]scheduler.Input{}}
}

// Run 是一次執行的環境：紀錄、進度、AI 任務的優先順序。
type Run struct {
	Log      func(line string)
	Progress func(p float64) // 0–1；負值代表不知道
	Stage    func(name string)
	Priority string
}

func (r Run) log(format string, args ...any) {
	if r.Log != nil {
		r.Log(fmt.Sprintf(format, args...))
	}
}

func (r Run) stage(name string) {
	if r.Stage != nil {
		r.Stage(name)
	}
	if r.Progress != nil {
		r.Progress(-1)
	}
}

func (r Run) spec(spec scheduler.Spec) scheduler.Spec {
	spec.Priority = r.Priority
	if spec.Priority == "" {
		spec.Priority = wp.PriorityBatch
	}
	spec.Log = r.Log
	spec.Progress = r.Progress
	spec.Started = func(worker string) { r.log("  . AI 伺服器「%s」開始處理", worker) }
	return spec
}

// UserError 是要直接顯示給使用者的錯誤（例如缺歌詞、歌詞改過）。
type UserError struct{ Message string }

func (e *UserError) Error() string { return e.Message }

func userError(format string, args ...any) error {
	return &UserError{Message: fmt.Sprintf(format, args...)}
}

func (p *Pipeline) now() string { return p.d.Now().Format(time.RFC3339) }

func (p *Pipeline) evaluate(id string) (planner.Input, planner.Result, error) {
	in, err := planner.Inspect(p.d.Store, id, p.d.Store.Library().Settings, p.d.Fonts, p.d.Versions)
	if err != nil {
		return planner.Input{}, planner.Result{}, err
	}
	return in, planner.Evaluate(in), nil
}

// local 取得一個本機 ffmpeg 的名額。
func (p *Pipeline) local(ctx context.Context) (release func(), err error) {
	select {
	case p.d.LocalSlots <- struct{}{}:
		return func() { <-p.d.LocalSlots }, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// workFile 回傳 cache/work 底下的暫存路徑。
func (p *Pipeline) workFile(name string) string { return p.d.Store.Path(store.WorkDir, name) }

func inputOf(path string) (scheduler.Input, error) {
	sum, size, err := store.HashFile(path)
	if err != nil {
		return scheduler.Input{}, err
	}
	return scheduler.Input{Path: path, SHA256: sum, Size: size}, nil
}

// place 把暫存檔搬到 songs/<id>/<name>（同一顆硬碟上改名；跨檔案系統時複製）。
func (p *Pipeline) place(id, src, name string) (song.FileRef, error) {
	dst := p.d.Store.SongPath(id, name)
	if err := os.Rename(src, dst); err != nil {
		if err := copyFile(src, dst); err != nil {
			return song.FileRef{}, err
		}
		_ = os.Remove(src)
	}
	return store.Refresh(dst, song.FileRef{})
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return store.WriteFile(dst, data)
}

// ---- 去人聲 --------------------------------------------------------------------

// Separate 去人聲（人聲 / 伴奏分離）。已經是最新的就略過（force 時重做）。
func (p *Pipeline) Separate(ctx context.Context, id string, force bool, run Run) error {
	run.stage("去人聲")
	_, r, err := p.evaluate(id)
	if err != nil {
		return err
	}
	if r.Status.Download != planner.Done {
		return userError("找不到來源檔，請重新下載或重新放入")
	}
	if r.Status.Separate == planner.Done && !force {
		run.log("已去過人聲，略過")
		return nil
	}
	sg, _ := p.d.Store.Song(id)
	src := p.d.Store.SongPath(id, sg.Source.File.Name)
	ext := strings.ToLower(filepath.Ext(sg.Source.File.Name))
	keepVideo := media.VideoExts[ext] && sg.Source.Mode != "audio" && sg.Source.Width > 0
	msg := "去人聲（人聲 / 伴奏分離）"
	if force {
		msg += "，強制重做"
	}
	run.log("%s：輸入 %s → 輸出 %s", msg, ext, ext)
	if keepVideo {
		run.log("  . 保留影像軌（不重新編碼）")
	}

	// 1. 抽音軌（44.1kHz 立體聲 wav）
	release, err := p.local(ctx)
	if err != nil {
		return err
	}
	run.log("  . 抽取音軌...")
	wav := p.workFile("separate-" + id + ".wav")
	err = p.d.Media.ExtractWav(ctx, src, wav)
	release()
	defer os.Remove(wav)
	if err != nil {
		return err
	}
	in, err := inputOf(wav)
	if err != nil {
		return err
	}

	// 2. Demucs（AI）
	res, err := p.d.AI.Submit(ctx, run.spec(scheduler.Spec{Kind: wp.KindSeparate, Song: id,
		Inputs:  map[string]scheduler.Input{wp.InputAudio: in},
		Params:  wp.SeparateParams{Model: planner.SeparateModel, Stems: planner.SeparateStems},
		Outputs: []string{wp.FileVocals, wp.FileNoVocals}}))
	if err != nil {
		return err
	}
	defer res.Cleanup()

	// 3. 封裝：伴奏和來源同格式（影像軌直接複製），人聲存成 FLAC
	if release, err = p.local(ctx); err != nil {
		return err
	}
	defer release()
	run.log("  . 封裝伴奏 → %s", song.InstrBase+ext)
	instTmp := p.workFile("instrumental-" + id + ext)
	if err := p.d.Media.Mux(ctx, res.Files[wp.FileNoVocals], src, instTmp, keepVideo); err != nil {
		return err
	}
	run.log("  . 人聲存成 %s", song.FileVocals)
	vocTmp := p.workFile("vocals-" + id + ".flac")
	if err := p.d.Media.EncodeFLAC(ctx, res.Files[wp.FileVocals], vocTmp); err != nil {
		os.Remove(instTmp)
		return err
	}
	old := sg.Stages.Separate
	inst, err := p.place(id, instTmp, song.InstrBase+ext)
	if err != nil {
		return err
	}
	voc, err := p.place(id, vocTmp, song.FileVocals)
	if err != nil {
		return err
	}
	if old != nil && old.Instrumental.Name != "" && old.Instrumental.Name != inst.Name {
		_ = os.Remove(p.d.Store.SongPath(id, old.Instrumental.Name)) // 來源換了副檔名：舊的伴奏刪掉
	}
	err = p.d.Store.EditSong(id, func(s *song.Song) error {
		s.Stages.Separate = &song.SeparateStage{Stage: song.Stage{Key: r.SeparateFP, Worker: res.Worker, DoneAt: p.now()},
			Instrumental: inst, Vocals: voc}
		return nil
	})
	if err == nil {
		run.log("去人聲完成")
	}
	return err
}

// speechWav 把人聲轉成 16kHz 單聲道 wav（對時與檢查的輸入），同一份人聲只轉一次。
func (p *Pipeline) speechWav(ctx context.Context, id string, vocals song.FileRef) (scheduler.Input, error) {
	path := p.workFile("speech-" + vocals.SHA256[:16] + ".wav")
	p.mu.Lock()
	in, ok := p.speech[path]
	p.mu.Unlock()
	if ok {
		if _, err := os.Stat(path); err == nil {
			return in, nil
		}
	}
	release, err := p.local(ctx)
	if err != nil {
		return scheduler.Input{}, err
	}
	defer release()
	tmp := path + ".tmp.wav"
	if err := p.d.Media.SpeechWav(ctx, p.d.Store.SongPath(id, vocals.Name), tmp); err != nil {
		return scheduler.Input{}, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return scheduler.Input{}, err
	}
	in, err = inputOf(path)
	if err != nil {
		return scheduler.Input{}, err
	}
	p.mu.Lock()
	p.speech[path] = in
	p.mu.Unlock()
	return in, nil
}

// ---- 製作伴唱帶 ----------------------------------------------------------------

// KaraokeOptions 是製作伴唱帶的選項。
type KaraokeOptions struct {
	Force   bool // 全部重做（含覆蓋手改的字幕）
	Realign bool // 重新對時
}

// Karaoke 製作伴唱帶：（需要時先去人聲）→ 對時 → 字幕與燒錄 → 對時檢查。
// 沒有歌詞時只記錄、不算失敗。
func (p *Pipeline) Karaoke(ctx context.Context, id string, opt KaraokeOptions, run Run) error {
	run.stage("製作伴唱帶")
	in, r, err := p.evaluate(id)
	if err != nil {
		return err
	}
	if in.Lyrics == nil {
		run.log("缺歌詞，暫不製作伴唱帶。請先輸入歌詞")
		return nil
	}
	if len(in.Lyrics.LyricLines()) == 0 {
		return userError("歌詞檔沒有內容")
	}
	run.log("製作伴唱帶")
	if r.Status.Separate != planner.Done {
		run.log("  . 尚未有人聲 / 伴奏分離結果，先進行分離")
		if err := p.Separate(ctx, id, false, run); err != nil {
			return err
		}
		run.stage("製作伴唱帶")
		if in, r, err = p.evaluate(id); err != nil {
			return err
		}
	}
	changed := false

	// 1. 對時
	switch {
	case r.RestoredUsable && !opt.Realign && !opt.Force:
		al := in.Alignment.Clone()
		al.Key, al.Lyrics, al.LineLyrics, al.Restored = r.AlignFP, r.LyricsFP, r.LineLyrics, nil
		al.Language, al.Method, al.Model = r.Language, p.d.Versions.Align, planner.WhisperModel
		if al.Run == "" {
			al.Run = newRun() // 從資料備份還原的對時：沿用備份裡的編號（確認狀態才能延續）
		}
		if err := p.writeAlignment(id, al); err != nil {
			return err
		}
		if err := p.recordAlign(id, r.AlignFP, "（資料備份）"); err != nil {
			return err
		}
		run.log("  . 沿用資料備份裡的對時（不重新對時）")
		changed = true
	case r.AlignPatch != nil && !opt.Realign && !opt.Force:
		err := p.patchAlign(ctx, id, in, r, run)
		if errors.Is(err, timing.ErrNoRoom) {
			run.log("  . 新的句子%s，改成整首重新對時", err)
			err = p.align(ctx, id, in, r, run)
		}
		if err != nil {
			return err
		}
		changed = true
	case !r.AlignOK || opt.Realign || opt.Force:
		if err := p.align(ctx, id, in, r, run); err != nil {
			return err
		}
		changed = true
	default:
		run.log("  . 對時結果沒有變化，沿用 alignment.json")
	}
	if changed {
		if in, r, err = p.evaluate(id); err != nil {
			return err
		}
	}

	// 2. 字幕與燒錄
	sg := in.Song
	for _, target := range sg.Info.Targets {
		rec, files := sg.Stages.Render[target], in.Render[target]
		if !opt.Force && rec != nil && rec.Key == r.RenderFP[target] && files.VideoOK {
			run.log("  . %s 已是最新", song.VideoFile(target))
			continue
		}
		if err := p.render(ctx, id, target, in, r, opt.Force, run); err != nil {
			return err
		}
	}
	if err := p.dropOldTargets(id, sg.Info.Targets); err != nil {
		return err
	}

	// 3. 對時檢查（失敗不影響成品）
	if in, r, err = p.evaluate(id); err != nil {
		return err
	}
	if err := p.qa(ctx, id, in, r, opt.Force, run); err != nil {
		if errors.Is(err, scheduler.ErrCancelled) || ctx.Err() != nil {
			return err
		}
		run.log("  [!] 對時檢查失敗（不影響伴唱帶）：%v", err)
	}
	run.log("伴唱帶完成")
	return nil
}

func lyricsParams(doc *lyrics.Document) ([]string, [][]lyrics.Ruby) {
	lines := doc.LyricLines()
	texts := make([]string, len(lines))
	rubies := make([][]lyrics.Ruby, len(lines))
	for i, ln := range lines {
		texts[i], rubies[i] = ln.Text, ln.Rubies
	}
	return texts, rubies
}

func (p *Pipeline) align(ctx context.Context, id string, in planner.Input, r planner.Result, run Run) error {
	if in.Alignment != nil && len(in.Alignment.Adjustments) > 0 {
		run.log("  . 注意：重新對時會取代先前手動調整的 %d 處時間", len(in.Alignment.Adjustments))
	}
	if r.Language == "nan" || r.Language == "yue" {
		run.log("  . 逐字對時中（CTC，%s）...", song.Languages[r.Language])
	} else {
		lang := r.Language
		if lang == "" {
			lang = "自動"
		}
		run.log("  . 逐字對時中（whisper %s, 語言 %s）...", planner.WhisperModel, lang)
	}
	speech, err := p.speechWav(ctx, id, in.Song.Stages.Separate.Vocals)
	if err != nil {
		return err
	}
	texts, rubies := lyricsParams(in.Lyrics)
	res, err := p.d.AI.Submit(ctx, run.spec(scheduler.Spec{Kind: wp.KindAlign, Song: id,
		Inputs: map[string]scheduler.Input{wp.InputAudio: speech},
		Params: wp.AlignParams{Texts: texts, Rubies: toProto(rubies), Language: r.Language, Model: planner.WhisperModel}}))
	if err != nil {
		return err
	}
	defer res.Cleanup()
	var out wp.AlignResult
	if err := json.Unmarshal(res.Raw, &out); err != nil {
		return fmt.Errorf("AI 回傳的對時結果讀不懂：%w", err)
	}
	if err := p.writeAlignment(id, &timing.Alignment{Key: r.AlignFP, Lyrics: r.LyricsFP, LineLyrics: r.LineLyrics,
		Language: r.Language, Method: p.d.Versions.Align, Model: planner.WhisperModel, Run: newRun(), Lines: out.Lines}); err != nil {
		return err
	}
	return p.recordAlign(id, r.AlignFP, res.Worker)
}

// patchAlign 是改了歌詞之後的局部重對（timing.Patch）：只重對改到的句子，其他句子的時間（含手動調整）不動。
// 一句用 align_line；連續好幾句時把那一段人聲剪出來用 align，時間再加回去。前後句之間沒有空檔時回傳 timing.ErrNoRoom。
func (p *Pipeline) patchAlign(ctx context.Context, id string, in planner.Input, r planner.Result, run Run) error {
	patch := r.AlignPatch
	type window struct {
		t0 float64
		t1 *float64
	}
	windows := make([]window, len(patch.Gaps))
	for i, g := range patch.Gaps { // 先確定每一段都放得下，再交給 AI
		t0, t1, err := patch.Window(g)
		if err != nil {
			return err
		}
		windows[i] = window{t0, t1}
	}
	removed := ""
	if patch.Removed > 0 {
		removed = fmt.Sprintf("、拿掉 %d 句", patch.Removed)
	}
	run.log("  . 歌詞改了：只重對改到的 %d 句%s，其他句子的時間（含手動調整）不動", patch.Realign(), removed)
	texts, rubies := lyricsParams(in.Lyrics)
	proto := toProto(rubies)
	worker := "（只拿掉句子）"
	var speech scheduler.Input
	if len(patch.Gaps) > 0 {
		var err error
		if speech, err = p.speechWav(ctx, id, in.Song.Stages.Separate.Vocals); err != nil {
			return err
		}
	}
	for i, g := range patch.Gaps {
		w := windows[i]
		end := "結尾"
		if w.t1 != nil {
			end = fmt.Sprintf("%.2fs", *w.t1)
		}
		pinned := ""
		if len(g.Pins) > 0 {
			pinned = "，手動調過的句子保留開頭"
		}
		which := fmt.Sprintf("第 %d 句", g.From+1)
		if g.To-g.From > 1 {
			which = fmt.Sprintf("第 %d–%d 句", g.From+1, g.To)
		}
		run.log("  . 重對%s（%.2fs–%s%s）", which, w.t0, end, pinned)
		var lines []wp.Line
		if g.To-g.From == 1 {
			res, err := p.d.AI.Submit(ctx, run.spec(scheduler.Spec{Kind: wp.KindAlignLine, Song: id,
				Inputs: map[string]scheduler.Input{wp.InputAudio: speech},
				Params: wp.AlignLineParams{Text: texts[g.From], Rubies: proto[g.From], T0: w.t0, T1: w.t1, Language: r.Language}}))
			if err != nil {
				return err
			}
			var out wp.AlignLineResult
			err = json.Unmarshal(res.Raw, &out)
			res.Cleanup()
			if err != nil {
				return fmt.Errorf("AI 回傳的對時結果讀不懂：%w", err)
			}
			lines, worker = []wp.Line{out.Line}, res.Worker
		} else {
			clip, err := p.clipWav(ctx, speech, w.t0, w.t1)
			if err != nil {
				return err
			}
			res, err := p.d.AI.Submit(ctx, run.spec(scheduler.Spec{Kind: wp.KindAlign, Song: id,
				Inputs: map[string]scheduler.Input{wp.InputAudio: clip},
				Params: wp.AlignParams{Texts: texts[g.From:g.To], Rubies: proto[g.From:g.To], Language: r.Language,
					Model: planner.WhisperModel}}))
			_ = os.Remove(clip.Path)
			if err != nil {
				return err
			}
			var out wp.AlignResult
			err = json.Unmarshal(res.Raw, &out)
			res.Cleanup()
			if err != nil {
				return fmt.Errorf("AI 回傳的對時結果讀不懂：%w", err)
			}
			timing.Offset(out.Lines, w.t0)
			lines, worker = out.Lines, res.Worker
		}
		if err := patch.Fill(g, lines); err != nil {
			return err
		}
	}
	al := in.Alignment.Clone()
	if err := al.Apply(patch, r.AlignFP, r.LyricsFP, r.LineLyrics, p.now()); err != nil {
		return err
	}
	if err := p.writeAlignment(id, al); err != nil {
		return err
	}
	return p.recordAlign(id, r.AlignFP, worker)
}

// clipWav 剪出人聲的一段，交給 AI（用完就刪）。
func (p *Pipeline) clipWav(ctx context.Context, speech scheduler.Input, t0 float64, t1 *float64) (scheduler.Input, error) {
	release, err := p.local(ctx)
	if err != nil {
		return scheduler.Input{}, err
	}
	defer release()
	path := p.workFile(fmt.Sprintf("clip-%s-%d.wav", speech.SHA256[:16], time.Now().UnixNano()))
	if err := p.d.Media.Clip(ctx, speech.Path, path, t0, t1); err != nil {
		_ = os.Remove(path)
		return scheduler.Input{}, err
	}
	return inputOf(path)
}

// newRun 產生整首對時的編號。
func newRun() string {
	b := make([]byte, 6)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func toProto(rubies [][]lyrics.Ruby) [][]wp.Ruby {
	out := make([][]wp.Ruby, len(rubies))
	for i, rs := range rubies {
		out[i] = make([]wp.Ruby, len(rs))
		for k, r := range rs {
			out[i][k] = wp.Ruby{Start: r.Start, End: r.End, Reading: r.Reading}
		}
	}
	return out
}

func (p *Pipeline) writeAlignment(id string, al *timing.Alignment) error {
	data, err := json.MarshalIndent(al, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFile(p.d.Store.SongPath(id, song.FileAlignment), append(data, '\n'))
}

func (p *Pipeline) recordAlign(id, key, worker string) error {
	return p.d.Store.EditSong(id, func(s *song.Song) error {
		s.Stages.Align = &song.Stage{Key: key, Worker: worker, DoneAt: p.now()}
		return nil
	})
}

func (p *Pipeline) render(ctx context.Context, id, target string, in planner.Input, r planner.Result, force bool, run Run) error {
	sg := in.Song
	if !r.FontOK {
		return userError("找不到「%s」要用的字型檔，請到設定確認字型", languageName(r.Language))
	}
	manual := r.ManualASS[target] && !force
	inputs := map[string]scheduler.Input{}
	mediaPath := p.d.Store.SongPath(id, sg.Source.File.Name)
	if target == song.TargetInstrumental {
		mediaPath = p.d.Store.SongPath(id, sg.Stages.Separate.Instrumental.Name)
	}
	mediaIn, err := inputOf(mediaPath)
	if err != nil {
		return err
	}
	inputs[wp.InputMedia] = mediaIn
	outputs := []string{wp.FileVideo}
	if manual {
		run.log("  . 偵測到 %s 被手動修改，保留修改內容", song.FileASS)
		assIn, err := inputOf(p.d.Store.SongPath(id, song.FileASS))
		if err != nil {
			return err
		}
		inputs[wp.InputASS] = assIn
	} else {
		outputs = append(outputs, wp.FileASS)
	}
	texts, rubies := lyricsParams(in.Lyrics)
	lines := make([]wp.Line, len(in.Alignment.Lines))
	for i, ln := range in.Alignment.Lines {
		ln.Text = timing.LineText(ln)
		lines[i] = ln
	}
	singers := r.Singers
	if singers == nil {
		singers = []*string{}
	}
	translations := r.Translations
	if translations == nil {
		translations = []string{}
	}
	run.log("  . 產生字幕並燒錄 → %s", song.VideoFile(target))
	res, err := p.d.AI.Submit(ctx, run.spec(scheduler.Spec{Kind: wp.KindRender, Song: id, Inputs: inputs, Outputs: outputs,
		Params: wp.RenderParams{Target: target, Lines: lines, Texts: texts, Rubies: toProto(rubies), Language: r.Language,
			Singers: singers, Translations: translations, TitleCard: r.TitleCard, Scale: in.Settings.SubtitleScale,
			Font: r.Font, Encode: wp.Encode{Prefer: []string{"h264_nvenc", "libx264"}}}}))
	if err != nil {
		return err
	}
	defer res.Cleanup()
	var out wp.RenderResult
	if err := json.Unmarshal(res.Raw, &out); err != nil {
		return fmt.Errorf("AI 回傳的燒錄結果讀不懂：%w", err)
	}
	video, err := p.place(id, res.Files[wp.FileVideo], song.VideoFile(target))
	if err != nil {
		return err
	}
	var ass song.FileRef
	if manual {
		ass, err = store.Refresh(p.d.Store.SongPath(id, song.FileASS), song.FileRef{})
	} else {
		ass, err = p.place(id, res.Files[wp.FileASS], song.FileASS)
	}
	if err != nil {
		return err
	}
	key := r.RenderPlain[target]
	if manual {
		key = r.RenderFP[target]
	}
	return p.d.Store.EditSong(id, func(s *song.Song) error {
		if s.Stages.Render == nil {
			s.Stages.Render = map[string]*song.RenderStage{}
		}
		s.Stages.Render[target] = &song.RenderStage{Stage: song.Stage{Key: key, Worker: res.Worker, DoneAt: p.now()},
			Content: r.RenderPlain[target], ASS: song.ASSRef{FileRef: ass, Manual: manual}, Video: video}
		// 同一份 ASS 給所有成品用：其他成品的紀錄也更新成這份（內容沒變時它們本來就相同）
		for t, rec := range s.Stages.Render {
			if t != target && rec.Content == r.RenderPlain[t] && !manual {
				rec.ASS = song.ASSRef{FileRef: ass}
			}
		}
		return nil
	})
}

// dropOldTargets 刪掉這次沒要求的舊成品，避免留下過期檔案。
func (p *Pipeline) dropOldTargets(id string, targets []string) error {
	sg, _ := p.d.Store.Song(id)
	var drop []string
	for t, rec := range sg.Stages.Render {
		if !contains(targets, t) {
			_ = os.Remove(p.d.Store.SongPath(id, rec.Video.Name))
			drop = append(drop, t)
		}
	}
	if len(drop) == 0 {
		return nil
	}
	return p.d.Store.EditSong(id, func(s *song.Song) error {
		for _, t := range drop {
			delete(s.Stages.Render, t)
		}
		return nil
	})
}

func contains(list []string, v string) bool {
	for _, x := range list {
		if x == v {
			return true
		}
	}
	return false
}

func languageName(lang string) string {
	if name, ok := song.Languages[lang]; ok {
		return name
	}
	if lang == "" {
		return "未判斷的語言"
	}
	return lang
}

// ---- 對時檢查 ------------------------------------------------------------------

// qa 對時檢查；已經檢查過、對時沒變就略過（force 時重做）。
func (p *Pipeline) qa(ctx context.Context, id string, in planner.Input, r planner.Result, force bool, run Run) error {
	if in.Alignment == nil {
		return userError("還沒有對時結果，請先製作伴唱帶")
	}
	if !force && r.Status.QA != nil {
		run.log("  . 對時沒有變動、已檢查過，略過")
		return nil
	}
	if r.Language == "nan" || r.Language == "yue" {
		run.log("  . 對時檢查：台語 / 粵語只做規則檢查（不做聽寫比對）")
	} else {
		run.log("  . 對時檢查：逐段獨立聽寫人聲（不看歌詞）...")
	}
	speech, err := p.speechWav(ctx, id, in.Song.Stages.Separate.Vocals)
	if err != nil {
		return err
	}
	texts, rubies := lyricsParams(in.Lyrics)
	res, err := p.d.AI.Submit(ctx, run.spec(scheduler.Spec{Kind: wp.KindQA, Song: id,
		Inputs: map[string]scheduler.Input{wp.InputAudio: speech},
		Params: wp.QAParams{Lines: in.Alignment.Lines, Texts: texts, Rubies: toProto(rubies), Language: r.Language, Model: planner.WhisperModel}}))
	if err != nil {
		return err
	}
	defer res.Cleanup()
	var out wp.QAResult
	if err := json.Unmarshal(res.Raw, &out); err != nil {
		return fmt.Errorf("AI 回傳的檢查結果讀不懂：%w", err)
	}
	doc := planner.QADoc{Key: r.QAFP, Lines: out.Doc.Lines, CheckedAt: p.now()}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return err
	}
	if err := store.WriteFile(p.d.Store.SongPath(id, song.FileQA), append(data, '\n')); err != nil {
		return err
	}
	if err := p.d.Store.EditSong(id, func(s *song.Song) error {
		s.Stages.QA = &song.Stage{Key: r.QAFP, Worker: res.Worker, DoneAt: p.now()}
		return nil
	}); err != nil {
		return err
	}
	if c := doc.Counts(); c.Wrong > 0 || c.Suspect > 0 {
		run.log("  . 對時檢查：%d 句可能不準、%d 句待確認（在歌詞編輯器查看）", c.Wrong, c.Suspect)
	} else {
		run.log("  . 對時檢查：沒有發現問題")
	}
	return nil
}

// Check 單獨檢查一首已經對時過的歌（失敗就讓工作失敗）。force=false 時，對時沒變且已檢查過就略過。
func (p *Pipeline) Check(ctx context.Context, id string, force bool, run Run) error {
	run.stage("檢查對時")
	run.log("對時檢查（獨立聽寫比對）")
	in, r, err := p.evaluate(id)
	if err != nil {
		return err
	}
	if in.Alignment == nil || in.Lyrics == nil || in.Song.Stages.Separate == nil {
		return userError("還沒有對時結果，請先製作伴唱帶")
	}
	return p.qa(ctx, id, in, r, force, run)
}

// ---- AI 重對 -------------------------------------------------------------------

// Retime 以第 line 句目前的開頭為準，讓 AI 重新對時（mode：from = 這句及之後全部、line = 只重對這句）。
// 和手動調整一樣只改 alignment.json，之後成品變成需更新（只重燒，不整首重新對時）。
func (p *Pipeline) Retime(ctx context.Context, id string, line int, mode string, run Run) error {
	run.stage("AI 重新對時")
	desc, ok := timing.RetimeModes[mode]
	if !ok {
		return userError("不支援的方式：%s", mode)
	}
	in, r, err := p.evaluate(id)
	if err != nil {
		return err
	}
	if in.Alignment == nil || in.Lyrics == nil || in.Song.Stages.Separate == nil {
		return userError("還沒有對時結果，請先製作伴唱帶")
	}
	al := in.Alignment
	texts, rubies := lyricsParams(in.Lyrics)
	// 對時之後歌詞（文字與讀音）有沒有改過：比對對時當下記下的歌詞指紋
	if len(al.Lines) != len(texts) || al.Lyrics != r.LyricsFP {
		return userError("歌詞改過，和目前的對時結果對不上：請先按「更新伴唱帶」（會整首重新對時）再調整")
	}
	if line < 0 || line >= len(al.Lines) {
		return userError("沒有這一句")
	}
	anchor := al.Lines[line].Start
	run.log("  . %s：第 %d 句，從 %.2fs 開始", desc, line+1, anchor)
	speech, err := p.speechWav(ctx, id, in.Song.Stages.Separate.Vocals)
	if err != nil {
		return err
	}
	inputs := map[string]scheduler.Input{wp.InputAudio: speech}
	proto := toProto(rubies)
	next := al.Clone()
	next.LineLyrics = r.LineLyrics
	if mode == timing.RetimeFrom {
		res, err := p.d.AI.Submit(ctx, run.spec(scheduler.Spec{Kind: wp.KindAlignFrom, Song: id, Inputs: inputs,
			Params: wp.AlignFromParams{AlignParams: wp.AlignParams{Texts: texts, Rubies: proto, Language: r.Language,
				Model: planner.WhisperModel}, First: line, Anchor: anchor}}))
		if err != nil {
			return err
		}
		defer res.Cleanup()
		var out wp.AlignResult
		if err := json.Unmarshal(res.Raw, &out); err != nil {
			return fmt.Errorf("AI 回傳的對時結果讀不懂：%w", err)
		}
		if err := next.ApplyFrom(line, anchor, out.Lines, p.now()); err != nil {
			return err
		}
	} else {
		var t1 *float64
		if line+1 < len(al.Lines) {
			v := al.Lines[line+1].Start
			t1 = &v
		}
		res, err := p.d.AI.Submit(ctx, run.spec(scheduler.Spec{Kind: wp.KindAlignLine, Song: id, Inputs: inputs,
			Params: wp.AlignLineParams{Text: texts[line], Rubies: proto[line], T0: anchor, T1: t1, Language: r.Language}}))
		if err != nil {
			return err
		}
		defer res.Cleanup()
		var out wp.AlignLineResult
		if err := json.Unmarshal(res.Raw, &out); err != nil {
			return fmt.Errorf("AI 回傳的對時結果讀不懂：%w", err)
		}
		if err := next.ApplyLine(line, anchor, out.Line, p.now()); err != nil {
			return err
		}
	}
	if err := p.writeAlignment(id, next); err != nil {
		return err
	}
	run.log("  . 完成，重新製作伴唱帶後生效")
	return nil
}
