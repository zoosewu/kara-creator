// Package pystr 照 Python 的字串規則處理文字（str.isspace、strip、split()、splitlines、re 的 \s）。
// 歌詞、標題的規則以 Python 的字串語意定義（規格資料就是這樣產生的）；Go 的 unicode.IsSpace 範圍和 Python 不同，要用這裡的。
package pystr

import (
	"strings"
	"unicode"
)

// SpaceClass 是 Python re 的 \s（str 樣式）放進字元類別用的內容：和 str.isspace() 相同的字元。
const SpaceClass = `\x{9}-\x{d}\x{1c}-\x{20}\x{85}\x{a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}`

// IsSpace 同 Python str.isspace() 對單一字元的判斷。
func IsSpace(r rune) bool {
	switch {
	case r >= 0x9 && r <= 0xd, r >= 0x1c && r <= 0x20, r == 0x85, r == 0xa0, r == 0x1680,
		r >= 0x2000 && r <= 0x200a, r == 0x2028, r == 0x2029, r == 0x202f, r == 0x205f, r == 0x3000:
		return true
	}
	return false
}

// Strip 同 Python str.strip()。
func Strip(s string) string { return strings.TrimFunc(s, IsSpace) }

// LStrip 同 Python str.lstrip()。
func LStrip(s string) string { return strings.TrimLeftFunc(s, IsSpace) }

// Fields 同 Python str.split()（沒有參數）。
func Fields(s string) []string { return strings.FieldsFunc(s, IsSpace) }

// JoinFields 同 Python " ".join(s.split())：連續空白（含全形）收斂成一個半形空白，去掉頭尾。
func JoinFields(s string) string { return strings.Join(Fields(s), " ") }

// isLineBreak 是 Python str.splitlines() 認得的換行字元（\r\n 另外處理）。
func isLineBreak(r rune) bool {
	switch r {
	case '\n', '\r', '\v', '\f', 0x1c, 0x1d, 0x1e, 0x85, 0x2028, 0x2029:
		return true
	}
	return false
}

// SplitLines 同 Python str.splitlines()（不保留換行字元）。
func SplitLines(s string) []string {
	var out []string
	start := 0
	for i, r := range s {
		if i < start || !isLineBreak(r) {
			continue
		}
		out = append(out, s[start:i])
		start = i + len(string(r))
		if r == '\r' && start < len(s) && s[start] == '\n' {
			start++
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}

// Casefold 同 Python str.casefold()（Unicode 版本相同時）。
func Casefold(s string) string {
	var sb strings.Builder
	for _, r := range s {
		if f, ok := casefoldSpecial[r]; ok {
			sb.WriteString(f)
		} else {
			sb.WriteRune(unicode.ToLower(r))
		}
	}
	return sb.String()
}
