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

// The capture is the relay record of the Agent SDK session-title request from
// the sdk_ts fixture run before admission, so its status is the refusal; the
// test reads only its headers and body.
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
		{name: "cli entrypoint", edit: func(headers http.Header, body string) string {
			headers.Set("User-Agent", strings.Replace(headers.Get("User-Agent"), "sdk-ts", "cli", 1))
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
// Code 2.1.299 (entrypoint cli) from a run before admission, with the Haiku
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
		{name: "showThinkingSummaries on", edit: func(headers http.Header, body string) string {
			headers.Set("Anthropic-Beta", strings.Replace(headers.Get("Anthropic-Beta"), "redact-thinking-2026-02-12,", "", 1))
			return body
		}, confirmed: true},
		{name: "sdk-ts entrypoint", edit: func(headers http.Header, body string) string {
			headers.Set("User-Agent", strings.Replace(headers.Get("User-Agent"), "(external, cli)", "(external, sdk-ts, agent-sdk/0.3.290)", 1))
			return body
		}},
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
