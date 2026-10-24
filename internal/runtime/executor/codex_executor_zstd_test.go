package executor

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
)

func TestCodexExecutorSendsZstdOnExecuteAndStream(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "Execute"
		if stream {
			name = "ExecuteStream"
		}
		t.Run(name, func(t *testing.T) {
			var captured *http.Request
			var encoded []byte
			calls := 0
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				calls++
				captured = req
				var err error
				encoded, err = io.ReadAll(req.Body)
				errClose := req.Body.Close()
				if err != nil || errClose != nil {
					t.Errorf("read/close request: %v / %v", err, errClose)
				}
				body := "data: {\"type\":\"response.completed\",\"response\":{\"id\":\"resp_fake\",\"status\":\"completed\",\"model\":\"gpt-6.1-sol\",\"output\":[],\"usage\":{\"input_tokens\":1,\"output_tokens\":1,\"total_tokens\":2}}}\n\n"
				return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"text/event-stream"}}, Body: io.NopCloser(strings.NewReader(body)), Request: req}, nil
			}))
			auth := &cliproxyauth.Auth{ID: "codex-zstd-" + name, Provider: "codex", Attributes: map[string]string{"base_url": "https://chatgpt.com/backend-api/codex"}, Metadata: map[string]any{"access_token": "fake-access-token"}}
			executor := NewCodexExecutor(&config.Config{})
			req := cliproxyexecutor.Request{Model: "gpt-6.1-sol", Payload: []byte(`{"model":"gpt-6.1-sol","input":[],"prompt_cache_key":"fake-cache-key"}`)}
			opts := cliproxyexecutor.Options{SourceFormat: sdktranslator.FormatOpenAIResponse, Stream: stream}
			if stream {
				result, err := executor.ExecuteStream(ctx, auth, req, opts)
				if err != nil {
					t.Fatal(err)
				}
				for chunk := range result.Chunks {
					if chunk.Err != nil {
						t.Fatal(chunk.Err)
					}
				}
			} else if _, err := executor.Execute(ctx, auth, req, opts); err != nil {
				t.Fatal(err)
			}
			if calls != 1 || captured == nil {
				t.Fatalf("captured requests = %d, want 1", calls)
			}
			if got := captured.Header.Get("Content-Encoding"); got != "zstd" {
				t.Fatalf("captured Content-Encoding = %q, want zstd (body plain JSON = %t)", got, json.Valid(encoded))
			}
			decoder, err := zstd.NewReader(nil)
			if err != nil {
				t.Fatal(err)
			}
			defer decoder.Close()
			plain, err := decoder.DecodeAll(encoded, nil)
			var payload struct {
				Model          string `json:"model"`
				PromptCacheKey string `json:"prompt_cache_key"`
			}
			if err != nil || json.Unmarshal(plain, &payload) != nil || payload.Model != req.Model || payload.PromptCacheKey != "fake-cache-key" {
				t.Fatalf("decoded payload = %s, decode error = %v", plain, err)
			}
			if captured.ContentLength != int64(len(encoded)) {
				t.Errorf("ContentLength = %d, want %d", captured.ContentLength, len(encoded))
			}
		})
	}
}
