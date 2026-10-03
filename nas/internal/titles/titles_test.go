package titles

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestGolden(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "golden", "titles.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Info    Info   `json:"info"`
		Guess   Guess  `json:"guess"`
		Channel string `json:"channel"`
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	fails := 0
	for i, c := range cases {
		if got := FromInfo(c.Info); got != c.Guess {
			t.Errorf("#%d %+v\n得到 %+v\n應該 %+v", i, c.Info, got, c.Guess)
			fails++
		}
		if got := CleanChannel(c.Info.Channel); got != c.Channel {
			t.Errorf("#%d CleanChannel(%q) = %q，應該 %q", i, c.Info.Channel, got, c.Channel)
			fails++
		}
		if fails > 20 {
			t.Fatal("錯太多，停止")
		}
	}
}
