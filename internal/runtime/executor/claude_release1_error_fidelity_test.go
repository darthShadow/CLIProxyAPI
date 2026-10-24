package executor

import (
	"net/http"
	"testing"
)

func TestRelease1ClaudeErrorsCarryFilteredUpstreamHeaders(t *testing.T) {
	upstream := http.Header{}
	for _, pair := range [][2]string{
		{"Anthropic-Ratelimit-Unified-Status", "rejected"},
		{"Anthropic-Ratelimit-Unified-5h-Status", "rejected"},
		{"Anthropic-Ratelimit-Unified-5h-Reset", "1800003600"},
		{"Retry-After", "120"},
		{"Request-Id", "req_fixture"},
		{"Content-Length", "99"},
		{"Set-Cookie", "session=secret"},
	} {
		upstream.Set(pair[0], pair[1])
	}
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadRequest, http.StatusInternalServerError} {
		err := classifyClaudeUpstreamErrorWithCooling(status, upstream, []byte(`{"type":"error","error":{"type":"x","message":"y"}}`), false)
		carrier, ok := err.(interface{ Headers() http.Header })
		if !ok {
			t.Fatalf("status %d error %T carries no headers", status, err)
		}
		got := carrier.Headers()
		for _, name := range []string{"Anthropic-Ratelimit-Unified-Status", "Anthropic-Ratelimit-Unified-5h-Reset", "Retry-After", "Request-Id"} {
			if got.Get(name) == "" {
				t.Fatalf("status %d: header %s missing from %v", status, name, got)
			}
		}
		for _, name := range []string{"Content-Length", "Set-Cookie"} {
			if got.Get(name) != "" {
				t.Fatalf("status %d: header %s must not reach the client", status, name)
			}
		}
		if coded, ok := err.(interface{ StatusCode() int }); !ok || coded.StatusCode() != status {
			t.Fatalf("status %d: StatusCode lost on %T", status, err)
		}
	}
}
