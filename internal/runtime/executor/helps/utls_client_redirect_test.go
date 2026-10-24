package helps

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestNewUtlsHTTPClientRefusesRedirects(t *testing.T) {
	for _, status := range []int{http.StatusMovedPermanently, http.StatusFound, http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var targetHits atomic.Int32
			target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				targetHits.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			defer target.Close()
			var originHits atomic.Int32
			origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				originHits.Add(1)
				w.Header().Set("Location", target.URL+"/v1/messages")
				w.WriteHeader(status)
			}))
			defer origin.Close()

			client := NewUtlsHTTPClient(context.Background(), &config.Config{}, nil, 0)
			req, errRequest := http.NewRequest(http.MethodPost, origin.URL+"/v1/messages", strings.NewReader(`{"model":"m"}`))
			if errRequest != nil {
				t.Fatal(errRequest)
			}
			req.Header.Set("Authorization", "Bearer fake-token")
			resp, errDo := client.Do(req)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if errDo == nil || !strings.Contains(errDo.Error(), "redirect") {
				t.Fatalf("Do() error = %v, want redirect refusal", errDo)
			}
			if got := originHits.Load(); got != 1 {
				t.Fatalf("origin hits = %d, want 1", got)
			}
			if got := targetHits.Load(); got != 0 {
				t.Fatalf("redirect target hits = %d, want 0", got)
			}
		})
	}
}
