// Package events 是即時事件（SSE，/api/v1/events）的發送中心：取代 v1 每 1.5 秒輪詢。
package events

import "sync"

// Event 是一則事件。
type Event struct {
	Kind string // song、library、job、job.log、worker、readings、settings
	Data any
}

// Hub 把事件送給所有訂閱者。訂閱者跟不上（緩衝滿了）就斷掉它，前端重連後重抓 /library。
type Hub struct {
	mu   sync.Mutex
	subs map[chan Event]struct{}
}

// New 建立 Hub。
func New() *Hub { return &Hub{subs: map[chan Event]struct{}{}} }

// Subscribe 訂閱；用完呼叫 cancel。
func (h *Hub) Subscribe() (<-chan Event, func()) {
	ch := make(chan Event, 256)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()
	return ch, func() {
		h.mu.Lock()
		if _, ok := h.subs[ch]; ok {
			delete(h.subs, ch)
			close(ch)
		}
		h.mu.Unlock()
	}
}

// Publish 送出事件（不會卡住）。
func (h *Hub) Publish(kind string, data any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- Event{kind, data}:
		default:
			delete(h.subs, ch)
			close(ch)
		}
	}
}

// Count 回傳目前的訂閱者數。
func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.subs)
}
