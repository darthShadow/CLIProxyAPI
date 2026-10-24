package auth

import (
	"context"
	"net/http"
	"strconv"
	"testing"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func soonestResetAuth(id string, reset time.Time) *Auth {
	auth := &Auth{ID: id, Provider: "claude"}
	if !reset.IsZero() {
		auth.Quota.Signals = map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(reset.Unix(), 10)}
		auth.Quota.ObservedAt = time.Now()
	}
	return auth
}

func TestRelease1SoonestResetPicksEarliestWeeklyReset(t *testing.T) {
	now := time.Now()
	selector := &SoonestResetSelector{}
	auths := []*Auth{
		soonestResetAuth("claude-late", now.Add(96*time.Hour)),
		soonestResetAuth("claude-soon", now.Add(20*time.Hour)),
	}
	for i := 0; i < 3; i++ {
		got, errPick := selector.Pick(context.Background(), "claude", "claude-opus-5-5", cliproxyexecutor.Options{}, auths)
		if errPick != nil || got == nil || got.ID != "claude-soon" {
			t.Fatalf("Pick %d = %v, %v; want claude-soon", i, got, errPick)
		}
	}
}

func TestRelease1SoonestResetLearnsUnknownFirst(t *testing.T) {
	now := time.Now()
	selector := &SoonestResetSelector{}
	auths := []*Auth{
		soonestResetAuth("claude-known", now.Add(20*time.Hour)),
		soonestResetAuth("claude-unknown", time.Time{}),
	}
	got, errPick := selector.Pick(context.Background(), "claude", "claude-opus-5-5", cliproxyexecutor.Options{}, auths)
	if errPick != nil || got == nil || got.ID != "claude-unknown" {
		t.Fatalf("Pick = %v, %v; want claude-unknown", got, errPick)
	}
}

func TestRelease1SoonestResetCodexRelativeReset(t *testing.T) {
	now := time.Now()
	soon := &Auth{ID: "codex-soon", Provider: "codex"}
	soon.Quota.Signals = map[string]string{"X-Codex-Secondary-Reset-After-Seconds": "3600"}
	soon.Quota.ObservedAt = now
	late := &Auth{ID: "codex-late", Provider: "codex"}
	late.Quota.Signals = map[string]string{"X-Codex-Secondary-Reset-At": strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10)}
	late.Quota.ObservedAt = now
	got, errPick := (&SoonestResetSelector{}).Pick(context.Background(), "codex", "gpt-6.1-sol", cliproxyexecutor.Options{}, []*Auth{late, soon})
	if errPick != nil || got == nil || got.ID != "codex-soon" {
		t.Fatalf("Pick = %v, %v; want codex-soon", got, errPick)
	}
}

func TestRelease1SoonestResetNeverMovesBoundSession(t *testing.T) {
	now := time.Now()
	soon := soonestResetAuth("claude-soon", now.Add(20*time.Hour))
	late := soonestResetAuth("claude-late", now.Add(96*time.Hour))
	selector := NewSessionAffinitySelectorWithConfig(SessionAffinityConfig{Fallback: &SoonestResetSelector{}, TTL: time.Hour, Hold: true})
	t.Cleanup(selector.Stop)
	opts := cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": []string{"session-soonest"}}, Metadata: make(map[string]any)}
	first, errFirst := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", opts, []*Auth{late})
	if errFirst != nil || first.ID != "claude-late" {
		t.Fatalf("bind Pick = %v, %v; want claude-late", first, errFirst)
	}
	again, errAgain := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", opts, []*Auth{late, soon})
	if errAgain != nil || again.ID != "claude-late" {
		t.Fatalf("bound session Pick = %v, %v; want it to stay on claude-late", again, errAgain)
	}
	fresh := cliproxyexecutor.Options{Headers: http.Header{"X-Claude-Code-Session-Id": []string{"session-new"}}, Metadata: make(map[string]any)}
	newPick, errNew := selector.Pick(context.Background(), "mixed", "claude-opus-5-5", fresh, []*Auth{late, soon})
	if errNew != nil || newPick.ID != "claude-soon" {
		t.Fatalf("new session Pick = %v, %v; want claude-soon", newPick, errNew)
	}
}
