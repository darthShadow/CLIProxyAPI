package executor

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func release1RefusalConfig(refuse bool) *config.Config {
	cfg := release1ClaudeConfig()
	cfg.ClaudeCode.RefuseUnconfirmedClients = refuse
	return cfg
}

func TestRelease1ClaudeRefusesUnconfirmedRequestsBeforeUpstream(t *testing.T) {
	cases := []struct {
		name      string
		refuse    bool
		userAgent string
		wantHits  int32
		wantRefus bool
	}{
		{name: "switch on, confirmed at configured version", refuse: true, userAgent: release1ClaudeUA, wantHits: 1},
		{name: "switch on, newer patch version", refuse: true, userAgent: "claude-cli/2.1.286 (external, cli)", wantHits: 0, wantRefus: true},
		{name: "switch on, claude-vscode at configured version", refuse: true, userAgent: "claude-cli/2.1.285 (external, claude-vscode)", wantHits: 0, wantRefus: true},
		{name: "switch on, unknown client", refuse: true, userAgent: "curl/8.9.0", wantHits: 0, wantRefus: true},
		{name: "switch off, unknown client keeps stock path", refuse: false, userAgent: "curl/8.9.0", wantHits: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := newRelease1ClaudeUpstream(t, http.StatusOK, nil, release1ClaudeOKBody)
			errExecute := release1ClaudeExecute(t, release1RefusalConfig(tc.refuse), upstream.server.URL, release1ClaudeHeaders(tc.userAgent), release1ClaudePayload(`{"type":"ephemeral"}`))
			if got := upstream.hits.Load(); got != tc.wantHits {
				t.Fatalf("upstream hits = %d, want %d (err = %v)", got, tc.wantHits, errExecute)
			}
			if !tc.wantRefus {
				if errExecute != nil {
					t.Fatalf("Execute() error = %v, want nil", errExecute)
				}
				return
			}
			if errExecute == nil {
				t.Fatal("Execute() error = nil, want local refusal")
			}
			var statusErr interface{ StatusCode() int }
			if !errors.As(errExecute, &statusErr) || statusErr.StatusCode() != http.StatusBadRequest {
				t.Fatalf("Execute() error = %v, want status 400", errExecute)
			}
			var scoped cliproxyexecutor.RequestScopedError
			if !errors.As(errExecute, &scoped) || !scoped.IsRequestScoped() {
				t.Fatalf("Execute() error = %T, want request-scoped refusal", errExecute)
			}
			if got := errExecute.Error(); got != claudeUnconfirmedClientBody {
				t.Fatalf("refusal body = %s, want %s", got, claudeUnconfirmedClientBody)
			}
		})
	}
}

// A login file can carry cloak_mode in its attributes or in its stored
// metadata (getCloakConfigFromAuth), and that value overrides the global
// cloak switch (resolveClaudeWirePolicy). With cloak_mode "always" and the
// global switch left at its stock value, a confirmed request still reaches the
// upstream uncloaked (no injected fallbacks, the client's one system block),
// and an unconfirmed request gets the local 400 with zero upstream hits.
func TestRelease1ClaudeRefusesUnconfirmedRequestsBeforeUpstreamWithCloakModeAttribute(t *testing.T) {
	plants := []struct {
		where string
		set   func(*cliproxyauth.Auth)
	}{
		{where: "attributes", set: func(auth *cliproxyauth.Auth) { auth.Attributes["cloak_mode"] = "always" }},
		{where: "metadata", set: func(auth *cliproxyauth.Auth) { auth.Metadata["cloak_mode"] = "always" }},
	}
	cases := []struct {
		name      string
		userAgent string
		wantHits  int32
	}{
		{name: "confirmed", userAgent: release1ClaudeUA, wantHits: 1},
		{name: "unconfirmed", userAgent: "curl/8.9.0", wantHits: 0},
	}
	for _, plant := range plants {
		for _, tc := range cases {
			t.Run(plant.where+", "+tc.name, func(t *testing.T) {
				upstream := newRelease1ClaudeUpstream(t, http.StatusOK, nil, release1ClaudeOKBody)
				cfg := release1RefusalConfig(true)
				cfg.DisableClaudeCloakMode = false
				auth := release1ClaudeOAuthAuth(upstream.server.URL)
				plant.set(auth)
				payload := release1ClaudePayload(`{"type":"ephemeral"}`)
				_, errExecute := NewClaudeExecutor(cfg).Execute(context.Background(), auth, cliproxyexecutor.Request{
					Model:   "claude-opus-5-5",
					Payload: payload,
				}, cliproxyexecutor.Options{
					SourceFormat:    sdktranslator.FormatClaude,
					OriginalRequest: payload,
					Headers:         release1ClaudeHeaders(tc.userAgent),
				})
				if got := upstream.hits.Load(); got != tc.wantHits {
					t.Fatalf("upstream hits = %d, want %d (err = %v)", got, tc.wantHits, errExecute)
				}
				if tc.wantHits == 0 {
					var statusErr interface{ StatusCode() int }
					if !errors.As(errExecute, &statusErr) || statusErr.StatusCode() != http.StatusBadRequest {
						t.Fatalf("Execute() error = %v, want local 400", errExecute)
					}
					return
				}
				if errExecute != nil {
					t.Fatalf("Execute() error = %v, want nil", errExecute)
				}
				if got := gjson.GetBytes(upstream.body, "fallbacks"); got.Exists() {
					t.Errorf("upstream fallbacks = %s, want absent (the cloak injects it for claude-opus-5-5)", got.Raw)
				}
				if got := gjson.GetBytes(upstream.body, "system.#").Int(); got != 1 {
					t.Errorf("upstream system blocks = %d, want the client's 1: %s", got, upstream.body)
				}
				if got, want := gjson.GetBytes(upstream.body, "system.0.text").String(), gjson.GetBytes(payload, "system.0.text").String(); got != want {
					t.Errorf("upstream system[0].text = %q, want the client's %q", got, want)
				}
			})
		}
	}
}

func TestRelease1ClaudeStreamAndCountTokensRefuseUnconfirmed(t *testing.T) {
	upstream := newRelease1ClaudeUpstream(t, http.StatusOK, nil, release1ClaudeOKBody)
	executor := NewClaudeExecutor(release1RefusalConfig(true))
	payload := release1ClaudePayload(`{"type":"ephemeral"}`)
	opts := cliproxyexecutor.Options{
		SourceFormat:    sdktranslator.FormatClaude,
		OriginalRequest: payload,
		Headers:         release1ClaudeHeaders("curl/8.9.0"),
	}
	request := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}
	if _, errStream := executor.ExecuteStream(context.Background(), release1ClaudeOAuthAuth(upstream.server.URL), request, opts); errStream == nil {
		t.Fatal("ExecuteStream() error = nil, want local refusal")
	}
	if _, errCount := executor.countTokensUpstream(context.Background(), release1ClaudeOAuthAuth(upstream.server.URL), request, opts); errCount == nil {
		t.Fatal("countTokensUpstream() error = nil, want local refusal")
	}
	if got := upstream.hits.Load(); got != 0 {
		t.Fatalf("upstream hits = %d, want 0", got)
	}
}

// The public CountTokens entry estimates locally for a custom origin (W's
// delegate and any loopback base URL); the refusal must run before that branch.
// HttpRequest must refuse before credentials are attached.
func TestRelease1ClaudePublicCountTokensAndHttpRequestRefuseUnconfirmed(t *testing.T) {
	upstream := newRelease1ClaudeUpstream(t, http.StatusOK, nil, release1ClaudeOKBody)
	executor := NewClaudeExecutor(release1RefusalConfig(true))
	payload := release1ClaudePayload(`{"type":"ephemeral"}`)
	request := cliproxyexecutor.Request{Model: "claude-opus-5-5", Payload: payload}
	auths := map[string]*cliproxyauth.Auth{
		"oauth, custom origin": release1ClaudeOAuthAuth(upstream.server.URL),
		"api-key delegate":     {ID: "release1-delegate", Provider: "claude", Attributes: map[string]string{"api_key": "fake-delegate-key", "base_url": upstream.server.URL}},
	}
	for _, userAgent := range []string{"curl/8.9.0", "claude-cli/2.1.286 (external, cli)", "claude-cli/2.1.280 (external, cli)"} {
		opts := cliproxyexecutor.Options{
			SourceFormat:    sdktranslator.FormatClaude,
			OriginalRequest: payload,
			Headers:         release1ClaudeHeaders(userAgent),
		}
		for name, auth := range auths {
			_, errCount := executor.CountTokens(context.Background(), auth, request, opts)
			var statusErr interface{ StatusCode() int }
			if !errors.As(errCount, &statusErr) || statusErr.StatusCode() != http.StatusBadRequest {
				t.Fatalf("%s, %s: CountTokens() error = %v, want local 400", name, userAgent, errCount)
			}
			httpReq, errNew := http.NewRequest(http.MethodPost, upstream.server.URL+"/v1/messages?beta=true", bytes.NewReader(payload))
			if errNew != nil {
				t.Fatal(errNew)
			}
			httpReq.Header = release1ClaudeHeaders(userAgent)
			resp, errHTTP := executor.HttpRequest(context.Background(), auth, httpReq)
			if resp != nil {
				_ = resp.Body.Close()
			}
			if !errors.As(errHTTP, &statusErr) || statusErr.StatusCode() != http.StatusBadRequest {
				t.Fatalf("%s, %s: HttpRequest() error = %v, want local 400", name, userAgent, errHTTP)
			}
		}
	}
	if got := upstream.hits.Load(); got != 0 {
		t.Fatalf("upstream hits = %d, want 0", got)
	}
}

// A measured helper without the OAuth beta, such as the Agent SDK session title
// that reaches the proxy with the proxy's own key, gets it on an OAuth
// credential, as a confirmed main turn does.
func TestClaudeHelperProfileGetsOAuthBetaOnOAuthCredential(t *testing.T) {
	const titleBetas = "interleaved-thinking-2025-05-14,context-management-2025-06-27,structured-outputs-2025-12-15"
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", titleBetas)
	req := newClaudeHeaderTestRequest(t, nil)
	if errHeaders := applyClaudeHeadersWithNativeProfile(req, claudeOAuthAuthForBetaPolicy(), claudeRaceProbeOAuthKey, false, nil,
		[]byte(`{"model":"claude-haiku-4-5-20251001","tools":[]}`), nil, incoming, true, true); errHeaders != nil {
		t.Fatalf("applyClaudeHeadersWithNativeProfile() error = %v", errHeaders)
	}
	if got, want := req.Header.Get("Anthropic-Beta"), "oauth-2025-04-20,"+titleBetas; got != want {
		t.Fatalf("Anthropic-Beta = %q, want %q", got, want)
	}
}

// Claude Code 2.1.295 sends effort-2025-11-24 with output_config.effort on its
// Haiku 5.5 session title. A confirmed client keeps the beta it sent, although
// the body is a Haiku helper.
func TestClaudeConfirmedClientKeepsItsEffortBeta(t *testing.T) {
	const titleBetas = "interleaved-thinking-2025-05-14,effort-2025-11-24,structured-outputs-2025-12-15"
	incoming := http.Header{}
	incoming.Set("Anthropic-Beta", titleBetas)
	req := newClaudeHeaderTestRequest(t, nil)
	body := []byte(`{"model":"claude-haiku-5-5","system":[{"type":"text","text":"You are naming a coding session."}],` +
		`"output_config":{"effort":"medium","format":{"type":"json_schema","schema":{"type":"object","properties":{"title":{"type":"string"}}}}}}`)
	if errHeaders := applyClaudeHeadersWithNativeProfile(req, &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "fixture-key"}}, "fixture-key",
		false, nil, body, &config.Config{}, incoming, true, true); errHeaders != nil {
		t.Fatalf("applyClaudeHeadersWithNativeProfile() error = %v", errHeaders)
	}
	if got := req.Header.Get("Anthropic-Beta"); got != titleBetas {
		t.Fatalf("Anthropic-Beta = %q, want %q", got, titleBetas)
	}
}
