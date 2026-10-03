package config

import (
	"io"
	"path/filepath"
	"testing"
)

func env(m map[string]string) func(string) string {
	return func(k string) string { return m[k] }
}

func TestFlagsOverrideEnv(t *testing.T) {
	c, err := Parse([]string{"--library", "/lib", "--listen", ":9000"},
		env(map[string]string{"KARA_LIBRARY": "/env-lib", "KARA_LISTEN": ":1", "KARA_WORKER_TOKEN": "s"}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Library != "/lib" || c.Listen != ":9000" || c.WorkerToken != "s" {
		t.Fatalf("參數應該優先於環境變數：%+v", c)
	}
	if c.Data != filepath.Join("/lib", "data") {
		t.Fatalf("data 預設應該在曲庫底下：%q", c.Data)
	}
}

func TestEnvOnly(t *testing.T) {
	c, err := Parse(nil, env(map[string]string{"KARA_LIBRARY": "/env-lib", "KARA_DATA": "/backup"}), io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	if c.Library != "/env-lib" || c.Data != "/backup" || c.Listen != ":8765" {
		t.Fatalf("%+v", c)
	}
}

func TestLibraryRequired(t *testing.T) {
	if _, err := Parse(nil, env(nil), io.Discard); err == nil {
		t.Fatal("沒有指定曲庫應該要失敗")
	}
}
