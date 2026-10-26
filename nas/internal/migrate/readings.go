// Package migrate 是一次性的資料遷移（做過的記在 library.json 的 migrations，不會重做）。
package migrate

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	kara "github.com/zoosewu/kara-creator"
	"github.com/zoosewu/kara-creator/nas/internal/lyrics"
	"github.com/zoosewu/kara-creator/nas/internal/planner"
	"github.com/zoosewu/kara-creator/nas/internal/readings"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/store"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// ReadingsV3 是讀音規則 v3（私 → わたし、明日 → あした、何時 → いつ、何 依後面的字唸なに / なん）的遷移。
const ReadingsV3 = "readings-v3"

// FromReading 是改版前的讀音版本：舊歌的這幾個字照這版的讀音釘住。
const FromReading = 2

// Env 是遷移需要的東西。
type Env struct {
	Store    *store.Store
	Font     func(language string) (wp.Font, bool)
	Versions kara.Versions              // 目前（改版後）的版本
	Old      map[string][]readings.Span // 改版前的讀音快取（readings.LoadSpans）
	Backup   string                     // 改過的歌詞先備份到這裡
	Log      func(string)
}

// PinReadings 把舊歌（日文）裡還是自動讀音的 私 / 何 / 明日 / 何時 改成手動、讀音照舊，舊歌的讀音、對時、成品都不變；
// 原本是最新的紀錄（對時、確認、檢查、成品）更新指紋，遷移之後還是最新（不用重新對時、重燒、重新確認）。
// 讀音版本也在成品的指紋裡，所以沒有要釘的歌（含非日文）一樣要更新成品的紀錄。
func PinReadings(e Env) {
	pinned := 0
	for _, id := range e.Store.SongIDs() {
		n, err := pinSong(e, id)
		if err != nil {
			e.Log(fmt.Sprintf("  [!] %s：%v（請手動確認這首歌的讀音與狀態）", id, err))
			continue
		}
		pinned += n
	}
	e.Log(fmt.Sprintf("讀音規則更新完成：%d 首歌、釘住 %d 處舊讀音", len(e.Store.SongIDs()), pinned))
}

func pinSong(e Env, id string) (int, error) {
	st := e.Store
	old := e.Versions
	old.Reading = FromReading
	inspect := func(v kara.Versions) (planner.Input, planner.Result, error) {
		in, err := planner.Inspect(st, id, st.Library().Settings, e.Font, v)
		if err != nil {
			return in, planner.Result{}, err
		}
		return in, planner.Evaluate(in), nil
	}
	in, before, err := inspect(old)
	if err != nil {
		return 0, err
	}
	sg := in.Song

	// 1. 釘住舊讀音
	count := 0
	if in.Lyrics != nil && before.Language == "ja" {
		doc := *in.Lyrics
		doc.Lines = slices.Clone(doc.Lines)
		var notes []string
		k := 0
		for i := range doc.Lines {
			ln := &doc.Lines[i]
			if ln.Kind != lyrics.KindLyric {
				continue
			}
			k++
			spans, cached := e.Old[readings.Key(before.Language, ln.Text, FromReading)]
			add := pins(ln.Text, ln.Rubies, spans, cached)
			if len(add) == 0 {
				continue
			}
			rs := []rune(ln.Text)
			var words []string
			for _, r := range add {
				words = append(words, fmt.Sprintf("%s（%s）", string(rs[r.Start:r.End]), r.Reading))
			}
			notes = append(notes, fmt.Sprintf("第 %d 句 %s", k, strings.Join(words, "、")))
			ln.Rubies = append(slices.Clone(ln.Rubies), add...)
			slices.SortFunc(ln.Rubies, func(a, b lyrics.Ruby) int { return a.Start - b.Start })
			count += len(add)
		}
		if count > 0 {
			path := st.SongPath(id, song.FileLyrics)
			orig, err := os.ReadFile(path)
			if err != nil {
				return 0, err
			}
			if err := os.MkdirAll(e.Backup, 0o755); err != nil {
				return 0, err
			}
			if err := os.WriteFile(filepath.Join(e.Backup, id+".lyrics.txt"), orig, 0o644); err != nil {
				return 0, err
			}
			if err := store.WriteFile(path, []byte(lyrics.Serialize(doc))); err != nil {
				return 0, err
			}
			e.Log(fmt.Sprintf("  . %s：%s", sg.Source.Title, strings.Join(notes, "；")))
		}
	}

	// 2. 原本是最新的紀錄：換成新的指紋
	mid, midR, err := inspect(e.Versions)
	if err != nil {
		return count, err
	}
	if al := mid.Alignment; al != nil {
		changed := false
		if before.AlignOK {
			al.Key, al.Lyrics, al.LineLyrics = midR.AlignFP, midR.LyricsFP, slices.Clone(midR.LineLyrics)
			changed = true
		} else {
			// 還沒更新的歌詞修改（只重對改到的句子）：沒改字、只多了釘住讀音的句子換成新的逐句指紋，不會被當成改過
			swap := map[string]string{}
			for k := range min(len(before.LineLyrics), len(midR.LineLyrics)) {
				if before.LineLyrics[k] != midR.LineLyrics[k] {
					swap[before.LineLyrics[k]] = midR.LineLyrics[k]
				}
			}
			for j, fp := range al.LineLyrics {
				if n, ok := swap[fp]; ok {
					al.LineLyrics[j], changed = n, true
				}
			}
		}
		if al.Restored != nil && al.Restored.Lyrics == before.LyricsFP && before.LyricsFP != midR.LyricsFP {
			al.Restored.Lyrics, changed = midR.LyricsFP, true
		}
		if changed {
			if err := writeJSON(st.SongPath(id, song.FileAlignment), al); err != nil {
				return count, err
			}
		}
	}
	if before.Status.QA != nil {
		if doc, err := planner.ReadQA(st, id); err == nil && doc != nil {
			doc.Key = midR.QAFP
			if err := writeJSON(st.SongPath(id, song.FileQA), doc); err != nil {
				return count, err
			}
		}
	}
	current := map[string]bool{} // 原本是最新的成品
	if err := st.EditSong(id, func(s *song.Song) error {
		if before.AlignOK && s.Stages.Align != nil {
			s.Stages.Align.Key = midR.AlignFP
		}
		if before.Status.QA != nil && s.Stages.QA != nil {
			s.Stages.QA.Key = midR.QAFP
		}
		if before.Status.Approval == planner.Approved && s.Info.Approved != nil {
			s.Info.Approved.Fingerprint = midR.ApproveFP
		}
		for t, rec := range s.Stages.Render {
			if rec.Key == before.RenderFP[t] {
				current[t] = true
				if rec.Content == before.RenderPlain[t] {
					rec.Content = midR.RenderPlain[t]
				}
			}
		}
		return nil
	}); err != nil {
		return count, err
	}
	// 成品的指紋要等內容紀錄換好之後再算（手改過的 ASS 要內容相同才沿用）
	_, after, err := inspect(e.Versions)
	if err != nil {
		return count, err
	}
	if err := st.EditSong(id, func(s *song.Song) error {
		for t, rec := range s.Stages.Render {
			if current[t] {
				rec.Key = after.RenderFP[t]
			}
		}
		return nil
	}); err != nil {
		return count, err
	}

	// 3. 確認狀態和遷移前相同
	_, final, err := inspect(e.Versions)
	if err != nil {
		return count, err
	}
	if final.Status.Karaoke != before.Status.Karaoke || final.Status.Approval != before.Status.Approval || (final.Status.QA == nil) != (before.Status.QA == nil) {
		return count, fmt.Errorf("遷移後狀態不同：伴唱帶 %s → %s、確認 %q → %q", before.Status.Karaoke, final.Status.Karaoke, before.Status.Approval, final.Status.Approval)
	}
	return count, nil
}

// pins 找出一句裡要釘住的字（還是自動讀音的 私 / 何 / 明日 / 何時），讀音用改版前的。
// spans 是改版前快取的自動讀音；沒有快取時（cached = false）用舊規則一定會給的讀音。
func pins(text string, manual []lyrics.Ruby, spans []readings.Span, cached bool) []lyrics.Ruby {
	rs := []rune(text)
	covered := func(s, e int) bool {
		for _, r := range manual {
			if s < r.End && e > r.Start {
				return true
			}
		}
		return false
	}
	var out []lyrics.Ruby
	add := func(s, e int, reading string) {
		if !covered(s, e) {
			out = append(out, lyrics.Ruby{Start: s, End: e, Reading: reading})
		}
	}
	if cached {
		for i, sp := range spans {
			w := string(rs[sp.Start:sp.End])
			after何 := i > 0 && spans[i-1].End == sp.Start && string(rs[spans[i-1].Start:spans[i-1].End]) == "何"
			if w == "私" || w == "何" || w == "明日" || (w == "時" && after何) {
				add(sp.Start, sp.End, sp.Ruby)
			}
		}
		return out
	}
	isKanji := func(i int) bool { return i < len(rs) && rs[i] >= 0x4e00 && rs[i] <= 0x9fff }
	for i := 0; i < len(rs); i++ {
		switch {
		case rs[i] == '明' && i+1 < len(rs) && rs[i+1] == '日':
			add(i, i+2, "あす")
			i++
		case rs[i] == '何' && i+1 < len(rs) && rs[i+1] == '時':
			add(i, i+1, "なん")
			add(i+1, i+2, "じ")
			i++
		case rs[i] == '何' && !(i+1 < len(rs) && (rs[i+1] == '処' || rs[i+1] == '故')): // 何処、何故是另外的詞
			add(i, i+1, "なん")
		case rs[i] == '私' && (!isKanji(i+1) || rs[i+1] == '達'): // 私立、私的這類複合詞不是「私」
			add(i, i+1, "わたくし")
		}
	}
	return out
}

func writeJSON(path string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return store.WriteFile(path, append(data, '\n'))
}
