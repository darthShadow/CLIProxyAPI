//go:build wiregate

package executor

import (
	"context"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/wiregate"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

const release1WireGateClientBilling = "x-anthropic-billing-header: cc_version=2.1.285.abc; cc_entrypoint=cli; cch=00000;"

// TestRelease1WireGateClaudeRemovalsOnTheWire is fixture F36 (recovered row
// 127): the five rewrite removals R1-C3 to R1-C7, observed on the bytes the
// real Claude transport sends toward api.anthropic.com through the wiregate
// seam (recovered row 44).
func TestRelease1WireGateClaudeRemovalsOnTheWire(t *testing.T) {
	cases := []struct {
		name          string
		cacheControl  string
		clientBilling bool
		wantTTLBeta   bool
	}{
		{name: "5-minute marker, no billing block", cacheControl: `{"type":"ephemeral"}`},
		{name: "1-hour marker", cacheControl: `{"type":"ephemeral","ttl":"1h"}`, wantTTLBeta: true},
		{name: "client billing block", cacheControl: `{"type":"ephemeral"}`, clientBilling: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gate := wiregate.Start(t, release1ClaudeOKBody)
			cfg := release1ClaudeConfig()
			cfg.ProxyURL = gate.ProxyURL()
			payload := release1ClaudePayload(tc.cacheControl)
			if tc.clientBilling {
				payload = []byte(strings.Replace(string(payload), `"system":[`, `"system":[{"type":"text","text":"`+release1WireGateClientBilling+`"},`, 1))
			}
			auth := &cliproxyauth.Auth{
				ID:         "release1-wiregate",
				Provider:   "claude",
				Attributes: map[string]string{"api_key": "sk-ant-oat-release1"},
				Metadata:   claudeOAuthTestMetadata(),
			}
			if _, errExecute := NewClaudeExecutor(cfg).Execute(context.Background(), auth, cliproxyexecutor.Request{
				Model:   "claude-opus-5-5",
				Payload: payload,
			}, cliproxyexecutor.Options{
				SourceFormat:    sdktranslator.FormatClaude,
				OriginalRequest: payload,
				Headers:         release1ClaudeHeaders(release1ClaudeUA),
			}); errExecute != nil {
				t.Fatalf("Execute() error = %v", errExecute)
			}
			wire := gate.Next(t)
			for !strings.HasPrefix(wire.RequestLine(), "POST /v1/messages") {
				wire = gate.Next(t)
			}
			if wire.Target != "api.anthropic.com:443" {
				t.Fatalf("CONNECT target = %q, want api.anthropic.com:443", wire.Target)
			}
			// R1-C4: a non-streaming request that omitted stream keeps it omitted.
			if got := gjson.GetBytes(wire.Body, "stream"); got.Exists() {
				t.Errorf("R1-C4: wire body stream = %s, want absent", got.Raw)
			}
			// R1-C3: no proxy billing block; a client block stays and is re-signed.
			first := gjson.GetBytes(wire.Body, "system.0.text").String()
			if tc.clientBilling {
				if !strings.HasPrefix(first, "x-anthropic-billing-header: cc_version=2.1.285.abc;") || strings.Contains(first, "cch=00000;") {
					t.Errorf("R1-C3: wire system[0] = %q, want the client block with a signed cch", first)
				}
				if signed, errSign := signAnthropicMessagesBody(wire.Body); errSign != nil || string(signed) != string(wire.Body) {
					t.Errorf("R1-C3: wire cch does not verify (re-signing changed the body, error %v)", errSign)
				}
			} else if strings.HasPrefix(first, "x-anthropic-billing-header:") || gjson.GetBytes(wire.Body, "system.#").Int() != 1 {
				t.Errorf("R1-C3: wire system = %s, want the client's single block and no proxy billing block", gjson.GetBytes(wire.Body, "system").Raw)
			}
			// R1-C5 and R1-C7: client SDK versions and both hint headers on the wire.
			for name, want := range map[string]string{
				"X-Stainless-Package-Version": "0.120.0",
				"X-Stainless-Runtime-Version": "v26.9.0",
				"X-Claude-Code-Prompt-Id":     "22222222-3333-4444-8555-666666666666",
				"X-Cc-Compaction-Request":     "1",
			} {
				if got := wire.Get(name); got != want {
					t.Errorf("wire %s = %q, want %q", name, got, want)
				}
			}
			// R1-C6: the 1-hour TTL beta only with a 1-hour marker.
			if got := strings.Contains(wire.Get("anthropic-beta"), claudeExtendedCacheTTLBeta); got != tc.wantTTLBeta {
				t.Errorf("R1-C6: wire anthropic-beta = %q, contains %s = %v, want %v", wire.Get("anthropic-beta"), claudeExtendedCacheTTLBeta, got, tc.wantTTLBeta)
			}
		})
	}
}
