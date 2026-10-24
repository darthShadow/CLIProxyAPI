package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestNormalizedRoutingRuntimeStateCarriesSessionAffinityHold(t *testing.T) {
	cases := []struct {
		name     string
		affinity bool
		hold     bool
		want     bool
	}{
		{name: "hold with affinity", affinity: true, hold: true, want: true},
		{name: "hold ignored without affinity", affinity: false, hold: true, want: false},
		{name: "affinity without hold", affinity: true, hold: false, want: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Routing.SessionAffinity = tc.affinity
			cfg.Routing.SessionAffinityHold = tc.hold
			if got := normalizedRoutingRuntimeState(cfg).sessionAffinityHold; got != tc.want {
				t.Fatalf("sessionAffinityHold = %v, want %v", got, tc.want)
			}
			selector := newRoutingSelector(normalizedRoutingRuntimeState(cfg))
			if affinity, ok := selector.(*coreauth.SessionAffinitySelector); ok {
				t.Cleanup(affinity.Stop)
			} else if tc.affinity {
				t.Fatalf("selector = %T, want *SessionAffinitySelector", selector)
			}
			opts := cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": []string{"hold-wiring-session"}}, Metadata: map[string]any{}}
			first := &coreauth.Auth{ID: "hold-wiring-a", Provider: "claude"}
			second := &coreauth.Auth{ID: "hold-wiring-b", Provider: "claude"}
			bound, errBind := selector.Pick(context.Background(), "claude", "hold-wiring-model", opts, []*coreauth.Auth{first})
			if errBind != nil || bound == nil || bound.ID != first.ID {
				t.Fatalf("initial Pick() = %v, %v, want account %s", bound, errBind, first.ID)
			}
			got, errPick := selector.Pick(context.Background(), "claude", "hold-wiring-model", opts, []*coreauth.Auth{second})
			if tc.want {
				var authErr *coreauth.Error
				if got != nil || !errors.As(errPick, &authErr) || authErr.StatusCode() != http.StatusTooManyRequests {
					t.Fatalf("held Pick() = %v, %v, want no account and status 429", got, errPick)
				}
			} else if errPick != nil || got == nil || got.ID != second.ID {
				t.Fatalf("unheld Pick() = %v, %v, want account %s", got, errPick, second.ID)
			}
		})
	}
}
