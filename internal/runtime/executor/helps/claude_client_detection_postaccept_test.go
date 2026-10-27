package helps

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/tidwall/sjson"
)

func TestDetectClaudeCodeRequestAdmitsCronWorkload(t *testing.T) {
	payload := claudeCodeDetectionPayload(validClaudeCodeMetadataUserID)
	for _, test := range []struct {
		name      string
		userAgent string
		confirmed bool
	}{
		{name: "cli cron", userAgent: "claude-cli/2.1.280 (external, cli, workload/cron)", confirmed: true},
		{name: "other workload", userAgent: "claude-cli/2.1.280 (external, cli, workload/batch)", confirmed: false},
		{name: "cron then client-app", userAgent: "claude-cli/2.1.280 (external, cli, workload/cron, client-app/x/1.0)", confirmed: false},
		{name: "sdk-py cron stays refused", userAgent: "claude-cli/2.1.280 (external, sdk-py, agent-sdk/0.1.0, workload/cron)", confirmed: false},
		{name: "sdk-ts cron", userAgent: "claude-cli/2.1.280 (external, sdk-ts, agent-sdk/0.3.290, workload/cron)", confirmed: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := confirmedClaudeCodeHeaders()
			headers.Set("User-Agent", test.userAgent)
			if got := DetectClaudeCodeRequest(headers, payload, false).Confirmed; got != test.confirmed {
				t.Fatalf("Confirmed = %t, want %t", got, test.confirmed)
			}
		})
	}
}

// The capture is the relay record of the Claude Code 2.1.311 Agent SDK
// session-title request from the sdk_ts fixture run before admission, so its
// status is the refusal; the test reads only its headers and body.
func TestDetectClaudeCodeRequestConfirmsSDKTitle(t *testing.T) {
	raw, errRead := os.ReadFile("testdata/sdk_ts_title.json")
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	var record struct {
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if errDecode := json.Unmarshal(raw, &record); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v", errDecode)
	}
	setBody := func(path string, value any) func(http.Header, string) string {
		return func(_ http.Header, body string) string {
			edited, _ := sjson.Set(body, path, value)
			return edited
		}
	}
	for _, test := range []struct {
		name      string
		edit      func(http.Header, string) string
		confirmed bool
	}{
		{name: "captured", edit: func(_ http.Header, body string) string { return body }, confirmed: true},
		{name: "2.1.310 betas", edit: func(headers http.Header, body string) string {
			headers.Set("Anthropic-Beta", "context-1m-2025-08-07,interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13,context-management-2025-06-27,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,effort-2025-11-24,structured-outputs-2025-12-15")
			return body
		}},
		{name: "retry without thinking-token-count", edit: func(headers http.Header, body string) string {
			headers.Set("Anthropic-Beta", strings.Replace(headers.Get("Anthropic-Beta"), "thinking-token-count-2026-05-13,", "", 1))
			return body
		}},
		{name: "cli entrypoint", edit: func(headers http.Header, body string) string {
			headers.Set("User-Agent", strings.Replace(headers.Get("User-Agent"), "sdk-ts", "cli", 1))
			return body
		}},
		{name: "shadow-code", edit: func(headers http.Header, body string) string {
			headers.Set("User-Agent", strings.Replace(headers.Get("User-Agent"), "sdk-ts", "shadow-code", 1))
			return body
		}, confirmed: true},
		{name: "t3-code", edit: func(headers http.Header, body string) string {
			headers.Set("User-Agent", strings.Replace(headers.Get("User-Agent"), "sdk-ts", "t3-code", 1))
			return body
		}, confirmed: true},
		{name: "shadow-code redact beta", edit: func(headers http.Header, body string) string {
			headers.Set("User-Agent", strings.Replace(headers.Get("User-Agent"), "sdk-ts", "shadow-code", 1))
			headers.Set("Anthropic-Beta", strings.Replace(headers.Get("Anthropic-Beta"), "interleaved-thinking-2025-05-14,", "interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,", 1))
			return body
		}},
		{name: "t3-code redact beta", edit: func(headers http.Header, body string) string {
			headers.Set("User-Agent", strings.Replace(headers.Get("User-Agent"), "sdk-ts", "t3-code", 1))
			headers.Set("Anthropic-Beta", strings.Replace(headers.Get("Anthropic-Beta"), "interleaved-thinking-2025-05-14,", "interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,", 1))
			return body
		}},
		{name: "beta dropped", edit: func(headers http.Header, body string) string {
			betas := strings.Split(headers.Get("Anthropic-Beta"), ",")
			headers.Set("Anthropic-Beta", strings.Join(betas[:len(betas)-1], ","))
			return body
		}},
		{name: "redact beta added", edit: func(headers http.Header, body string) string {
			headers.Set("Anthropic-Beta", strings.Replace(headers.Get("Anthropic-Beta"), "interleaved-thinking-2025-05-14,", "interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,", 1))
			return body
		}},
		{name: "other model", edit: setBody("model", "claude-opus-5-5")},
		{name: "CLI identity", edit: setBody("system.0.text", "You are Claude Code, Anthropic's official CLI for Claude.")},
		{name: "extra field", edit: setBody("fixture_extra", true)},
		{name: "number text", edit: setBody("messages.0.content.0.text", 1)},
		{name: "boolean text", edit: setBody("system.1.text", false)},
		{name: "object text", edit: setBody("messages.0.content.0.text", map[string]any{"text": "fixture"})},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := http.Header{}
			for name, value := range record.Headers {
				headers.Set(name, value)
			}
			body := test.edit(headers, record.Body)
			if got := DetectClaudeCodeRequest(headers, []byte(body), false).Confirmed; got != test.confirmed {
				t.Fatalf("Confirmed = %t, want %t", got, test.confirmed)
			}
		})
	}
}

// The capture is the relay record of the session title of interactive Claude
// Code 2.1.311 (entrypoint cli) from a run before admission, with the Haiku
// model of the shared settings and showThinkingSummaries unset; the test reads
// only its headers and body.
func TestDetectClaudeCodeRequestConfirmsCLITitle(t *testing.T) {
	raw, errRead := os.ReadFile("testdata/cli_title.json")
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	var record struct {
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if errDecode := json.Unmarshal(raw, &record); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v", errDecode)
	}
	setBody := func(path string, value any) func(http.Header, string) string {
		return func(_ http.Header, body string) string {
			edited, _ := sjson.Set(body, path, value)
			return edited
		}
	}
	for _, test := range []struct {
		name      string
		edit      func(http.Header, string) string
		confirmed bool
	}{
		{name: "captured", edit: func(_ http.Header, body string) string { return body }, confirmed: true},
		{name: "2.1.310 betas", edit: func(headers http.Header, body string) string {
			headers.Set("Anthropic-Beta", "context-1m-2025-08-07,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,effort-2025-11-24,structured-outputs-2025-12-15")
			return body
		}},
		{name: "retry without thinking-token-count", edit: func(headers http.Header, body string) string {
			headers.Set("Anthropic-Beta", strings.Replace(headers.Get("Anthropic-Beta"), "thinking-token-count-2026-05-13,", "", 1))
			return body
		}},
		{name: "showThinkingSummaries on", edit: func(headers http.Header, body string) string {
			headers.Set("Anthropic-Beta", strings.Replace(headers.Get("Anthropic-Beta"), "redact-thinking-2026-02-12,", "", 1))
			return body
		}, confirmed: true},
		{name: "sdk-ts entrypoint", edit: func(headers http.Header, body string) string {
			headers.Set("User-Agent", strings.Replace(headers.Get("User-Agent"), "(external, cli)", "(external, sdk-ts, agent-sdk/0.3.290)", 1))
			return body
		}},
		{name: "shadow-code", edit: func(headers http.Header, body string) string {
			headers.Set("User-Agent", strings.Replace(headers.Get("User-Agent"), "(external, cli)", "(external, shadow-code)", 1))
			return body
		}, confirmed: true},
		{name: "t3-code", edit: func(headers http.Header, body string) string {
			headers.Set("User-Agent", strings.Replace(headers.Get("User-Agent"), "(external, cli)", "(external, t3-code)", 1))
			return body
		}, confirmed: true},
		{name: "Agent SDK identity", edit: setBody("system.0.text", "You are a Claude agent, built on Anthropic's Claude Agent SDK.")},
		{name: "beta dropped", edit: func(headers http.Header, body string) string {
			betas := strings.Split(headers.Get("Anthropic-Beta"), ",")
			headers.Set("Anthropic-Beta", strings.Join(betas[:len(betas)-1], ","))
			return body
		}},
		{name: "other model", edit: setBody("model", "claude-opus-5-5")},
		{name: "extra field", edit: setBody("fixture_extra", true)},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := http.Header{}
			for name, value := range record.Headers {
				headers.Set(name, value)
			}
			body := test.edit(headers, record.Body)
			if got := DetectClaudeCodeRequest(headers, []byte(body), false).Confirmed; got != test.confirmed {
				t.Fatalf("Confirmed = %t, want %t", got, test.confirmed)
			}
		})
	}
}

// The capture is the relay record of the Claude Code 2.1.311 Agent SDK
// (sdk-ts) WebFetch page-summary request before admission, so its status is
// the refusal; the test reads only its headers and body.
func TestDetectClaudeCodeRequestConfirmsSDKWebFetch(t *testing.T) {
	raw, errRead := os.ReadFile("testdata/sdk_ts_web_fetch.json")
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	var record struct {
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if errDecode := json.Unmarshal(raw, &record); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v", errDecode)
	}
	setBody := func(path string, value any) func(string) string {
		return func(body string) string {
			edited, errEdit := sjson.Set(body, path, value)
			if errEdit != nil {
				t.Fatalf("Set(%q) error = %v", path, errEdit)
			}
			return edited
		}
	}
	for _, test := range []struct {
		name      string
		userAgent string
		betas     string
		retry     bool
		edit      func(string) string
		confirmed bool
	}{
		{name: "sdk-ts first try", confirmed: true},
		{name: "sdk-ts retry", retry: true, confirmed: true},
		{name: "sdk-cli first try", userAgent: "claude-cli/2.1.311 (external, sdk-cli)", confirmed: true},
		{name: "sdk-cli retry", userAgent: "claude-cli/2.1.311 (external, sdk-cli)", retry: true, confirmed: true},
		{name: "t3-code first try", userAgent: strings.Replace(record.Headers["User-Agent"], "sdk-ts", "t3-code", 1), confirmed: true},
		{name: "t3-code retry", userAgent: strings.Replace(record.Headers["User-Agent"], "sdk-ts", "t3-code", 1), retry: true, confirmed: true},
		{name: "t3-code -p first try", userAgent: "claude-cli/2.1.311 (external, t3-code)", confirmed: true},
		{name: "t3-code -p retry", userAgent: "claude-cli/2.1.311 (external, t3-code)", retry: true, confirmed: true},
		{name: "shadow-code", userAgent: strings.Replace(record.Headers["User-Agent"], "sdk-ts", "shadow-code", 1), confirmed: true},
		{name: "cli entrypoint", userAgent: "claude-cli/2.1.311 (external, cli)"},
		{name: "sdk-py entrypoint", userAgent: strings.Replace(record.Headers["User-Agent"], "sdk-ts", "sdk-py", 1)},
		{name: "structured-outputs beta", betas: "interleaved-thinking-2025-05-14,context-management-2025-06-27,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,thinking-token-count-2026-05-13,effort-2025-11-24,structured-outputs-2025-12-15"},
		{name: "redact beta", betas: strings.Replace(record.Headers["anthropic-beta"], "interleaved-thinking-2025-05-14,", "interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,", 1)},
		{name: "t3-code redact beta", userAgent: strings.Replace(record.Headers["User-Agent"], "sdk-ts", "t3-code", 1), betas: strings.Replace(record.Headers["anthropic-beta"], "interleaved-thinking-2025-05-14,", "interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,", 1)},
		{name: "CLI identity", edit: setBody("system.0.text", claudeCodeCLIIdentity)},
		{name: "output format", edit: setBody("output_config.format", map[string]any{"type": "json_schema", "schema": map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}, "required": []string{"title"}, "additionalProperties": false}})},
		{name: "other effort", edit: setBody("output_config.effort", "high")},
		{name: "tools added", edit: setBody("tools", []map[string]string{{"name": "fixture"}})},
		{name: "extra system block", edit: setBody("system.1", map[string]string{"type": "text", "text": "fixture"})},
		{name: "other model", edit: setBody("model", "claude-opus-5-5")},
		{name: "extra field", edit: setBody("fixture_extra", true)},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := http.Header{}
			for name, value := range record.Headers {
				headers.Set(name, value)
			}
			if test.userAgent != "" {
				headers.Set("User-Agent", test.userAgent)
			}
			if test.betas != "" {
				headers.Set("Anthropic-Beta", test.betas)
			}
			if test.retry {
				headers.Set("Anthropic-Beta", strings.Replace(headers.Get("Anthropic-Beta"), "thinking-token-count-2026-05-13,", "", 1))
			}

			body := record.Body
			if test.edit != nil {
				body = test.edit(body)
			}
			if got := DetectClaudeCodeRequest(headers, []byte(body), false).Confirmed; got != test.confirmed {
				t.Fatalf("Confirmed = %t, want %t", got, test.confirmed)
			}
		})
	}
}

// The capture is the relay record of the interactive Claude Code 2.1.311 (cli)
// WebFetch page-summary request before admission, so its status is the
// refusal; the test reads only its headers and body.
func TestDetectClaudeCodeRequestConfirmsCLIWebFetch(t *testing.T) {
	raw, errRead := os.ReadFile("testdata/cli_web_fetch.json")
	if errRead != nil {
		t.Fatalf("ReadFile() error = %v", errRead)
	}
	var record struct {
		Headers map[string]string `json:"headers"`
		Body    string            `json:"body"`
	}
	if errDecode := json.Unmarshal(raw, &record); errDecode != nil {
		t.Fatalf("Unmarshal() error = %v", errDecode)
	}
	setBody := func(path string, value any) func(string) string {
		return func(body string) string {
			edited, errEdit := sjson.Set(body, path, value)
			if errEdit != nil {
				t.Fatalf("Set(%q) error = %v", path, errEdit)
			}
			return edited
		}
	}
	for _, test := range []struct {
		name              string
		userAgent         string
		betas             string
		retry             bool
		thinkingSummaries bool
		edit              func(string) string
		confirmed         bool
	}{
		{name: "cli first try", confirmed: true},
		{name: "cli retry", retry: true, confirmed: true},
		{name: "t3-code first try", userAgent: "claude-cli/2.1.311 (external, t3-code)", confirmed: true},
		{name: "t3-code retry", userAgent: "claude-cli/2.1.311 (external, t3-code)", retry: true, confirmed: true},
		{name: "shadow-code", userAgent: "claude-cli/2.1.311 (external, shadow-code)", confirmed: true},
		{name: "showThinkingSummaries on", thinkingSummaries: true, confirmed: true},
		{name: "showThinkingSummaries on retry", thinkingSummaries: true, retry: true, confirmed: true},
		{name: "t3-code showThinkingSummaries on", userAgent: "claude-cli/2.1.311 (external, t3-code)", thinkingSummaries: true, confirmed: true},
		{name: "t3-code showThinkingSummaries on retry", userAgent: "claude-cli/2.1.311 (external, t3-code)", thinkingSummaries: true, retry: true, confirmed: true},
		{name: "sdk-ts entrypoint", userAgent: "claude-cli/2.1.311 (external, sdk-ts, agent-sdk/0.3.290)"},
		{name: "sdk-cli entrypoint", userAgent: "claude-cli/2.1.311 (external, sdk-cli)"},
		{name: "Agent SDK identity", edit: setBody("system.0.text", claudeCodeSDKIdentity)},
		{name: "structured-outputs beta", betas: "interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,thinking-token-count-2026-05-13,effort-2025-11-24,structured-outputs-2025-12-15"},
		{name: "output format", edit: setBody("output_config.format", map[string]any{"type": "json_schema", "schema": map[string]any{"type": "object", "properties": map[string]any{"title": map[string]any{"type": "string"}}, "required": []string{"title"}, "additionalProperties": false}})},
		{name: "other model", edit: setBody("model", "claude-opus-5-5")},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := http.Header{}
			for name, value := range record.Headers {
				headers.Set(name, value)
			}
			if test.userAgent != "" {
				headers.Set("User-Agent", test.userAgent)
			}
			if test.betas != "" {
				headers.Set("Anthropic-Beta", test.betas)
			}
			if test.retry {
				headers.Set("Anthropic-Beta", strings.Replace(headers.Get("Anthropic-Beta"), "thinking-token-count-2026-05-13,", "", 1))
			}
			if test.thinkingSummaries {
				headers.Set("Anthropic-Beta", strings.Replace(headers.Get("Anthropic-Beta"), "redact-thinking-2026-02-12,", "", 1))
			}
			body := record.Body
			if test.edit != nil {
				body = test.edit(body)
			}
			if got := DetectClaudeCodeRequest(headers, []byte(body), false).Confirmed; got != test.confirmed {
				t.Fatalf("Confirmed = %t, want %t", got, test.confirmed)
			}
		})
	}
}
