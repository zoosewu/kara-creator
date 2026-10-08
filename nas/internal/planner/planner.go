// Package planner 判斷一首歌各階段的狀態（docs/data.md「狀態判斷」）與目前的指紋。
//
// Evaluate 是純函式（不碰檔案，好測試）；Inspect 負責讀檔、確認檔案，組出 Evaluate 的輸入。
package planner

import (
	"strconv"

	kara "github.com/zoosewu/kara-creator"
	"github.com/zoosewu/kara-creator/nas/internal/difflib"
	"github.com/zoosewu/kara-creator/nas/internal/fingerprint"
	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/lyrics"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/timing"
	"github.com/zoosewu/kara-creator/nas/internal/titles"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

// 固定的模型設定（改了會讓所有歌的對應階段需要重做）。
const (
	SeparateModel = "htdemucs"
	SeparateStems = 2
	WhisperModel  = "large-v3"
)

// DefaultSize 是純音訊來源的畫面大小（黑底）。
const DefaultSize = "1920x1080"

// 狀態。
const (
	Done     = "done"
	Pending  = "pending"  // 還沒做過
	Outdated = "outdated" // 去人聲：做過，但來源或方法變了

	// 伴唱帶的「需更新」分成兩種（已確認只會因為需重新對時而失效）
	NeedsRender = "needs_render" // 只需重燒：演唱者、翻譯、標題畫面、字型、字幕大小、手動調時間、AI 重對某幾句、燒錄方法…
	NeedsAlign  = "needs_align"  // 需重新對時：歌詞文字或讀音、人聲（重新去人聲）、語言、對時方法改了
	Missing     = "missing"      // 檔案不見了（來源、歌詞）
	NoLyrics    = "no_lyrics"

	Approved = "approved" // 確認過，內容沒變
	Stale    = "stale"    // 確認之後內容變了，需重新確認
)

// RenderFiles 是一個成品目前的檔案狀況（Inspect 確認）。
type RenderFiles struct {
	VideoOK bool   // 成品影片存在，和紀錄相同
	ASSOK   bool   // karaoke.ass 存在
	ASSHash string // karaoke.ass 目前的 sha256
}

// Input 是判斷一首歌需要的全部資料。
type Input struct {
	Song            *song.Song
	SourceOK        bool              // 來源檔存在，sha256 和紀錄相同
	SeparateFilesOK bool              // 伴奏、人聲都存在，和紀錄相同
	Lyrics          *lyrics.Document  // nil = 沒有歌詞檔
	Alignment       *timing.Alignment // nil = 還沒對時
	QAKey           string            // qa.json 的 key（沒有時為空）
	QACounts        QACounts          // qa.json 的統計
	Render          map[string]RenderFiles
	Settings        library.Settings
	Font            func(language string) (wp.Font, bool) // 這個語言要用的字型；找不到字型檔時 ok 為 false
	Versions        kara.Versions
}

// QACounts 是對時檢查的統計。
type QACounts struct {
	Wrong   int `json:"wrong"`   // 可能不準
	Suspect int `json:"suspect"` // 待確認
}

// Status 是給 UI 看的各階段狀態。
type Status struct {
	Download string    `json:"download"` // done | missing
	Separate string    `json:"separate"` // done | pending | outdated
	Lyrics   string    `json:"lyrics"`   // done | missing
	Karaoke  string    `json:"karaoke"`  // done | pending | needs_render | needs_align | no_lyrics
	QA       *QACounts `json:"qa"`       // 沒檢查過或對時改了之後為 null（UI 不顯示疑慮數）
	Approval string    `json:"approval"` // approved | stale（重新對時後失效）| ""（沒確認過）
}

// Result 是判斷結果。
type Result struct {
	Status   Status
	Language string // 演唱語言（手動指定 > 依歌詞判斷）；判斷不出來時為空
	Title    string // 實際使用的歌名與演唱者（手動 > 歌詞檔 > 標題辨識）
	Artist   string
	Guess    titles.Guess

	// 成品的內容
	TitleCard    []*string // [歌名, 演唱者(, 備註)]；不顯示標題畫面時 [nil, nil]
	Singers      []*string // 句數和對時不符時為空
	Translations []string  // 不燒時為空
	Size         string    // 寬x高
	Font         wp.Font
	FontOK       bool

	// 目前的指紋
	LyricsFP    string
	SeparateFP  string
	AlignFP     string // 需要人聲：去人聲不是最新時為空
	ContentFP   string // 對時內容指紋（還沒對時時為空）
	QAFP        string
	ApproveFP   string
	RenderFP    map[string]string // 成品指紋（含手動 ASS）
	RenderPlain map[string]string // 不看手動 ASS 的成品指紋（寫進 RenderStage.Content）
	ManualASS   map[string]bool   // 這個成品要用使用者手改的 ASS

	// AlignOK：alignment.json 是目前的歌詞、人聲、語言、方法對出來的（不必重新對時）。
	AlignOK bool
	// LastApproval 是最近一次確認（目前有效或已經失效的）；沒確認過為 nil。
	LastApproval *song.Approval
	// ChangedSinceApproval：確認失效時，目前歌詞裡和上次確認時不同的句子（從 0 起算）；新增的句子也算。
	ChangedSinceApproval []int

	// RestoredUsable：alignment.json 是從資料備份還原的，歌詞、語言、方法都相同，可以直接沿用（不重新對時）。
	RestoredUsable bool
}

// Evaluate 判斷狀態與指紋。
func Evaluate(in Input) Result {
	sg := in.Song
	r := Result{RenderFP: map[string]string{}, RenderPlain: map[string]string{}, ManualASS: map[string]bool{}}

	var doc lyrics.Document
	if in.Lyrics != nil {
		doc = *in.Lyrics
	}
	lyricLines := doc.LyricLines()
	texts := doc.Texts()
	rubies := make([][]lyrics.Ruby, len(lyricLines))
	for i, ln := range lyricLines {
		rubies[i] = ln.Rubies
	}

	// 語言、歌名、演唱者、標題畫面
	r.Language = first(sg.Info.Language, lyrics.DetectLanguage(texts))
	r.Title, r.Artist, r.Guess = Display(sg, doc.Meta)
	// 標題辨識只能用整個影片標題時不顯示標題畫面，避免整串 YouTube 標題上畫面。
	known := sg.Info.Title != "" || doc.Meta["title"] != "" || r.Guess.Source != titles.SourceFallback
	var cardTitle, cardArtist, cardNote string
	r.TitleCard = []*string{nil, nil}
	if known {
		cardTitle, cardArtist, cardNote = r.Title, r.Artist, sg.Info.Note
		r.TitleCard[0] = &cardTitle
		if cardArtist != "" {
			r.TitleCard[1] = &cardArtist
		}
		if cardNote != "" {
			r.TitleCard = append(r.TitleCard, &cardNote)
		}
	}

	// 來源、去人聲、歌詞
	r.Status.Download = Done
	if !in.SourceOK {
		r.Status.Download = Missing
	}
	r.SeparateFP = fingerprint.Separate(sg.Source.File.SHA256, SeparateModel, SeparateStems, in.Versions.Separate)
	sep := sg.Stages.Separate
	switch {
	case sep == nil:
		r.Status.Separate = Pending
	case sep.Key != r.SeparateFP || !in.SeparateFilesOK:
		r.Status.Separate = Outdated
	default:
		r.Status.Separate = Done
	}
	r.Status.Lyrics = Done
	if in.Lyrics == nil {
		r.Status.Lyrics = Missing
	}

	// 對時
	r.LyricsFP = fingerprint.Lyrics(texts, rubies)
	if r.Status.Separate == Done {
		r.AlignFP = fingerprint.Align(r.LyricsFP, sep.Vocals.SHA256, WhisperModel, r.Language, in.Versions.Align)
	}
	al := in.Alignment
	r.AlignOK = al != nil && r.AlignFP != "" && al.Key == r.AlignFP && sg.Stages.Align != nil && sg.Stages.Align.Key == r.AlignFP
	r.RestoredUsable = al != nil && al.Restored != nil && al.Restored.Lyrics == r.LyricsFP &&
		al.Restored.Language == r.Language && al.Restored.Method == in.Versions.Align

	// 成品的內容（演唱者、翻譯要和對時的句數相同才能一句一句對上）
	lineCount := -1
	if al != nil {
		lineCount = len(al.Lines)
		r.ContentFP = fingerprint.Alignment(al.Lines)
	}
	anyTranslation := false
	for _, ln := range lyricLines {
		anyTranslation = anyTranslation || ln.Translation != ""
	}
	if len(lyricLines) == lineCount {
		for _, ln := range lyricLines {
			r.Singers = append(r.Singers, ln.Singer)
			if sg.Info.Translation && anyTranslation {
				r.Translations = append(r.Translations, ln.Translation)
			}
		}
	}
	r.Size = DefaultSize
	if sg.Source.Width > 0 && sg.Source.Height > 0 {
		r.Size = strconv.Itoa(sg.Source.Width) + "x" + strconv.Itoa(sg.Source.Height)
	}
	if in.Font != nil {
		r.Font, r.FontOK = in.Font(r.Language)
	}
	singers := fingerprint.Items(r.Singers)
	translations := fingerprint.Items(ptrs(r.Translations))
	if al != nil {
		r.ApproveFP = fingerprint.Approve(r.LyricsFP, r.Language, in.Versions.Align, al.Run)
		if sep != nil {
			r.QAFP = fingerprint.QA(r.ContentFP, r.LyricsFP, sep.Vocals.SHA256, r.Language, WhisperModel, in.Versions.QA)
		}
	}

	// 成品
	karaoke := Done
	for _, target := range sg.Info.Targets {
		media := sg.Source.File.SHA256
		if target == song.TargetInstrumental {
			media = ""
			if sep != nil {
				media = sep.Instrumental.SHA256
			}
		}
		ri := fingerprint.RenderInput{Target: target, Media: media, Alignment: r.ContentFP, Lyrics: r.LyricsFP,
			Singers: singers, Translations: translations, Title: cardTitle, Artist: cardArtist, Note: cardNote,
			Scale: in.Settings.SubtitleScale, Font: r.FontID(), Size: r.Size, Version: in.Versions.Render,
			Reading: in.Versions.Reading}
		plain := fingerprint.Render(ri)
		r.RenderPlain[target], r.RenderFP[target] = plain, plain
		rec := sg.Stages.Render[target]
		files := in.Render[target]
		// 使用者手改過 ASS（Q15）：產生 ASS 時的內容到現在都沒變，就沿用手改的 ASS
		if rec != nil && files.ASSOK && (files.ASSHash != rec.ASS.SHA256 || rec.ASS.Manual) && rec.Content == plain {
			ri.ASS = files.ASSHash
			r.RenderFP[target] = fingerprint.Render(ri)
			r.ManualASS[target] = true
		}
		switch {
		case rec == nil:
			karaoke = worse(karaoke, Pending)
		case (!r.AlignOK && !r.RestoredUsable) || r.Status.Separate != Done:
			karaoke = worse(karaoke, NeedsAlign)
		case !r.AlignOK || rec.Key != r.RenderFP[target] || !files.VideoOK:
			karaoke = worse(karaoke, NeedsRender) // 從資料備份還原、可以沿用的對時也只需重燒
		}
	}
	if in.Lyrics == nil {
		karaoke = NoLyrics
	}
	r.Status.Karaoke = karaoke

	// 對時檢查、已確認
	if r.QAFP != "" && in.QAKey == r.QAFP && sg.Stages.QA != nil && sg.Stages.QA.Key == r.QAFP {
		counts := in.QACounts
		r.Status.QA = &counts
	}
	if n := len(sg.Info.History); n > 0 {
		r.LastApproval = &sg.Info.History[n-1]
	}
	if a := sg.Info.Approved; a != nil {
		r.LastApproval = a
		r.Status.Approval = Stale
		if r.ApproveFP != "" && a.Fingerprint == r.ApproveFP && karaoke != NeedsAlign {
			r.Status.Approval = Approved
		}
	}
	if r.Status.Approval != Approved && r.LastApproval != nil && r.LastApproval.Texts != nil {
		r.ChangedSinceApproval = []int{}
		for _, op := range difflib.Opcodes(r.LastApproval.Texts, texts) {
			if op.Tag != difflib.Equal {
				for j := op.J1; j < op.J2; j++ {
					r.ChangedSinceApproval = append(r.ChangedSinceApproval, j)
				}
			}
		}
	}
	return r
}

// FontID 是成品用的字型 id（字型檔 sha256:第幾個字型；.ttc 一個檔案裡有好幾個字型）。
func (r Result) FontID() string {
	if r.Font.SHA256 == "" {
		return ""
	}
	return r.Font.SHA256 + ":" + strconv.Itoa(r.Font.Index)
}

// Display 回傳實際使用的歌名與演唱者：手動設定 > 歌詞檔的 # title / # artist > 自動辨識。
// 自動辨識不會寫進紀錄，所以永遠不會蓋掉手動設定；手動欄位清空就回到自動辨識。
func Display(sg *song.Song, lyricsMeta map[string]string) (title, artist string, guess titles.Guess) {
	guess = titles.FromInfo(titles.Info{Title: sg.Source.Title, Track: sg.Source.Track, Artists: sg.Source.Artists,
		Channel: sg.Source.Channel, Uploader: sg.Source.Uploader})
	return first(sg.Info.Title, lyricsMeta["title"], guess.Title), first(sg.Info.Artist, lyricsMeta["artist"], guess.Artist), guess
}

// worse 回傳兩個狀態中比較需要處理的（pending > needs_align > needs_render > done）。
func worse(a, b string) string {
	rank := map[string]int{Done: 0, NeedsRender: 1, NeedsAlign: 2, Pending: 3}
	if rank[b] > rank[a] {
		return b
	}
	return a
}

func first(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func ptrs(values []string) []*string {
	out := make([]*string, len(values))
	for i := range values {
		out[i] = &values[i]
	}
	return out
}
