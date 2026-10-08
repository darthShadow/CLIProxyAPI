package auth

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// sequenceSelector returns auths[n % len(auths)] on its n-th call (0-based), so
// every cold binding gets a different credential than the previous one.
type sequenceSelector struct {
	mu     sync.Mutex
	calls  int
	before func(call int)
}

func (s *sequenceSelector) Pick(_ context.Context, _, _ string, _ cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	s.mu.Lock()
	call := s.calls
	s.calls++
	before := s.before
	s.mu.Unlock()
	if before != nil {
		before(call)
	}
	return auths[call%len(auths)], nil
}

func holdTestAuths() []*Auth {
	return []*Auth{{ID: "claude-a", Provider: "claude"}, {ID: "claude-b", Provider: "claude"}}
}

func holdTestOpts(sessionID, agentID string) cliproxyexecutor.Options {
	headers := http.Header{"X-Claude-Code-Session-Id": []string{sessionID}}
	if agentID != "" {
		headers.Set("X-Claude-Code-Agent-Id", agentID)
	}
	return cliproxyexecutor.Options{Headers: headers, Metadata: make(map[string]any)}
}

func newHoldTestSelector(t *testing.T, hold bool, fallback Selector) *SessionAffinitySelector {
	t.Helper()
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: fallback, TTL: time.Hour, Hold: hold})
	t.Cleanup(selector.Stop)
	return selector
}

func TestSessionAffinityHoldBindsAcrossModels(t *testing.T) {
	selector := newHoldTestSelector(t, true, &sequenceSelector{})
	first, errFirst := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", holdTestOpts("session-models", ""), holdTestAuths())
	if errFirst != nil {
		t.Fatalf("first Pick() error = %v", errFirst)
	}
	second, errSecond := selector.Pick(context.Background(), "mixed", "claude-sonnet-5-5", holdTestOpts("session-models", ""), holdTestAuths())
	if errSecond != nil {
		t.Fatalf("second Pick() error = %v", errSecond)
	}
	if second.ID != first.ID {
		t.Fatalf("second model bound to %s, want %s (one session, one account)", second.ID, first.ID)
	}
}

func TestSessionAffinityHoldReturnsErrorWhileBoundCredentialRests(t *testing.T) {
	cases := []struct {
		name    string
		agentID string
	}{
		{name: "main session"},
		{name: "subagent of the session", agentID: "agent-1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			selector := newHoldTestSelector(t, true, &sequenceSelector{})
			bound, errBind := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", holdTestOpts("session-rest", ""), holdTestAuths())
			if errBind != nil || bound.ID != "claude-a" {
				t.Fatalf("bind Pick() = %v, %v, want claude-a", bound, errBind)
			}
			// The conductor passes only available credentials; claude-a is resting.
			resting := []*Auth{{ID: "claude-b", Provider: "claude"}}
			got, errPick := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", holdTestOpts("session-rest", tc.agentID), resting)
			if errPick == nil {
				t.Fatalf("Pick() = %s, want an error while the bound credential rests", got.ID)
			}
			var authErr *Error
			if !errors.As(errPick, &authErr) || authErr.StatusCode() != http.StatusTooManyRequests {
				t.Fatalf("Pick() error = %v, want status 429", errPick)
			}
		})
	}
}

func TestSessionAffinityHoldKeepsBindingAfterFailure(t *testing.T) {
	selector := newHoldTestSelector(t, true, &sequenceSelector{})
	opts := holdTestOpts("session-failure", "")
	bound, errBind := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", opts, holdTestAuths())
	if errBind != nil {
		t.Fatalf("bind Pick() error = %v", errBind)
	}
	selector.OnResult(Result{AuthID: bound.ID, Provider: "claude", Model: "claude-opus-5-5", Success: false, Error: &Error{Code: "unauthorized", HTTPStatus: http.StatusUnauthorized}, Options: opts})
	again, errAgain := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", holdTestOpts("session-failure", ""), holdTestAuths())
	if errAgain != nil {
		t.Fatalf("Pick() after failure error = %v", errAgain)
	}
	if again.ID != bound.ID {
		t.Fatalf("Pick() after failure = %s, want %s", again.ID, bound.ID)
	}
}

func TestSessionAffinityHoldBindsOnceUnderConcurrency(t *testing.T) {
	nested := make(chan *Auth, 1)
	var selector *SessionAffinitySelector
	fallback := &sequenceSelector{before: func(call int) {
		if call != 0 {
			return
		}
		// A second request of the same session arrives while the first one is
		// still choosing its credential.
		done := make(chan struct{})
		go func() {
			defer close(done)
			auth, _ := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", holdTestOpts("session-race", ""), holdTestAuths())
			nested <- auth
		}()
		select {
		case <-done:
		case <-time.After(100 * time.Millisecond):
		}
	}}
	selector = newHoldTestSelector(t, true, fallback)
	first, errFirst := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", holdTestOpts("session-race", ""), holdTestAuths())
	if errFirst != nil {
		t.Fatalf("Pick() error = %v", errFirst)
	}
	select {
	case got := <-nested:
		if got == nil || got.ID != first.ID {
			t.Fatalf("concurrent Pick() = %v, want %s (first binding wins)", got, first.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("concurrent Pick() did not return")
	}
}
