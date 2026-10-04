package lyrics

import (
	"crypto/sha1"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
)

// V1AlignSHA1 是 v1 的歌詞對時雜湊（songtool/lyrics.py Lyrics.align_sha1）：
// sha1(json.dumps([[句子, [{"start", "end", "reading"}…]]…], ensure_ascii=False))，預設分隔符 ", " 與 ": "。
// 從 v1 格式的資料備份還原時，用它確認歌詞和對時當下相同，才沿用備份的對時。
func V1AlignSHA1(doc Document) string {
	var sb strings.Builder
	sb.WriteByte('[')
	for i, ln := range doc.LyricLines() {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString("[" + pyJSONString(ln.Text) + ", [")
		for k, r := range ln.Rubies {
			if k > 0 {
				sb.WriteString(", ")
			}
			fmt.Fprintf(&sb, `{"start": %d, "end": %d, "reading": %s}`, r.Start, r.End, pyJSONString(r.Reading))
		}
		sb.WriteString("]]")
	}
	sb.WriteByte(']')
	sum := sha1.Sum([]byte(sb.String()))
	return hex.EncodeToString(sum[:])
}

// pyJSONString 同 Python json.dumps(s, ensure_ascii=False)：只跳脫引號、反斜線與控制字元。
func pyJSONString(s string) string {
	var sb strings.Builder
	sb.WriteByte('"')
	for _, r := range s {
		switch r {
		case '"':
			sb.WriteString(`\"`)
		case '\\':
			sb.WriteString(`\\`)
		case '\n':
			sb.WriteString(`\n`)
		case '\r':
			sb.WriteString(`\r`)
		case '\t':
			sb.WriteString(`\t`)
		case '\b':
			sb.WriteString(`\b`)
		case '\f':
			sb.WriteString(`\f`)
		default:
			if r < 0x20 {
				sb.WriteString(`\u00` + strconv.FormatInt(int64(r)>>4, 16) + strconv.FormatInt(int64(r)&15, 16))
			} else {
				sb.WriteRune(r)
			}
		}
	}
	sb.WriteByte('"')
	return sb.String()
}
