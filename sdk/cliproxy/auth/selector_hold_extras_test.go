package auth

import (
	"context"
	"testing"
	"time"
)

// Recovered row 109 (fixture F18): resting-session extras for the hold mode.
// Helpers holdTestAuths, holdTestOpts, newHoldTestSelector, and sequenceSelector
// come from selector_hold_test.go (step A4).

func TestSessionAffinityHoldSeparateSessionsKeepTheirOwnAccounts(t *testing.T) {
	selector := newHoldTestSelector(t, true, &sequenceSelector{})
	first, errFirst := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", holdTestOpts("session-x", ""), holdTestAuths())
	second, errSecond := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", holdTestOpts("session-y", ""), holdTestAuths())
	if errFirst != nil || errSecond != nil || first.ID == second.ID {
		t.Fatalf("binds = %v/%v, %v/%v; want two different accounts", first, errFirst, second, errSecond)
	}
	for i := 0; i < 3; i++ {
		againX, _ := selector.Pick(context.Background(), "mixed", "claude-sonnet-5-5", holdTestOpts("session-x", ""), holdTestAuths())
		againY, _ := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", holdTestOpts("session-y", "agent-1"), holdTestAuths())
		if againX == nil || againY == nil || againX.ID != first.ID || againY.ID != second.ID {
			t.Fatalf("round %d: %v, %v; want %s and %s", i, againX, againY, first.ID, second.ID)
		}
	}
}

func TestSessionAffinityHoldKeepsBindingAfterCancel(t *testing.T) {
	selector := newHoldTestSelector(t, true, &sequenceSelector{})
	opts := holdTestOpts("session-cancel", "")
	bound, errBind := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", opts, holdTestAuths())
	if errBind != nil {
		t.Fatalf("bind Pick() error = %v", errBind)
	}
	selector.OnResult(Result{AuthID: bound.ID, Provider: "claude", Model: "claude-opus-5-5", Success: false, Error: &Error{Code: "context_canceled", Message: context.Canceled.Error()}, Options: opts})
	again, errAgain := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", holdTestOpts("session-cancel", ""), holdTestAuths())
	if errAgain != nil || again.ID != bound.ID {
		t.Fatalf("Pick() after cancel = %v, %v; want %s", again, errAgain, bound.ID)
	}
}

func TestSessionAffinityHoldBindingExpiresAfterTTL(t *testing.T) {
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &sequenceSelector{}, TTL: 100 * time.Millisecond, Hold: true})
	t.Cleanup(selector.Stop)
	bound, errBind := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", holdTestOpts("session-expiry", ""), holdTestAuths())
	if errBind != nil {
		t.Fatalf("bind Pick() error = %v", errBind)
	}
	time.Sleep(300 * time.Millisecond)
	again, errAgain := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", holdTestOpts("session-expiry", ""), holdTestAuths())
	if errAgain != nil || again.ID == bound.ID {
		t.Fatalf("Pick() after TTL = %v, %v; want a fresh binding to the next account, not %s", again, errAgain, bound.ID)
	}
}
