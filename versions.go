// Package kara 放 NAS 伺服器和 AI worker 共用的演算法 / 協定版本（versions.json）。
//
// versions.json 是唯一的來源：Python 直接讀檔，Go 在編譯時內嵌。
// 改了演算法就改 versions.json 的數字（調 align 會讓所有歌整首重新對時，先問使用者）。
package kara

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
)

//go:embed versions.json
var versionsJSON []byte

// Versions 是 versions.json 的內容。NAS 只派任務給版本完全相同的 worker。
type Versions struct {
	Protocol int `json:"protocol"`
	Separate int `json:"separate"`
	Align    int `json:"align"`
	QA       int `json:"qa"`
	Reading  int `json:"reading"`
	Render   int `json:"render"`
}

// Current 是編譯時內嵌的版本。
var Current = mustParse(versionsJSON)

func mustParse(data []byte) Versions {
	v, err := ParseVersions(data)
	if err != nil {
		panic(err)
	}
	return v
}

// ParseVersions 解析 versions.json；有不認得的欄位就失敗（新增版本項目時必須同步改這裡）。
func ParseVersions(data []byte) (Versions, error) {
	var v Versions
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&v); err != nil {
		return Versions{}, fmt.Errorf("versions.json：%w", err)
	}
	return v, nil
}

// Diff 列出和 other 不同的項目，例如 ["align 7 ≠ 6"]；完全相同時回傳 nil。
func (v Versions) Diff(other Versions) []string {
	var out []string
	add := func(name string, a, b int) {
		if a != b {
			out = append(out, fmt.Sprintf("%s %d ≠ %d", name, a, b))
		}
	}
	add("protocol", v.Protocol, other.Protocol)
	add("separate", v.Separate, other.Separate)
	add("align", v.Align, other.Align)
	add("qa", v.QA, other.QA)
	add("reading", v.Reading, other.Reading)
	add("render", v.Render, other.Render)
	return out
}
