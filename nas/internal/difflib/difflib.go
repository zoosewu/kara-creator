// Package difflib 移植 Python difflib.SequenceMatcher(isjunk=None, autojunk=False) 的 get_opcodes，
// 結果和 Python 完全相同（編輯原始歌詞時用它對應新舊句子；規格資料由 Python 產生）。
package difflib

import "sort"

// 標籤。
const (
	Equal   = "equal"
	Replace = "replace"
	Delete  = "delete"
	Insert  = "insert"
)

// Opcode 同 Python 的 (tag, i1, i2, j1, j2)：a[I1:I2] 要怎麼變成 b[J1:J2]。
type Opcode struct {
	Tag            string
	I1, I2, J1, J2 int
}

type match struct{ i, j, k int }

// Opcodes 回傳把 a 變成 b 的步驟。
func Opcodes[T comparable](a, b []T) []Opcode {
	var out []Opcode
	i, j := 0, 0
	for _, m := range matchingBlocks(a, b) {
		tag := ""
		switch {
		case i < m.i && j < m.j:
			tag = Replace
		case i < m.i:
			tag = Delete
		case j < m.j:
			tag = Insert
		}
		if tag != "" {
			out = append(out, Opcode{tag, i, m.i, j, m.j})
		}
		i, j = m.i+m.k, m.j+m.k
		if m.k > 0 {
			out = append(out, Opcode{Equal, m.i, i, m.j, j})
		}
	}
	return out
}

func matchingBlocks[T comparable](a, b []T) []match {
	b2j := map[T][]int{}
	for k, elt := range b {
		b2j[elt] = append(b2j[elt], k)
	}
	type span struct{ alo, ahi, blo, bhi int }
	queue := []span{{0, len(a), 0, len(b)}}
	var blocks []match
	for len(queue) > 0 {
		q := queue[len(queue)-1]
		queue = queue[:len(queue)-1]
		m := longest(a, b, b2j, q.alo, q.ahi, q.blo, q.bhi)
		if m.k > 0 {
			blocks = append(blocks, m)
			if q.alo < m.i && q.blo < m.j {
				queue = append(queue, span{q.alo, m.i, q.blo, m.j})
			}
			if m.i+m.k < q.ahi && m.j+m.k < q.bhi {
				queue = append(queue, span{m.i + m.k, q.ahi, m.j + m.k, q.bhi})
			}
		}
	}
	sort.Slice(blocks, func(x, y int) bool {
		if blocks[x].i != blocks[y].i {
			return blocks[x].i < blocks[y].i
		}
		if blocks[x].j != blocks[y].j {
			return blocks[x].j < blocks[y].j
		}
		return blocks[x].k < blocks[y].k
	})
	// 相鄰的區塊合併。
	var out []match
	cur := match{}
	for _, m := range blocks {
		if cur.i+cur.k == m.i && cur.j+cur.k == m.j {
			cur.k += m.k
			continue
		}
		if cur.k > 0 {
			out = append(out, cur)
		}
		cur = m
	}
	if cur.k > 0 {
		out = append(out, cur)
	}
	return append(out, match{len(a), len(b), 0})
}

// longest 同 Python find_longest_match（沒有 junk）：最長的相同區塊，同長時取 a 最前、再取 b 最前。
func longest[T comparable](a, b []T, b2j map[T][]int, alo, ahi, blo, bhi int) match {
	besti, bestj, bestk := alo, blo, 0
	j2len := map[int]int{}
	for i := alo; i < ahi; i++ {
		next := map[int]int{}
		for _, j := range b2j[a[i]] {
			if j < blo {
				continue
			}
			if j >= bhi {
				break
			}
			k := j2len[j-1] + 1
			next[j] = k
			if k > bestk {
				besti, bestj, bestk = i-k+1, j-k+1, k
			}
		}
		j2len = next
	}
	for besti > alo && bestj > blo && a[besti-1] == b[bestj-1] {
		besti, bestj, bestk = besti-1, bestj-1, bestk+1
	}
	for besti+bestk < ahi && bestj+bestk < bhi && a[besti+bestk] == b[bestj+bestk] {
		bestk++
	}
	return match{besti, bestj, bestk}
}
