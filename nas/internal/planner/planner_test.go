package planner

import (
	"testing"

	kara "github.com/zoosewu/kara-creator"
	"github.com/zoosewu/kara-creator/nas/internal/library"
	"github.com/zoosewu/kara-creator/nas/internal/lyrics"
	"github.com/zoosewu/kara-creator/nas/internal/song"
	"github.com/zoosewu/kara-creator/nas/internal/timing"
	wp "github.com/zoosewu/kara-creator/nas/internal/workerproto"
)

var versions = kara.Versions{Protocol: 1, Separate: 1, Align: 7, QA: 4, Reading: 1, Render: 1}

func fonts(lang string) (wp.Font, bool) {
	if lang == "ja" {
		return wp.Font{SHA256: "font-ja", Family: "Noto Sans CJK JP"}, true
	}
	return wp.Font{SHA256: "font-tc", Family: "Noto Sans CJK TC"}, true
}

func newInput() Input {
	sg := song.New("abc", song.Source{Kind: song.KindURL, Title: "虛構歌手『自己編的歌』Official Video",
		File: song.FileRef{Name: "source.mp4", SHA256: "src"}, Width: 1280, Height: 720})
	return Input{Song: sg, SourceOK: true, Render: map[string]RenderFiles{},
		Settings: library.Settings{SubtitleScale: 1, Fonts: map[string]string{}}, Font: fonts, Versions: versions}
}

func doc(text string) *lyrics.Document {
	d := lyrics.Parse(text)
	return &d
}

func lines(texts ...string) []wp.Line {
	var out []wp.Line
	for i, t := range texts {
		s := float64(i * 2)
		out = append(out, wp.Line{Text: t, Start: s, End: s + 1, Words: []wp.Word{{Text: t, Start: s, End: s + 1}}})
	}
	return out
}

// makeAll 模擬工作做完：照目前的指紋寫上所有紀錄。
func makeAll(t *testing.T, in *Input) Result {
	t.Helper()
	r := Evaluate(*in)
	sg := in.Song
	sg.Stages.Separate = &song.SeparateStage{Stage: song.Stage{Key: r.SeparateFP},
		Instrumental: song.FileRef{Name: "instrumental.mp4", SHA256: "inst"}, Vocals: song.FileRef{Name: "vocals.flac", SHA256: "voc"}}
	in.SeparateFilesOK = true
	r = Evaluate(*in)
	if in.Lyrics != nil {
		if in.Alignment == nil || in.Alignment.Key != r.AlignFP {
			in.Alignment = &timing.Alignment{Key: r.AlignFP, Lyrics: r.LyricsFP, Lines: lines(in.Lyrics.Texts()...)}
		}
		sg.Stages.Align = &song.Stage{Key: r.AlignFP}
		r = Evaluate(*in)
		sg.Stages.Render = map[string]*song.RenderStage{}
		for _, target := range sg.Info.Targets {
			hash := "ass-generated"
			if r.ManualASS[target] {
				hash = in.Render[target].ASSHash
			}
			sg.Stages.Render[target] = &song.RenderStage{Stage: song.Stage{Key: r.RenderFP[target]}, Content: r.RenderPlain[target],
				ASS: song.ASSRef{FileRef: song.FileRef{Name: "karaoke.ass", SHA256: hash}, Manual: r.ManualASS[target]}}
			in.Render[target] = RenderFiles{VideoOK: true, ASSOK: true, ASSHash: hash}
		}
		sg.Stages.QA = &song.Stage{Key: r.QAFP}
		in.QAKey, in.QACounts = r.QAFP, QACounts{Wrong: 1}
	}
	return Evaluate(*in)
}

func TestNewSong(t *testing.T) {
	in := newInput()
	r := Evaluate(in)
	if r.Status != (Status{Download: Done, Separate: Pending, Lyrics: Missing, Karaoke: NoLyrics}) {
		t.Fatalf("%+v", r.Status)
	}
	if r.Title != "自己編的歌" || r.Artist != "虛構歌手" || *r.TitleCard[0] != "自己編的歌" {
		t.Fatalf("標題辨識：%q %q", r.Title, r.Artist)
	}
	in.Lyrics = doc("一句\n")
	if r := Evaluate(in); r.Status.Karaoke != Pending || r.AlignFP != "" {
		t.Fatalf("有歌詞、還沒做：%+v", r.Status)
	}
	in.SourceOK = false
	if r := Evaluate(in); r.Status.Download != Missing {
		t.Fatal("來源不見了")
	}
}

func TestDoneAndOutdated(t *testing.T) {
	in := newInput()
	in.Lyrics = doc("[男] 第一句\n> 翻譯\n第二句\n")
	r := makeAll(t, &in)
	want := Status{Download: Done, Separate: Done, Lyrics: Done, Karaoke: Done, QA: &QACounts{Wrong: 1}}
	if r.Status.Download != want.Download || r.Status.Separate != Done || r.Status.Karaoke != Done || r.Status.QA == nil || r.Status.QA.Wrong != 1 {
		t.Fatalf("%+v", r.Status)
	}
	if len(r.Singers) != 2 || *r.Singers[0] != "男" || r.Singers[1] != nil || len(r.Translations) != 2 || r.Translations[0] != "翻譯" {
		t.Fatalf("演唱者 / 翻譯：%v %v", r.Singers, r.Translations)
	}
	if r.Size != "1280x720" || r.Font.SHA256 != "font-tc" || r.Language != "zh" {
		t.Fatalf("%q %+v %q", r.Size, r.Font, r.Language)
	}

	cases := []struct {
		name     string
		change   func(in *Input)
		karaoke  string
		alignOK  bool
		separate string
	}{
		{"改歌詞 → 整首重新對時", func(in *Input) { in.Lyrics = doc("[男] 第一句改了\n第二句\n") }, Outdated, false, Done},
		{"只改演唱者 → 只重燒", func(in *Input) { in.Lyrics = doc("[女] 第一句\n> 翻譯\n第二句\n") }, Outdated, true, Done},
		{"字幕大小 → 只重燒", func(in *Input) { in.Settings.SubtitleScale = 1.2 }, Outdated, true, Done},
		{"燒錄方法改版", func(in *Input) { in.Versions.Render++ }, Outdated, true, Done},
		{"讀音規則改版", func(in *Input) { in.Versions.Reading++ }, Outdated, true, Done},
		{"對時方法改版 → 整首重新對時", func(in *Input) { in.Versions.Align++ }, Outdated, false, Done},
		{"不燒翻譯", func(in *Input) { in.Song.Info.Translation = false }, Outdated, true, Done},
		{"語言改了 → 重新對時", func(in *Input) { in.Song.Info.Language = "nan" }, Outdated, false, Done},
		{"成品影片不見了", func(in *Input) {
			in.Render[song.TargetInstrumental] = RenderFiles{ASSOK: true, ASSHash: "ass-generated"}
		}, Outdated, true, Done},
		{"伴奏被換掉 → 重新去人聲", func(in *Input) { in.SeparateFilesOK = false }, Outdated, false, Outdated},
		{"來源換了", func(in *Input) { in.Song.Source.File.SHA256 = "new" }, Outdated, false, Outdated},
		{"手動調時間 → 只重燒", func(in *Input) { in.Alignment.Lines[0].Start += 0.1 }, Outdated, true, Done},
		{"沒變", func(in *Input) {}, Done, true, Done},
	}
	for _, c := range cases {
		in := newInput()
		in.Lyrics = doc("[男] 第一句\n> 翻譯\n第二句\n")
		makeAll(t, &in)
		c.change(&in)
		r := Evaluate(in)
		if r.Status.Karaoke != c.karaoke || r.AlignOK != c.alignOK || r.Status.Separate != c.separate {
			t.Errorf("%s：karaoke=%s alignOK=%v separate=%s", c.name, r.Status.Karaoke, r.AlignOK, r.Status.Separate)
		}
	}
}

func TestQAInvalidatedByTiming(t *testing.T) {
	in := newInput()
	in.Lyrics = doc("一\n二\n")
	makeAll(t, &in)
	in.Alignment.Lines[1].End += 0.5
	if r := Evaluate(in); r.Status.QA != nil {
		t.Fatal("對時改了，舊的檢查結果不該顯示")
	}
}

func TestApproval(t *testing.T) {
	in := newInput()
	in.Lyrics = doc("一\n二\n")
	r := makeAll(t, &in)
	if r.Status.Approval != "" {
		t.Fatal("沒確認過")
	}
	in.Song.Info.Approved = &song.Approval{Fingerprint: r.ApproveFP}
	if r := Evaluate(in); r.Status.Approval != Approved {
		t.Fatal(r.Status.Approval)
	}
	// Q6：換字型、改字幕大小不必重新確認
	in.Settings.SubtitleScale = 1.5
	in.Font = func(string) (wp.Font, bool) { return wp.Font{SHA256: "other"}, true }
	if r := Evaluate(in); r.Status.Approval != Approved {
		t.Fatal("換字型、字幕大小不影響已確認")
	}
	in.Song.Info.Note = "作詞：某人"
	if r := Evaluate(in); r.Status.Approval != Stale {
		t.Fatal("備註改了（標題畫面變了）要重新確認")
	}
}

func TestManualASS(t *testing.T) {
	in := newInput()
	in.Lyrics = doc("一\n二\n")
	makeAll(t, &in)
	target := song.TargetInstrumental

	// 使用者用 Aegisub 改了 ASS → 要用這份重新燒錄
	in.Render[target] = RenderFiles{VideoOK: true, ASSOK: true, ASSHash: "ass-edited"}
	r := Evaluate(in)
	if !r.ManualASS[target] || r.Status.Karaoke != Outdated {
		t.Fatalf("手改 ASS：manual=%v karaoke=%s", r.ManualASS[target], r.Status.Karaoke)
	}
	r = makeAll(t, &in)
	if !r.ManualASS[target] || r.Status.Karaoke != Done || !in.Song.Stages.Render[target].ASS.Manual {
		t.Fatalf("用手改的 ASS 燒好之後：manual=%v karaoke=%s", r.ManualASS[target], r.Status.Karaoke)
	}
	// 內容（字幕大小）改了 → 手改的 ASS 作廢，重新產生
	in.Settings.SubtitleScale = 0.8
	if r := Evaluate(in); r.ManualASS[target] || r.Status.Karaoke != Outdated {
		t.Fatalf("內容改了應該重新產生 ASS：manual=%v", r.ManualASS[target])
	}
}

func TestTitleCardAndCounts(t *testing.T) {
	in := newInput()
	in.Song.Source.Title = "完全沒有規則的標題"
	in.Lyrics = doc("一\n二\n")
	r := Evaluate(in)
	if r.TitleCard[0] != nil || r.TitleCard[1] != nil || len(r.TitleCard) != 2 {
		t.Fatal("只能用整個影片標題時不顯示標題畫面")
	}
	in.Song.Info.Note = "備註"
	in.Lyrics = doc("# title: 歌詞檔的歌名\n一\n二\n")
	r = Evaluate(in)
	if len(r.TitleCard) != 3 || *r.TitleCard[0] != "歌詞檔的歌名" || *r.TitleCard[2] != "備註" {
		t.Fatalf("%v", r.TitleCard)
	}
	// 對時句數和歌詞不同：演唱者、翻譯都不用
	in.Lyrics = doc("[男] 一\n> 翻\n[女] 二\n")
	in.Alignment = &timing.Alignment{Lines: lines("一")}
	if r := Evaluate(in); r.Singers != nil || r.Translations != nil {
		t.Fatalf("%v %v", r.Singers, r.Translations)
	}
}

func TestRestored(t *testing.T) {
	in := newInput()
	in.Lyrics = doc("一\n二\n")
	r := Evaluate(in)
	in.Alignment = &timing.Alignment{Lines: lines("一", "二"), Restored: &timing.Restored{Lyrics: r.LyricsFP, Language: "zh", Method: 7}}
	if r := Evaluate(in); !r.RestoredUsable {
		t.Fatal("歌詞、語言、方法都相同應該可以沿用")
	}
	in.Versions.Align = 8
	if r := Evaluate(in); r.RestoredUsable {
		t.Fatal("方法改了不能沿用")
	}
}
