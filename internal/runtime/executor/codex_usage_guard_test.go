package executor

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func codexUsageBody(allowed, limitReached bool, resetAt int64, accountID string) string {
	return codexUsageBodyUsed(allowed, limitReached, resetAt, accountID, 10)
}

func codexUsageBodyUsed(allowed, limitReached bool, resetAt int64, accountID string, usedPercent int) string {
	return fmt.Sprintf(`{"plan_type":"pro","rate_limit":{"allowed":%t,"limit_reached":%t,"primary_window":{"used_percent":%d,"limit_window_seconds":18000,"reset_after_seconds":3600,"reset_at":%d}},"account_id":%q}`, allowed, limitReached, usedPercent, resetAt, accountID)
}

// codexUsageServer answers /wham/usage with usage and every other path with
// 503, counting each kind.
type codexUsageServer struct {
	*httptest.Server
	usageStatus int
	usageBody   string
	usageHits   atomic.Int32
	sendHits    atomic.Int32
}

func newCodexUsageServer(t *testing.T, usageStatus int, usageBody string) *codexUsageServer {
	t.Helper()
	s := &codexUsageServer{usageStatus: usageStatus, usageBody: usageBody}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/wham/usage" {
			s.usageHits.Add(1)
			if got := r.Header.Get("Authorization"); got != "Bearer fake-access-token" {
				t.Errorf("usage Authorization = %q", got)
			}
			if got := r.Header.Get("ChatGPT-Account-Id"); got != "acct-1" {
				t.Errorf("usage ChatGPT-Account-Id = %q", got)
			}
			if got := r.Header.Get("User-Agent"); got != codexUserAgent {
				t.Errorf("usage User-Agent = %q, want the executor's Codex identity", got)
			}
			if r.Header.Get("X-Openai-Codex-Luna-Reserve") != "" {
				t.Errorf("usage read sent the reserve header")
			}
			if s.usageStatus == http.StatusTemporaryRedirect {
				http.Redirect(w, r, "/elsewhere", http.StatusTemporaryRedirect)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(s.usageStatus)
			_, _ = w.Write([]byte(s.usageBody))
			return
		}
		s.sendHits.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	t.Cleanup(s.Close)
	return s
}

func codexGuardAuth(t *testing.T, id, baseURL string, signals map[string]string) *cliproxyauth.Auth {
	t.Helper()
	t.Cleanup(func() { codexUsageEntries.Delete(id) })
	return &cliproxyauth.Auth{
		ID:         id,
		Provider:   "codex",
		Attributes: map[string]string{"base_url": baseURL},
		Metadata:   map[string]any{"access_token": "fake-access-token", "refresh_token": "fake-refresh-token", "account_id": "acct-1"},
		Quota:      cliproxyauth.QuotaState{Signals: signals, ObservedAt: time.Now()},
	}
}

func TestCodexAutoExecutorRefusesWhileIncludedUsageIsExhausted(t *testing.T) {
	future := time.Now().Add(time.Hour).Unix()
	futureText := strconv.FormatInt(future, 10)
	pastText := strconv.FormatInt(time.Now().Add(-time.Minute).Unix(), 10)
	allowed := codexUsageBody(true, false, future, "acct-1")
	cases := []struct {
		name        string
		guard       bool
		apiKey      bool
		signals     map[string]string
		usageStatus int
		usageBody   string
		wantSends   int32
		wantReads   int32
	}{
		{name: "observed primary window exhausted", guard: true, signals: map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-At": futureText}, usageStatus: 200, usageBody: allowed, wantSends: 0, wantReads: 0},
		{name: "observed secondary window exhausted", guard: true, signals: map[string]string{"X-Codex-Primary-Used-Percent": "40", "X-Codex-Secondary-Used-Percent": "100", "X-Codex-Secondary-Reset-At": futureText}, usageStatus: 200, usageBody: allowed, wantSends: 0, wantReads: 0},
		{name: "observed window already reset reads usage again", guard: true, signals: map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-At": pastText}, usageStatus: 200, usageBody: allowed, wantSends: 1, wantReads: 1},
		{name: "usage left", guard: true, signals: map[string]string{"X-Codex-Primary-Used-Percent": "99", "X-Codex-Primary-Reset-At": futureText}, usageStatus: 200, usageBody: allowed, wantSends: 1, wantReads: 1},
		{name: "no observation, read allows", guard: true, usageStatus: 200, usageBody: allowed, wantSends: 1, wantReads: 1},
		{name: "no observation, read says limit reached", guard: true, usageStatus: 200, usageBody: codexUsageBody(false, true, future, "acct-1"), wantSends: 0, wantReads: 1},
		{name: "no observation, read fails", guard: true, usageStatus: 500, usageBody: `{}`, wantSends: 0, wantReads: 1},
		{name: "no observation, read has no decision", guard: true, usageStatus: 200, usageBody: `{"plan_type":"pro"}`, wantSends: 0, wantReads: 1},
		{name: "no observation, read for another account", guard: true, usageStatus: 200, usageBody: codexUsageBody(true, false, future, "acct-2"), wantSends: 0, wantReads: 1},
		{name: "no observation, read redirects", guard: true, usageStatus: http.StatusTemporaryRedirect, wantSends: 0, wantReads: 1},
		{name: "guard off", guard: false, signals: map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-At": futureText}, usageStatus: 200, usageBody: allowed, wantSends: 1, wantReads: 0},
		{name: "api-key credential", guard: true, apiKey: true, signals: map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-At": futureText}, usageStatus: 200, usageBody: allowed, wantSends: 1, wantReads: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := newCodexUsageServer(t, tc.usageStatus, tc.usageBody)
			cfg := &config.Config{}
			cfg.Codex.RefuseExhaustedIncludedUsage = tc.guard
			auth := codexGuardAuth(t, "codex-guard-"+tc.name, server.URL, tc.signals)
			if tc.apiKey {
				auth.Attributes["api_key"] = "fake-api-key"
				auth.Metadata = nil
			}
			payload := []byte(`{"model":"gpt-6.1-sol","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
			_, errExecute := NewCodexAutoExecutor(cfg).Execute(context.Background(), auth, cliproxyexecutor.Request{Model: "gpt-6.1-sol", Payload: payload}, cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatClaude,
				OriginalRequest: payload,
			})
			if got := server.sendHits.Load(); got != tc.wantSends {
				t.Fatalf("upstream sends = %d, want %d (err = %v)", got, tc.wantSends, errExecute)
			}
			if got := server.usageHits.Load(); got != tc.wantReads {
				t.Fatalf("usage reads = %d, want %d", got, tc.wantReads)
			}
			if tc.wantSends != 0 {
				return
			}
			var statusErr interface{ StatusCode() int }
			if !errors.As(errExecute, &statusErr) || statusErr.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("Execute() error = %v, want status 429", errExecute)
			}
			var scoped cliproxyexecutor.RequestScopedError
			if !errors.As(errExecute, &scoped) || !scoped.IsRequestScoped() {
				t.Fatalf("Execute() error = %T, want request-scoped refusal", errExecute)
			}
		})
	}
}

func TestCodexIncludedUsageReadsAtStartAndAfterReset(t *testing.T) {
	start := time.Unix(2_000_000_000, 0)
	server := newCodexUsageServer(t, 200, codexUsageBody(true, false, start.Add(time.Hour).Unix(), "acct-1"))
	cfg := &config.Config{}
	cfg.Codex.RefuseExhaustedIncludedUsage = true
	auth := codexGuardAuth(t, "codex-guard-reset", server.URL, nil)
	for _, step := range []struct {
		at        time.Time
		wantReads int32
	}{
		{at: start, wantReads: 1},
		{at: start.Add(30 * time.Second), wantReads: 1},
		{at: start.Add(30 * time.Minute), wantReads: 2},
		{at: start.Add(time.Hour), wantReads: 3},
	} {
		if errAdmit := admitCodexIncludedUsage(context.Background(), cfg, auth, step.at); errAdmit != nil {
			t.Fatalf("admit at %s: %v", step.at, errAdmit)
		}
		if got := server.usageHits.Load(); got != step.wantReads {
			t.Fatalf("usage reads at %s = %d, want %d", step.at, got, step.wantReads)
		}
	}
	server.usageBody = codexUsageBody(false, true, start.Add(2*time.Hour).Unix(), "acct-1")
	if errAdmit := admitCodexIncludedUsage(context.Background(), cfg, auth, start.Add(2*time.Hour+time.Second)); errAdmit == nil {
		t.Fatal("admit after second reset with exhausted usage = nil, want refusal")
	}
}

func TestCodexIncludedUsageConcurrentFirstRequestsShareOneRead(t *testing.T) {
	server := newCodexUsageServer(t, 200, codexUsageBody(true, false, time.Now().Add(time.Hour).Unix(), "acct-1"))
	cfg := &config.Config{}
	cfg.Codex.RefuseExhaustedIncludedUsage = true
	auth := codexGuardAuth(t, "codex-guard-concurrent", server.URL, nil)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if errAdmit := admitCodexIncludedUsage(context.Background(), cfg, auth, time.Now()); errAdmit != nil {
				t.Errorf("admit: %v", errAdmit)
			}
		}()
	}
	wg.Wait()
	if got := server.usageHits.Load(); got != 1 {
		t.Fatalf("usage reads = %d, want 1", got)
	}
}

// Codex CLI shares the account (decision line 33) and spends usage the proxy
// does not see. An allowed read older than codexUsageFreshFor, or at or above
// codexUsageNearLimitPercent in the read or in the proxy's own response
// headers, is read again before the next request is sent.
func TestCodexIncludedUsageRereadsWhenSnapshotMayBeStale(t *testing.T) {
	cfg := &config.Config{}
	cfg.Codex.RefuseExhaustedIncludedUsage = true
	start := time.Unix(2_000_000_000, 0)
	reset := start.Add(3 * time.Hour).Unix()

	t.Run("another client exhausts usage between reads", func(t *testing.T) {
		server := newCodexUsageServer(t, 200, codexUsageBody(true, false, reset, "acct-1"))
		auth := codexGuardAuth(t, "codex-guard-stale-other-client", server.URL, nil)
		if errAdmit := admitCodexIncludedUsage(context.Background(), cfg, auth, start); errAdmit != nil {
			t.Fatalf("first admit: %v", errAdmit)
		}
		if errAdmit := admitCodexIncludedUsage(context.Background(), cfg, auth, start.Add(30*time.Second)); errAdmit != nil {
			t.Fatalf("admit on a fresh read: %v", errAdmit)
		}
		server.usageBody = codexUsageBody(false, true, reset, "acct-1")
		if errAdmit := admitCodexIncludedUsage(context.Background(), cfg, auth, start.Add(codexUsageFreshFor+time.Second)); errAdmit == nil {
			t.Fatal("admit after the read went stale = nil, want refusal from a new read")
		}
		if got := server.usageHits.Load(); got != 2 {
			t.Fatalf("usage reads = %d, want 2", got)
		}
	})

	t.Run("read near the limit is read again on every admission", func(t *testing.T) {
		server := newCodexUsageServer(t, 200, codexUsageBodyUsed(true, false, reset, "acct-1", 95))
		auth := codexGuardAuth(t, "codex-guard-stale-near-limit", server.URL, nil)
		for i := 0; i < 3; i++ {
			if errAdmit := admitCodexIncludedUsage(context.Background(), cfg, auth, start.Add(time.Duration(i)*time.Second)); errAdmit != nil {
				t.Fatalf("admit %d: %v", i, errAdmit)
			}
		}
		if got := server.usageHits.Load(); got != 3 {
			t.Fatalf("usage reads = %d, want 3 (one per admission at 95 percent)", got)
		}
	})

	t.Run("proxy's own headers near the limit force a read", func(t *testing.T) {
		server := newCodexUsageServer(t, 200, codexUsageBody(true, false, reset, "acct-1"))
		auth := codexGuardAuth(t, "codex-guard-stale-observed", server.URL, nil)
		if errAdmit := admitCodexIncludedUsage(context.Background(), cfg, auth, start); errAdmit != nil {
			t.Fatalf("first admit: %v", errAdmit)
		}
		auth.Quota.Signals = map[string]string{"X-Codex-Primary-Used-Percent": "92"}
		if errAdmit := admitCodexIncludedUsage(context.Background(), cfg, auth, start.Add(time.Second)); errAdmit != nil {
			t.Fatalf("second admit: %v", errAdmit)
		}
		if got := server.usageHits.Load(); got != 2 {
			t.Fatalf("usage reads = %d, want 2 (observed 92 percent forces a read)", got)
		}
	})
}
