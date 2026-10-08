package executor

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestKimiExecutorStreamCleanEOFEmitsClaudeTerminalEvents(t *testing.T) {
	for _, upstreamDone := range []bool{false, true} {
		name := "without_done"
		if upstreamDone {
			name = "with_done"
		}
		t.Run(name, func(t *testing.T) {
			stream := "data: " + `{"id":"c1","model":"kimi-k2","choices":[{"index":0,"delta":{"role":"assistant","content":"hello"},"finish_reason":null}]}` + "\n\n" +
				"data: " + `{"id":"c1","model":"kimi-k2","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":1000,"completion_tokens":10}}` + "\n\n" +
				"data: " + `{"id":"c1","model":"kimi-k2","choices":[],"usage":{"prompt_tokens_details":{"cached_tokens":900}}}` + "\n\n"
			if upstreamDone {
				stream += "data: [DONE]\n\n"
			}
			ctx := context.WithValue(context.Background(), "cliproxy.roundtripper", kimiRoundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
					Body:       io.NopCloser(strings.NewReader(stream)),
					Request:    req,
				}, nil
			}))
			payload := []byte(`{"model":"kimi-k2","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
			result, err := NewKimiExecutor(&config.Config{}).ExecuteStream(ctx, nil,
				cliproxyexecutor.Request{Model: "kimi-k2", Payload: payload},
				cliproxyexecutor.Options{
					SourceFormat: sdktranslator.FormatOpenAI, ResponseFormat: sdktranslator.FormatClaude,
					Stream: true, OriginalRequest: payload,
				})
			if err != nil {
				t.Fatalf("ExecuteStream error = %v, want nil", err)
			}
			var events []string
			var finalUsage gjson.Result
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatalf("stream error = %v, want nil", chunk.Err)
				}
				for _, frame := range strings.Split(string(chunk.Payload), "\n\n") {
					if !strings.HasPrefix(frame, "event: ") {
						continue
					}
					lines := strings.SplitN(frame, "\n", 2)
					event := strings.TrimPrefix(lines[0], "event: ")
					events = append(events, event)
					if event == "message_delta" && len(lines) == 2 {
						finalUsage = gjson.Get(strings.TrimPrefix(lines[1], "data: "), "usage")
					}
				}
			}
			for _, terminal := range []string{"message_delta", "message_stop"} {
				count := 0
				for _, event := range events {
					if event == terminal {
						count++
					}
				}
				if count != 1 {
					t.Errorf("%s count = %d, want 1 (events=%v)", terminal, count, events)
				}
			}
			if len(events) < 2 || events[len(events)-2] != "message_delta" || events[len(events)-1] != "message_stop" {
				t.Fatalf("last events = %v, want message_delta then message_stop", events)
			}
			for field, want := range map[string]int64{"input_tokens": 100, "output_tokens": 10, "cache_read_input_tokens": 900} {
				got := finalUsage.Get(field)
				if !got.Exists() || got.Int() != want {
					t.Errorf("usage.%s = %s, want %d", field, got.Raw, want)
				}
			}
		})
	}
}
