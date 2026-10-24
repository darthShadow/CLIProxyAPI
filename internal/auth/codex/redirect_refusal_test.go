package codex

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestNewCodexAuthClientRefusesRedirects(t *testing.T) {
	var targetHits atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetHits.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/oauth/token")
		w.WriteHeader(http.StatusPermanentRedirect)
	}))
	defer origin.Close()

	auth := NewCodexAuth(&config.Config{})
	resp, errDo := auth.httpClient.Post(origin.URL+"/oauth/token", "application/x-www-form-urlencoded", strings.NewReader("grant_type=refresh_token&refresh_token=fake"))
	if resp != nil {
		_ = resp.Body.Close()
	}
	if errDo == nil || !strings.Contains(errDo.Error(), "redirect") {
		t.Fatalf("Post() error = %v, want redirect refusal", errDo)
	}
	if got := targetHits.Load(); got != 0 {
		t.Fatalf("redirect target hits = %d, want 0", got)
	}
}
