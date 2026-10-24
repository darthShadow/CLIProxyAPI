package executor

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

// release1RequireDirectBilling checks a byte-identical, request-scoped direct
// response.
func release1RequireDirectBilling(t *testing.T, name string, err error, status int, body string) {
	t.Helper()
	var terminated *cliproxyexecutor.RequestTerminatedError
	if !errors.As(err, &terminated) || terminated == nil {
		t.Fatalf("%s: error = %T %v, want direct upstream response", name, err, err)
	}
	if terminated.HTTPStatus != status || !bytes.Equal(terminated.Body, []byte(body)) {
		t.Fatalf("%s: status %d body %s, want %d and byte-identical %s", name, terminated.HTTPStatus, terminated.Body, status, body)
	}
	if got := terminated.Header.Get("X-Upstream-Marker"); got != "kept" {
		t.Fatalf("%s: X-Upstream-Marker = %q, want kept", name, got)
	}
	var scoped cliproxyexecutor.RequestScopedError
	if !errors.As(err, &scoped) || !scoped.IsRequestScoped() {
		t.Fatalf("%s: error = %T, want request-scoped (no retry, no cooldown)", name, err)
	}
}

func TestRelease1ClaudeCountTokensBillingPassesThrough(t *testing.T) {
	body := `{"type":"error","error":{"type":"billing_error","message":"fixture"},"request_id":"req_fixture","future_error":1}`
	for _, status := range []int{http.StatusPaymentRequired, http.StatusForbidden} {
		upstream := newRelease1ClaudeUpstream(t, status, http.Header{"X-Upstream-Marker": {"kept"}}, body)
		payload := release1ClaudePayload(`{"type":"ephemeral"}`)
		_, errCount := NewClaudeExecutor(release1ClaudeConfig()).countTokensUpstream(context.Background(), release1ClaudeOAuthAuth(upstream.server.URL), cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatClaude,
			OriginalRequest: payload,
			Headers:         release1ClaudeHeaders(release1ClaudeUA),
		})
		release1RequireDirectBilling(t, "count_tokens", errCount, status, body)
	}
}

func TestRelease1CodexBillingErrorsPassThroughUnchanged(t *testing.T) {
	body := `{"error":{"type":"fixture_billing","code":"fixture_code","message":"fixture"},"future_error":1}`
	for _, status := range []int{http.StatusPaymentRequired, http.StatusForbidden} {
		var hits atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.Header().Set("X-Upstream-Marker", "kept")
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}))
		auth := &cliproxyauth.Auth{
			ID:         "release1-codex-billing",
			Provider:   "codex",
			Attributes: map[string]string{"base_url": server.URL},
			Metadata:   map[string]any{"access_token": "fake-access-token", "account_id": "acct-1"},
		}
		payload := []byte(`{"model":"gpt-6.1-sol","max_tokens":10,"messages":[{"role":"user","content":"hi"}]}`)
		request := cliproxyexecutor.Request{Model: "gpt-6.1-sol", Payload: payload}
		opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload}
		executor := NewCodexAutoExecutor(&config.Config{})
		_, errExecute := executor.Execute(context.Background(), auth, request, opts)
		release1RequireDirectBilling(t, "codex Execute", errExecute, status, body)
		_, errStream := executor.ExecuteStream(context.Background(), auth, request, opts)
		release1RequireDirectBilling(t, "codex ExecuteStream", errStream, status, body)
		if got := hits.Load(); got != 2 {
			t.Fatalf("upstream sends = %d, want 2 (one per call, no retry)", got)
		}
		server.Close()
	}
}
