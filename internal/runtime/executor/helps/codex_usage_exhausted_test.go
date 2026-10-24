package helps

import (
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestCodexIncludedUsageExhausted(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	cases := []struct {
		name          string
		quota         cliproxyauth.QuotaState
		wantExhausted bool
		wantUntil     time.Time
	}{
		{name: "no observation", quota: cliproxyauth.QuotaState{}, wantExhausted: false},
		{name: "reset-after seconds from observation", quota: cliproxyauth.QuotaState{ObservedAt: now.Add(-time.Minute), Signals: map[string]string{"X-Codex-Primary-Used-Percent": "100.0", "X-Codex-Primary-Reset-After-Seconds": "600"}}, wantExhausted: true, wantUntil: now.Add(9 * time.Minute)},
		{name: "later of two exhausted windows", quota: cliproxyauth.QuotaState{Signals: map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-At": "1800000600", "X-Codex-Secondary-Used-Percent": "100", "X-Codex-Secondary-Reset-At": "1800086400"}}, wantExhausted: true, wantUntil: time.Unix(1800086400, 0)},
		{name: "exhausted without reset time fails closed", quota: cliproxyauth.QuotaState{Signals: map[string]string{"X-Codex-Secondary-Used-Percent": "100"}}, wantExhausted: true},
		{name: "reset passed", quota: cliproxyauth.QuotaState{Signals: map[string]string{"X-Codex-Primary-Used-Percent": "100", "X-Codex-Primary-Reset-At": "1799999999"}}, wantExhausted: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			until, exhausted := CodexIncludedUsageExhausted(tc.quota, now)
			if exhausted != tc.wantExhausted || !until.Equal(tc.wantUntil) {
				t.Fatalf("CodexIncludedUsageExhausted() = (%v, %v), want (%v, %v)", until, exhausted, tc.wantUntil, tc.wantExhausted)
			}
		})
	}
}
