package executor

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/signature/signaturetest"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	release1ClaudeUA        = "claude-cli/2.1.285 (external, cli)"
	release1ClaudeSessionID = "11111111-2222-4333-8444-555555555555"
	release1ClaudeUserID    = `{"device_id":"ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff","account_uuid":"","session_id":"11111111-2222-4333-8444-555555555555"}`
	release1ClaudeOKBody    = `{"id":"msg_1","type":"message","role":"assistant","model":"claude-opus-5-5","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn","usage":{"input_tokens":1,"output_tokens":1}}`
)

// release1ClaudeUpstream records every request the executor sends.
type release1ClaudeUpstream struct {
	server  *httptest.Server
	hits    atomic.Int32
	body    []byte
	headers http.Header
}

func newRelease1ClaudeUpstream(t *testing.T, status int, headers http.Header, body string) *release1ClaudeUpstream {
	t.Helper()
	upstream := &release1ClaudeUpstream{}
	upstream.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstream.hits.Add(1)
		upstream.body, _ = io.ReadAll(r.Body)
		upstream.headers = r.Header.Clone()
		for name, values := range headers {
			for _, value := range values {
				w.Header().Add(name, value)
			}
		}
		if w.Header().Get("Content-Type") == "" {
			w.Header().Set("Content-Type", "application/json")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(upstream.server.Close)
	return upstream
}

func release1ClaudeConfig() *config.Config {
	return &config.Config{
		ClaudeHeaderDefaults: config.ClaudeHeaderDefaults{
			UserAgent:      release1ClaudeUA,
			PackageVersion: "0.112.1",
			RuntimeVersion: "v26.3.0",
		},
	}
}

func release1ClaudeOAuthAuth(baseURL string) *cliproxyauth.Auth {
	return &cliproxyauth.Auth{
		ID:       "release1-oauth",
		Provider: "claude",
		Attributes: map[string]string{
			"api_key":  "sk-ant-oat-release1",
			"base_url": baseURL,
		},
		Metadata: claudeOAuthTestMetadata(),
	}
}

// release1ClaudeHeaders is a confirmed native Claude Code 2.1.285 main-thread
// request whose SDK and runtime versions differ from the configured tuple.
func release1ClaudeHeaders(userAgent string) http.Header {
	headers := http.Header{}
	headers.Set("User-Agent", userAgent)
	headers.Set("X-App", "cli")
	headers.Set("Anthropic-Beta", "claude-code-20250219,interleaved-thinking-2025-05-14,context-management-2025-06-27,prompt-caching-scope-2026-01-05")
	headers.Set("Anthropic-Version", "2023-06-01")
	headers.Set("Content-Type", "application/json")
	headers.Set("X-Claude-Code-Session-Id", release1ClaudeSessionID)
	headers.Set("X-Claude-Code-Prompt-Id", "22222222-3333-4444-8555-666666666666")
	headers.Set("X-Cc-Compaction-Request", "1")
	headers.Set("X-Stainless-Lang", "js")
	headers.Set("X-Stainless-Runtime", "node")
	headers.Set("X-Stainless-Package-Version", "0.120.0")
	headers.Set("X-Stainless-Runtime-Version", "v26.9.0")
	headers.Set("X-Stainless-Os", "MacOS")
	headers.Set("X-Stainless-Arch", "arm64")
	headers.Set("X-Stainless-Retry-Count", "0")
	headers.Set("X-Stainless-Timeout", "600")
	return headers
}

// release1ClaudePayload has no billing block, no stream field, and the given
// cache_control object on its only system block.
func release1ClaudePayload(cacheControl string) []byte {
	return []byte(`{"model":"claude-opus-5-5","max_tokens":64000,"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude.","cache_control":` + cacheControl + `}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}],"metadata":{"user_id":"` + strings.ReplaceAll(release1ClaudeUserID, `"`, `\"`) + `"}}`)
}

func release1ClaudeExecute(t *testing.T, cfg *config.Config, baseURL string, headers http.Header, payload []byte) error {
	t.Helper()
	executor := NewClaudeExecutor(cfg)
	_, errExecute := executor.Execute(context.Background(), release1ClaudeOAuthAuth(baseURL), cliproxyexecutor.Request{
		Model:   "claude-opus-5-5",
		Payload: payload,
	}, cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers:         headers,
	})
	return errExecute
}

func TestRelease1ClaudeConfirmedWireKeepsClientShape(t *testing.T) {
	cases := []struct {
		name         string
		cacheControl string
		wantTTLBeta  bool
	}{
		{name: "5-minute marker", cacheControl: `{"type":"ephemeral"}`, wantTTLBeta: false},
		{name: "1-hour marker", cacheControl: `{"type":"ephemeral","ttl":"1h"}`, wantTTLBeta: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newRelease1ClaudeUpstream(t, http.StatusOK, nil, release1ClaudeOKBody)
			if errExecute := release1ClaudeExecute(t, release1ClaudeConfig(), upstream.server.URL, release1ClaudeHeaders(release1ClaudeUA), release1ClaudePayload(tc.cacheControl)); errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}
			if got := gjson.GetBytes(upstream.body, "stream"); got.Exists() {
				t.Errorf("upstream body stream = %s, want field absent: %s", got.Raw, upstream.body)
			}
			if got := gjson.GetBytes(upstream.body, "system.0.text").String(); strings.HasPrefix(got, "x-anthropic-billing-header:") {
				t.Errorf("upstream system[0] = %q, want no proxy billing block", got)
			}
			if got := gjson.GetBytes(upstream.body, "system.#").Int(); got != 1 {
				t.Errorf("upstream system blocks = %d, want 1: %s", got, upstream.body)
			}
			for name, want := range map[string]string{
				"X-Stainless-Package-Version": "0.120.0",
				"X-Stainless-Runtime-Version": "v26.9.0",
				"User-Agent":                  release1ClaudeUA,
				"X-Claude-Code-Prompt-Id":     "22222222-3333-4444-8555-666666666666",
				"X-Cc-Compaction-Request":     "1",
			} {
				if got := upstream.headers.Get(name); got != want {
					t.Errorf("upstream %s = %q, want %q", name, got, want)
				}
			}
			betas := upstream.headers.Get("Anthropic-Beta")
			if got := strings.Contains(betas, claudeExtendedCacheTTLBeta); got != tc.wantTTLBeta {
				t.Errorf("Anthropic-Beta = %q, contains %s = %v, want %v", betas, claudeExtendedCacheTTLBeta, got, tc.wantTTLBeta)
			}
		})
	}
}

func TestRelease1ClaudeBillingErrorsPassThroughUnchanged(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{name: "402 payment required", status: http.StatusPaymentRequired, body: `{"type":"error","error":{"type":"billing_error","message":"Your subscription payment failed."},"request_id":"req_402"}`},
		{name: "403 forbidden", status: http.StatusForbidden, body: `{"type":"error","error":{"type":"permission_error","message":"OAuth authentication is currently not allowed for this organization."},"request_id":"req_403"}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newRelease1ClaudeUpstream(t, tc.status, http.Header{"X-Upstream-Marker": {"kept"}}, tc.body)
			errExecute := release1ClaudeExecute(t, release1ClaudeConfig(), upstream.server.URL, release1ClaudeHeaders(release1ClaudeUA), release1ClaudePayload(`{"type":"ephemeral"}`))
			if got := upstream.hits.Load(); got != 1 {
				t.Fatalf("upstream hits = %d, want 1", got)
			}
			var terminated *cliproxyexecutor.RequestTerminatedError
			if !errors.As(errExecute, &terminated) || terminated == nil {
				t.Fatalf("Execute() error = %T %v, want direct upstream response", errExecute, errExecute)
			}
			if terminated.HTTPStatus != tc.status {
				t.Fatalf("status = %d, want %d", terminated.HTTPStatus, tc.status)
			}
			if !bytes.Equal(terminated.Body, []byte(tc.body)) {
				t.Fatalf("body = %s, want byte-identical %s", terminated.Body, tc.body)
			}
			if got := terminated.Header.Get("X-Upstream-Marker"); got != "kept" {
				t.Fatalf("X-Upstream-Marker = %q, want kept", got)
			}
			var scoped cliproxyexecutor.RequestScopedError
			if !errors.As(errExecute, &scoped) || !scoped.IsRequestScoped() {
				t.Fatalf("Execute() error = %T, want request-scoped (no retry, no cooldown)", errExecute)
			}
		})
	}
}

func TestRelease1ClaudeStreamPathKeepsBillingRules(t *testing.T) {
	payload := []byte(strings.Replace(string(release1ClaudePayload(`{"type":"ephemeral"}`)), `"max_tokens":64000,`, `"max_tokens":64000,"stream":true,`, 1))
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers:         release1ClaudeHeaders(release1ClaudeUA),
	}
	request := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}

	sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	upstream := newRelease1ClaudeUpstream(t, http.StatusOK, http.Header{"Content-Type": {"text/event-stream"}}, sse)
	result, errStream := NewClaudeExecutor(release1ClaudeConfig()).ExecuteStream(context.Background(), release1ClaudeOAuthAuth(upstream.server.URL), request, opts)
	if errStream != nil {
		t.Fatalf("ExecuteStream() error = %v", errStream)
	}
	for range result.Chunks {
	}
	if got := gjson.GetBytes(upstream.body, "system.0.text").String(); strings.HasPrefix(got, "x-anthropic-billing-header:") {
		t.Fatalf("stream upstream system[0] = %q, want no proxy billing block", got)
	}

	billingBody := `{"type":"error","error":{"type":"billing_error","message":"Your subscription payment failed."}}`
	billing := newRelease1ClaudeUpstream(t, http.StatusPaymentRequired, nil, billingBody)
	_, errBilling := NewClaudeExecutor(release1ClaudeConfig()).ExecuteStream(context.Background(), release1ClaudeOAuthAuth(billing.server.URL), request, opts)
	var terminated *cliproxyexecutor.RequestTerminatedError
	if !errors.As(errBilling, &terminated) || terminated.HTTPStatus != http.StatusPaymentRequired || string(terminated.Body) != billingBody {
		t.Fatalf("ExecuteStream() error = %T %v, want the upstream 402 unchanged", errBilling, errBilling)
	}
}

// release1ClaudeThinkingReplayPayload is a confirmed Claude Code turn whose
// history replays a signed thinking block; thinking is the raw thinking field,
// or empty for a body without one.
func release1ClaudeThinkingReplayPayload(thinking string, stream bool) []byte {
	fields := `"model":"claude-opus-5-5","max_tokens":64000,`
	if stream {
		fields += `"stream":true,`
	}
	if thinking != "" {
		fields += `"thinking":` + thinking + `,`
	}
	return []byte(`{` + fields + `"system":[{"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]},{"role":"assistant","content":[{"type":"thinking","thinking":"earlier","signature":"` + preservedThinkingTestSignature + `"},{"type":"text","text":"hi"}]},{"role":"user","content":[{"type":"text","text":"continue"}]}],"metadata":{"user_id":"` + strings.ReplaceAll(release1ClaudeUserID, `"`, `\"`) + `"}}`)
}

func TestRelease1ClaudeConfirmedWireKeepsClientThinking(t *testing.T) {
	sse := "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	for _, tc := range []struct {
		name     string
		thinking string
		stream   bool
	}{
		{name: "adaptive", thinking: `{"type":"adaptive"}`},
		{name: "omitted", thinking: ""},
		{name: "adaptive stream", thinking: `{"type":"adaptive"}`, stream: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload := release1ClaudeThinkingReplayPayload(tc.thinking, tc.stream)
			var upstream *release1ClaudeUpstream
			if tc.stream {
				upstream = newRelease1ClaudeUpstream(t, http.StatusOK, http.Header{"Content-Type": {"text/event-stream"}}, sse)
				result, errStream := NewClaudeExecutor(release1ClaudeConfig()).ExecuteStream(context.Background(), release1ClaudeOAuthAuth(upstream.server.URL), cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}, cliproxyexecutor.Options{
					SourceFormat:    sdktranslator.FormatClaude,
					OriginalRequest: payload,
					Headers:         release1ClaudeHeaders(release1ClaudeUA),
				})
				if errStream != nil {
					t.Fatalf("ExecuteStream() error = %v", errStream)
				}
				for range result.Chunks {
				}
			} else {
				upstream = newRelease1ClaudeUpstream(t, http.StatusOK, nil, release1ClaudeOKBody)
				if errExecute := release1ClaudeExecute(t, release1ClaudeConfig(), upstream.server.URL, release1ClaudeHeaders(release1ClaudeUA), payload); errExecute != nil {
					t.Fatalf("Execute() error = %v", errExecute)
				}
			}
			if got := gjson.GetBytes(upstream.body, "thinking"); got.Raw != tc.thinking {
				t.Errorf("upstream thinking = %q, want the client's %q: %s", got.Raw, tc.thinking, upstream.body)
			}
			if betas := upstream.headers.Get("Anthropic-Beta"); strings.Contains(betas, claudeThinkingBindingBeta) {
				t.Errorf("Anthropic-Beta = %q, want no proxy-added %s", betas, claudeThinkingBindingBeta)
			}
			if !strings.Contains(string(upstream.body), preservedThinkingTestSignature) {
				t.Errorf("upstream body lost the replayed signature: %s", upstream.body)
			}
		})
	}
	for _, tc := range []struct {
		name         string
		model        string
		outputConfig string
	}{
		{name: "opus disabled", model: "claude-opus-5-5"},
		{name: "opus disabled with effort", model: "claude-opus-5-5", outputConfig: `{"effort":"high"}`},
		{name: "sonnet disabled", model: "claude-sonnet-5-5"},
		{name: "sonnet disabled with effort", model: "claude-sonnet-5-5", outputConfig: `{"effort":"xhigh"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"Execute", "ExecuteStream", "countTokensUpstream"} {
				t.Run(path, func(t *testing.T) {
					payload := release1ClaudeThinkingReplayPayload(`{"type":"disabled"}`, path == "ExecuteStream")
					payload = []byte(strings.Replace(string(payload), `"model":"claude-opus-5-5"`, `"model":"`+tc.model+`"`, 1))
					if tc.outputConfig != "" {
						payload = []byte(strings.Replace(string(payload), `"max_tokens":64000,`, `"max_tokens":64000,"output_config":`+tc.outputConfig+`,`, 1))
					}
					upstream := release1ClaudeWireRequest(t, path, release1RefusalConfig(true), release1ClaudeHeaders(release1ClaudeUA), payload, tc.model)
					for _, field := range []string{"thinking", "output_config"} {
						got, want := gjson.GetBytes(upstream.body, field), gjson.GetBytes(payload, field)
						if got.Raw != want.Raw || got.Exists() != want.Exists() {
							t.Errorf("upstream %s = %q (present=%t), want the client's %q (present=%t): %s", field, got.Raw, got.Exists(), want.Raw, want.Exists(), upstream.body)
						}
					}
				})
			}
		})
	}
}

func TestRelease1ClaudeConfirmedWireKeepsUnrecognizedThinking(t *testing.T) {
	unrecognized := signaturetest.ClaudeCAISv4("summary")
	gpt := make([]byte, 73)
	gpt[0] = 0x80
	for _, tc := range []struct {
		name      string
		signature string
		refuse    bool
		userAgent string
		keep      bool
		warn      bool
	}{
		{name: "unrecognized", signature: unrecognized, refuse: true, userAgent: release1ClaudeUA, keep: true},
		{name: "GPT Fernet", signature: base64.URLEncoding.EncodeToString(gpt), refuse: true, userAgent: release1ClaudeUA},
		{name: "empty", refuse: true, userAgent: release1ClaudeUA},
		{name: "unconfirmed", signature: unrecognized, userAgent: "curl/8.9.0"},
		{name: "warn kept unrecognized", signature: unrecognized, refuse: true, userAgent: release1ClaudeUA, warn: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, path := range []string{"Execute", "ExecuteStream", "countTokensUpstream"} {
				t.Run(path, func(t *testing.T) {
					payload := release1ClaudeThinkingReplayPayload(`{"type":"adaptive"}`, path == "ExecuteStream")
					payload = []byte(strings.Replace(string(payload), preservedThinkingTestSignature, tc.signature, 1))
					hook := newSignatureDebugHook(t)
					upstream := release1ClaudeWireRequest(t, path, release1RefusalConfig(tc.refuse), release1ClaudeHeaders(tc.userAgent), payload, "claude-opus-5-5")
					if tc.warn {
						matches := 0
						for _, entry := range hook.AllEntries() {
							if entry.Level != log.WarnLevel || entry.Data["component"] != "signature_sanitizer" || entry.Data["executor"] != "claude" || entry.Data["action"] != "sanitize_claude_messages" {
								continue
							}
							matches++
							if got := entry.Data["kept_unrecognized"]; got != 1 {
								t.Errorf("kept_unrecognized = %v, want 1", got)
							}
							if !strings.Contains(entry.Message, "kept_unrecognized=1") {
								t.Errorf("warn message = %q, want kept_unrecognized=1", entry.Message)
							}
						}
						if matches != 1 {
							t.Errorf("signature sanitizer Warn entries = %d, want 1", matches)
						}
					} else if tc.keep {
						got, want := gjson.GetBytes(upstream.body, "messages").Raw, gjson.GetBytes(payload, "messages").Raw
						if got != want {
							t.Errorf("upstream messages = %s, want the client's %s", got, want)
						}
					} else {
						for _, message := range gjson.GetBytes(upstream.body, "messages").Array() {
							for _, part := range message.Get("content").Array() {
								if part.Get("type").String() == "thinking" {
									t.Errorf("upstream retains a thinking block, want none: %s", upstream.body)
								}
							}
						}
					}
				})
			}
		})
	}
}

func release1ClaudeWireRequest(t *testing.T, path string, cfg *config.Config, headers http.Header, payload []byte, model string) *release1ClaudeUpstream {
	t.Helper()
	responseHeaders := http.Header{}
	responseBody := release1ClaudeOKBody
	if path == "ExecuteStream" {
		responseHeaders.Set("Content-Type", "text/event-stream")
		responseBody = "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"model\":\"claude-opus-5-5\",\"content\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1}}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	} else if path == "countTokensUpstream" {
		responseBody = `{"input_tokens":1}`
	}
	upstream := newRelease1ClaudeUpstream(t, http.StatusOK, responseHeaders, responseBody)
	executor := NewClaudeExecutor(cfg)
	auth := release1ClaudeOAuthAuth(upstream.server.URL)
	request := cliproxyexecutor.Request{Model: model, Payload: payload}
	opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude, OriginalRequest: payload, Headers: headers}
	switch path {
	case "Execute":
		if _, errExecute := executor.Execute(context.Background(), auth, request, opts); errExecute != nil {
			t.Fatalf("Execute() error = %v", errExecute)
		}
	case "ExecuteStream":
		result, errStream := executor.ExecuteStream(context.Background(), auth, request, opts)
		if errStream != nil {
			t.Fatalf("ExecuteStream() error = %v", errStream)
		}
		for chunk := range result.Chunks {
			if chunk.Err != nil {
				t.Fatalf("ExecuteStream() chunk error = %v", chunk.Err)
			}
		}
	case "countTokensUpstream":
		if _, errCount := executor.countTokensUpstream(context.Background(), auth, request, opts); errCount != nil {
			t.Fatalf("countTokensUpstream() error = %v", errCount)
		}
	default:
		t.Fatalf("unknown executor path %q", path)
	}
	if got := upstream.hits.Load(); got != 1 {
		t.Fatalf("upstream hits = %d, want 1", got)
	}
	return upstream
}
