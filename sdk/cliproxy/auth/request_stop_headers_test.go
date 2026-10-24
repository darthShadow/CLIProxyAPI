package auth

import (
	"net/http"
	"testing"
)

type headerCarryingError struct{ headers http.Header }

func (e headerCarryingError) Error() string { return "upstream 429" }

func (e headerCarryingError) Headers() http.Header { return e.headers }

func TestRelease1RequestStopKeepsUpstreamHeaders(t *testing.T) {
	inner := headerCarryingError{headers: http.Header{"Anthropic-Ratelimit-Unified-Status": []string{"rejected"}}}
	wrapped := wrapRequestStopError(inner)
	carrier, ok := wrapped.(interface{ Headers() http.Header })
	if !ok || carrier.Headers().Get("Anthropic-Ratelimit-Unified-Status") != "rejected" {
		t.Fatalf("stop wrapper %T dropped the upstream headers", wrapped)
	}
	if plain, ok := wrapRequestStopError(http.ErrNoCookie).(interface{ Headers() http.Header }); !ok || plain.Headers() != nil {
		t.Fatal("stop wrapper of a plain error must report no headers")
	}
}
