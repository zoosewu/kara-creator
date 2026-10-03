package events

import "testing"

func TestHub(t *testing.T) {
	h := New()
	a, cancelA := h.Subscribe()
	b, cancelB := h.Subscribe()
	h.Publish("song", "abc")
	if e := <-a; e.Kind != "song" || e.Data != "abc" {
		t.Fatal(e)
	}
	<-b
	cancelA()
	cancelA() // 重複取消沒關係
	h.Publish("library", nil)
	if _, ok := <-a; ok {
		t.Fatal("取消後 channel 要關閉")
	}
	<-b
	// 跟不上的訂閱者被斷掉
	for range 300 {
		h.Publish("job", nil)
	}
	if h.Count() != 0 {
		t.Fatal("緩衝滿了要斷掉")
	}
	cancelB()
}
