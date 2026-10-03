package lyrics

import "github.com/zoosewu/kara-creator/nas/internal/difflib"

// FromPlain 把「原始歌詞」檢視（只有歌詞與翻譯，沒有標註）轉回結構（v1 ui/server.py 的 _from_plain）：
// 沒改到的句子沿用原本的演唱者、讀音與翻譯（用 diff 對應，插入或刪除句子也不會錯位）；
// 改過的句子保留演唱者，讀音只留下原字沒變的部分。
func FromPlain(text string, base Document) Document {
	doc := Parse(text)
	doc.Meta = map[string]string{}
	for k, v := range base.Meta {
		doc.Meta[k] = v
	}
	old := base.LyricLines()
	var idx []int // doc.Lines 裡歌詞句的位置
	var newTexts []string
	for i, ln := range doc.Lines {
		if ln.Kind == KindLyric {
			idx = append(idx, i)
			newTexts = append(newTexts, ln.Text)
		}
	}
	oldTexts := make([]string, len(old))
	for i, ln := range old {
		oldTexts[i] = ln.Text
	}
	for _, op := range difflib.Opcodes(oldTexts, newTexts) {
		if op.Tag != difflib.Equal && (op.Tag != difflib.Replace || op.I2-op.I1 != op.J2-op.J1) {
			continue
		}
		for k := range op.I2 - op.I1 {
			src, dst := old[op.I1+k], &doc.Lines[idx[op.J1+k]]
			if dst.Singer == nil {
				dst.Singer = src.Singer
			}
			if len(dst.Rubies) == 0 {
				srcText, dstText := []rune(src.Text), []rune(dst.Text)
				kept := []Ruby{}
				for _, r := range src.Rubies {
					if string(slice(dstText, r.Start, r.End)) == string(slice(srcText, r.Start, r.End)) {
						kept = append(kept, r)
					}
				}
				dst.Rubies = kept
			}
			if dst.Translation == "" && op.Tag == difflib.Equal {
				dst.Translation = src.Translation
			}
		}
	}
	return doc
}
