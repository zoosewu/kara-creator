package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	kara "github.com/zoosewu/kara-creator"
)

func TestHealthz(t *testing.T) {
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest("GET", "/healthz", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d", rec.Code)
	}
	var body struct {
		OK       bool          `json:"ok"`
		Versions kara.Versions `json:"versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Versions != kara.Current {
		t.Fatalf("%+v", body)
	}
}
