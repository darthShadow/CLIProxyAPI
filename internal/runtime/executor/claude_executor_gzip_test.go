package executor

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestZeroClaudeBillingCCH(t *testing.T) {
	valid := `{"system":[{"text":"x-anthropic-billing-header: cc_version=2.1.310.test; cch=68a33;"}],"messages":[{"role":"user","content":"keep cch=68a33;"}]}`
	for _, tc := range []struct{ name, body, want string }{
		{"valid", valid, strings.Replace(valid, "cch=68a33", "cch=00000", 1)},
		{"noBilling", `{"messages":[{"content":"cch=68a33;"}]}`, `{"messages":[{"content":"cch=68a33;"}]}`},
		{"malformed", strings.Replace(valid, "cch=68a33", "cch=68a3", 1), strings.Replace(valid, "cch=68a33", "cch=68a3", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := []byte(tc.body)
			if got := zeroClaudeBillingCCH(body); string(got) != tc.want {
				t.Fatalf("reset body = %s, want %s", got, tc.want)
			}
			if string(body) != tc.body {
				t.Fatal("reset mutated the original signed body")
			}
		})
	}
}

func TestDoClaudeUpstreamRequestGzipsSignedBody(t *testing.T) {
	helps.ResetClaudeGzipLatchForTest()
	t.Cleanup(helps.ResetClaudeGzipLatchForTest)
	body := `{"system":[{"text":"x-anthropic-billing-header: cc_version=2.1.310.test; cch=68a33;"}],"prompt":"` + strings.Repeat("x", 6000) + `"}`
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: roundTripperFunc(func(req *http.Request) (*http.Response, error) {
		if got := req.Header.Get("Content-Encoding"); got != "gzip" {
			t.Fatalf("captured Content-Encoding = %q, want gzip", got)
		}
		got := claudeGzipDecodedRequest(t, req)
		want := strings.Replace(body, "cch=68a33", "cch=00000", 1)
		if string(got) != want {
			t.Fatal("decoded request minus padding differs from signed input with zero CCH")
		}
		return &http.Response{StatusCode: 200, Header: http.Header{}, Body: http.NoBody, Request: req}, nil
	})}
	resp, err := doClaudeUpstreamRequest(client, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestClaudeGzipRejectionLatchesAcrossPaths(t *testing.T) {
	for _, path := range []string{"execute", "stream", "count_tokens"} {
		t.Run(path, func(t *testing.T) {
			helps.ResetClaudeGzipLatchForTest()
			t.Cleanup(helps.ResetClaudeGzipLatchForTest)
			const model = "claude-opus-5-5"
			prompt := strings.Repeat("p", 5000)
			body, err := signAnthropicMessagesBody([]byte(`{"model":"` + model + `","system":[{"type":"text","text":"x-anthropic-billing-header: cc_version=2.1.310.test; cch=68a33;"}],"messages":[{"role":"user","content":"` + prompt + `"}],"max_tokens":64,"top_k":5}`))
			if err != nil {
				t.Fatal(err)
			}
			models := []config.PayloadModelRule{{Name: model}}
			cfg := &config.Config{Payload: config.PayloadConfig{
				Override: []config.PayloadRule{{Models: models, Params: map[string]any{"max_tokens": 1234}}},
				Filter:   []config.PayloadFilterRule{{Models: models, Params: []string{"top_k"}}},
			}}
			auth := &cliproxyauth.Auth{Attributes: map[string]string{"api_key": "sk-fake-upstream-1111", "base_url": "https://api.anthropic.com"}}
			const rejection = `{"error":{"type":"invalid_request_error","message":"The request body is not valid JSON: gzip"}}`
			calls := 0
			transport := roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				wantEncoding := "gzip"
				if calls == 2 {
					wantEncoding = ""
				}
				if got := req.Header.Get("Content-Encoding"); got != wantEncoding {
					t.Fatalf("request %d Content-Encoding = %q, want %q", calls, got, wantEncoding)
				}
				decoded := claudeGzipDecodedRequest(t, req)
				if gjson.GetBytes(decoded, "max_tokens").Int() != 1234 || gjson.GetBytes(decoded, "top_k").Exists() {
					t.Fatalf("request %d payload rules lost: max_tokens %s, top_k %s", calls, gjson.GetBytes(decoded, "max_tokens"), gjson.GetBytes(decoded, "top_k"))
				}
				content := gjson.GetBytes(decoded, "messages.0.content")
				if content.IsArray() {
					content = content.Get("0.text")
				}
				if content.String() != prompt {
					t.Fatalf("request %d prompt changed", calls)
				}
				cch := regexp.MustCompile(`cch=([0-9a-f]{5});`).FindSubmatch(decoded)
				if cch == nil || (calls == 1 && string(cch[1]) != "00000") || (calls == 2 && string(cch[1]) == "00000") {
					t.Fatalf("request %d CCH = %q, want zero only in gzip", calls, cch)
				}
				if req.URL.Host != "api.anthropic.com" || (path == "count_tokens") != strings.Contains(req.URL.Path, "count_tokens") {
					t.Fatalf("wrong request origin or path: %s", req.URL)
				}
				return &http.Response{StatusCode: 400, Header: http.Header{"Request-Id": {"req_offline"}, "Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(rejection)), Request: req}, nil
			})
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", http.RoundTripper(transport))
			e := NewClaudeExecutor(cfg)
			req := cliproxyexecutor.Request{Model: model, Payload: body}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatClaude}
			for attempt := 1; attempt <= 2; attempt++ {
				switch path {
				case "execute":
					_, err = e.Execute(ctx, auth, req, opts)
				case "stream":
					_, err = e.ExecuteStream(ctx, auth, req, opts)
				case "count_tokens":
					_, err = e.CountTokens(ctx, auth, req, opts)
				}
				var status interface{ StatusCode() int }
				if !errors.As(err, &status) || status.StatusCode() != 400 || !strings.Contains(err.Error(), rejection) {
					t.Fatalf("caller error = %v, want unchanged 400 body %s", err, rejection)
				}
				if calls != attempt {
					t.Fatalf("round trips = %d, want %d (no resend)", calls, attempt)
				}
			}
		})
	}
}

func claudeGzipDecodedRequest(t *testing.T, req *http.Request) []byte {
	t.Helper()
	var reader io.Reader = req.Body
	if req.Header.Get("Content-Encoding") == "gzip" {
		gz, err := gzip.NewReader(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = gz.Close() })
		reader = gz
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if req.Header.Get("Content-Encoding") == "gzip" {
		end := bytes.LastIndexByte(body, '}') + 1
		if end == 0 || !regexp.MustCompile(`^\n[ \t]{0,256}$`).Match(body[end:]) {
			t.Fatalf("invalid gzip padding after JSON at %d", end)
		}
		body = body[:end]
	}
	return body
}
