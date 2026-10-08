package difflib

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestOpcodesSpec(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "testdata", "spec", "difflib.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		A, B    []string
		Opcodes [][]any
	}
	if err := json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, c := range cases {
		want := []Opcode{}
		for _, op := range c.Opcodes {
			want = append(want, Opcode{op[0].(string), int(op[1].(float64)), int(op[2].(float64)), int(op[3].(float64)), int(op[4].(float64))})
		}
		got := Opcodes(c.A, c.B)
		if got == nil {
			got = []Opcode{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("Opcodes(%q, %q)\n得到 %v\n應該 %v", c.A, c.B, got, want)
		}
	}
}
