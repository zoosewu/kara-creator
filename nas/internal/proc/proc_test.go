package proc

import (
	"context"
	"testing"
	"time"
)

func TestCancelKillsChildren(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	cmd := Command(ctx, "sh", "-c", "sleep 30; echo done")
	start := time.Now()
	out, _ := cmd.Output() // sleep 是子程序，佔著 stdout
	if time.Since(start) > 3*time.Second || len(out) != 0 {
		t.Fatalf("取消時要連子程序一起結束（花了 %v）", time.Since(start))
	}
}
