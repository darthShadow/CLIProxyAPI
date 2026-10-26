package executor

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func TestReadCodexAccountUsageReadsPlanAndResets(t *testing.T) {
	for _, tc := range []struct {
		name          string
		first, second int
		account       string
		calls         int
		wantErr       string
	}{{"success", 200, 200, "acct-1", 2, ""}, {"first status", 401, 200, "acct-1", 1, "HTTP 401"}, {"mismatch", 200, 200, "other", 1, "answered for a different account"}, {"second status", 200, 404, "acct-1", 2, ""}} {
		t.Run(tc.name, func(t *testing.T) {
			var mu sync.Mutex
			var paths []string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				if r.Method != "GET" || strings.Contains(r.URL.Path, "consume") {
					t.Errorf("unsafe request %s %s", r.Method, r.URL.Path)
				}
				if r.Header.Get("Authorization") != "Bearer fake-access-token" {
					t.Error("wrong token")
				}
				if r.URL.Path == "/wham/usage" {
					w.WriteHeader(tc.first)
					fmt.Fprintf(w, `{"plan_type":"pro","account_id":%q,"rate_limit_reset_credits":{"available_count":1},"fake_token":"sk-fake-upstream-1111"}`, tc.account)
				} else if r.URL.Path == "/wham/rate-limit-reset-credits" {
					w.WriteHeader(tc.second)
					fmt.Fprint(w, `{"available_count":2,"credits":[{"status":"available","expires_at":"2026-11-25T10:00:00Z"},{"status":"available","expires_at":null}],"fake_token":"sk-fake-upstream-1111"}`)
				} else {
					t.Errorf("unexpected path %s", r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer srv.Close()
			a := codexGuardAuth(t, tc.name, srv.URL+"/codex", nil)
			v, e := ReadCodexAccountUsage(context.Background(), &config.Config{}, a)
			if tc.wantErr != "" {
				if e == nil || e.Error() != tc.wantErr {
					t.Errorf("error=%v want=%q", e, tc.wantErr)
				}
			} else {
				if e != nil {
					t.Errorf("unexpected error %v", e)
				}
				if v.Plan != "pro" {
					t.Errorf("plan=%q want=pro", v.Plan)
				}
				if tc.second == 200 {
					if v.ResetsAvailable != 2 || len(v.ResetExpiries) != 2 {
						t.Errorf("result=%+v", v)
					}
				} else if v.ResetsAvailable != 1 || v.ResetDetailsErr == nil || v.ResetDetailsErr.Error() != "HTTP 404" {
					t.Errorf("fallback=%+v", v)
				}
			}
			if strings.Contains(fmt.Sprintf("%#v %v", v, e), "sk-fake-upstream") {
				t.Fatal("upstream body leaked")
			}
			mu.Lock()
			defer mu.Unlock()
			if len(paths) != tc.calls {
				t.Errorf("calls=%v want=%d", paths, tc.calls)
			}
		})
	}
}
func TestCodexUsageRequestMatchesGuard(t *testing.T) {
	var mu sync.Mutex
	var headers []http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Clone())
		mu.Unlock()
		if r.Method != "GET" {
			t.Errorf("method=%s", r.Method)
		}
		if r.URL.Path == "/wham/usage" {
			fmt.Fprint(w, codexUsageBody(true, false, time.Now().Add(time.Hour).Unix(), "acct-1"))
		} else {
			fmt.Fprint(w, `{"available_count":0,"credits":[]}`)
		}
	}))
	defer srv.Close()
	a := codexGuardAuth(t, "request-match", srv.URL, nil)
	if !probeCodexIncludedUsage(context.Background(), &config.Config{}, a, time.Now()).allowed {
		t.Fatal("guard refused control")
	}
	if _, e := ReadCodexAccountUsage(context.Background(), &config.Config{}, a); e != nil {
		t.Errorf("new shared read unavailable: %v", e)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(headers) != 3 {
		t.Fatalf("got %d requests want guard plus two reads", len(headers))
	}
	for i, h := range headers {
		for key, want := range map[string]string{"Authorization": "Bearer fake-access-token", "User-Agent": codexUserAgent, "Accept": "application/json", "ChatGPT-Account-Id": "acct-1"} {
			if h.Get(key) != want {
				t.Errorf("request %d %s=%q want=%q", i, key, h.Get(key), want)
			}
		}
		if h.Get("X-Openai-Codex-Luna-Reserve") != "" {
			t.Error("reserve header sent")
		}
	}
}

type usageArchiveTransport func(*http.Request) (*http.Response, error)

func (f usageArchiveTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func TestReadCodexAccountUsageRefusesUntrustedOrigin(t *testing.T) {
	calls := 0
	ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", usageArchiveTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"account_id":"acct-1","plan_type":"pro","available_count":0,"credits":[]}`)), Request: r}, nil
	}))
	a := codexGuardAuth(t, "untrusted-usage-origin", "https://untrusted.example.test", nil)
	if _, e := ReadCodexAccountUsage(ctx, &config.Config{}, a); e == nil {
		t.Error("button read accepted an untrusted origin")
	}
	if calls != 0 {
		t.Errorf("sent account token to untrusted origin: calls=%d", calls)
	}
}
