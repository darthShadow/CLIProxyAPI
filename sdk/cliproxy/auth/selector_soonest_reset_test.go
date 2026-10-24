package auth

import (
	"context"
	"math"
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

func TestRelease1SoonestResetCodexWeeklyWindowByDuration(t *testing.T) {
	now := time.Now()
	codexAuth := func(id string, signals map[string]string) *Auth {
		auth := &Auth{ID: id, Provider: "codex"}
		auth.Quota.Signals = signals
		auth.Quota.ObservedAt = now
		return auth
	}
	for _, tc := range []struct {
		name       string
		soon, late map[string]string
	}{
		{
			// A plan with only a weekly limit sends it as the primary window.
			name: "weekly primary",
			soon: map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-After-Seconds": "3600"},
			late: map[string]string{"X-Codex-Primary-Window-Minutes": "10080", "X-Codex-Primary-Reset-At": strconv.FormatInt(now.Add(48*time.Hour).Unix(), 10)},
		},
		{
			// A 5-hour primary window is not the weekly one.
			name: "5-hour primary",
			soon: map[string]string{"X-Codex-Primary-Window-Minutes": "300", "X-Codex-Primary-Reset-After-Seconds": "7200", "X-Codex-Secondary-Reset-After-Seconds": "3600"},
			late: map[string]string{"X-Codex-Primary-Window-Minutes": "300", "X-Codex-Primary-Reset-After-Seconds": "60", "X-Codex-Secondary-Reset-After-Seconds": "172800"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selector := &SoonestResetSelector{}
			auths := []*Auth{codexAuth("codex-late", tc.late), codexAuth("codex-soon", tc.soon)}
			for i := 0; i < 3; i++ {
				got, errPick := selector.Pick(context.Background(), "codex", "gpt-6.1-sol", cliproxyexecutor.Options{}, auths)
				if errPick != nil || got == nil || got.ID != "codex-soon" {
					t.Fatalf("Pick %d = %v, %v; want codex-soon", i, got, errPick)
				}
			}
		})
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

func quotaSignalAuth(id string, signals map[string]string) *Auth {
	auth := &Auth{ID: id, Provider: "claude"}
	auth.Quota.Signals = signals
	auth.Quota.ObservedAt = time.Now()
	return auth
}

func unixAfter(now time.Time, d time.Duration) string {
	return strconv.FormatInt(now.Add(d).Unix(), 10)
}

func pickSoonestReset(t *testing.T, auths ...*Auth) string {
	t.Helper()
	got, errPick := (&SoonestResetSelector{}).Pick(context.Background(), "claude", "claude-opus-5-5", cliproxyexecutor.Options{}, auths)
	if errPick != nil || got == nil {
		t.Fatalf("Pick = %v, %v; want a credential", got, errPick)
	}
	return got.ID
}

func TestSoonestResetRanksByWeightedAllowanceLeft(t *testing.T) {
	now := time.Now()
	weekly := func(id string, reset time.Duration, used string, weight string) *Auth {
		auth := quotaSignalAuth(id, map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset":       unixAfter(now, reset),
			"Anthropic-Ratelimit-Unified-7d-Utilization": used,
		})
		if weight != "" {
			auth.Attributes = map[string]string{AttributeWeight: weight}
		}
		return auth
	}
	for _, tc := range []struct {
		name             string
		aReset, bReset   time.Duration
		aUsed, bUsed     string
		aWeight, bWeight string
		want             string
	}{
		{name: "Example, equal plans, 15 min", aReset: 15 * time.Minute, aUsed: "0.91", bReset: 3 * time.Hour, bUsed: "0.20", want: "claude-b"},
		{name: "Example, equal plans, 30 min", aReset: 30 * time.Minute, aUsed: "0.91", bReset: 4 * time.Hour, bUsed: "0.30", want: "claude-b"},
		{name: "Allowance per hour beyond 5 hours", aReset: 20 * time.Hour, aUsed: "0.90", bReset: 60 * time.Hour, bUsed: "0.10", want: "claude-b"},
		{name: "Larger plan, later reset, wins", aReset: 60 * time.Hour, aUsed: "0.70", aWeight: "6", bReset: 20 * time.Hour, bUsed: "0.50", want: "claude-a"},
		{name: "Example, mostly empty on Max 20x (1 : 6)", aReset: 15 * time.Minute, aUsed: "0.91", aWeight: "1", bReset: 3 * time.Hour, bUsed: "0.20", bWeight: "6", want: "claude-b"},
		{name: "Example, near-reset on Max 20x (6 : 1), 30 min", aReset: 30 * time.Minute, aUsed: "0.91", aWeight: "6", bReset: 4 * time.Hour, bUsed: "0.30", bWeight: "1", want: "claude-b"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := weekly("claude-a", tc.aReset, tc.aUsed, tc.aWeight)
			b := weekly("claude-b", tc.bReset, tc.bUsed, tc.bWeight)
			if got := pickSoonestReset(t, a, b); got != tc.want {
				t.Fatalf("Pick = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSoonestResetSkipsSpentFiveHourWindow(t *testing.T) {
	now := time.Now()
	for _, tc := range []struct {
		name     string
		fiveHour map[string]string
		want     string
	}{
		{name: "spent before reset", fiveHour: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.95", "Anthropic-Ratelimit-Unified-5h-Reset": unixAfter(now, time.Hour)}, want: "claude-b"},
		{name: "at the floor", fiveHour: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.90", "Anthropic-Ratelimit-Unified-5h-Reset": unixAfter(now, time.Hour)}, want: "claude-b"},
		{name: "below the floor", fiveHour: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.89", "Anthropic-Ratelimit-Unified-5h-Reset": unixAfter(now, time.Hour)}, want: "claude-a"},
		{name: "window reset passed", fiveHour: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.95", "Anthropic-Ratelimit-Unified-5h-Reset": unixAfter(now, -time.Minute)}, want: "claude-a"},
		{name: "no reset", fiveHour: map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.95"}, want: "claude-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			signals := map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Reset":       unixAfter(now, 10*time.Hour),
				"Anthropic-Ratelimit-Unified-7d-Utilization": "0.00",
			}
			for key, value := range tc.fiveHour {
				signals[key] = value
			}
			bestRate := quotaSignalAuth("claude-a", signals)
			open := quotaSignalAuth("claude-b", map[string]string{
				"Anthropic-Ratelimit-Unified-7d-Reset":       unixAfter(now, 90*time.Hour),
				"Anthropic-Ratelimit-Unified-7d-Utilization": "0.50",
				"Anthropic-Ratelimit-Unified-5h-Utilization": "0.20",
				"Anthropic-Ratelimit-Unified-5h-Reset":       unixAfter(now, 2*time.Hour),
			})
			if got := pickSoonestReset(t, bestRate, open); got != tc.want {
				t.Fatalf("Pick = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSoonestResetAllAccountsOverFloorStillPicks(t *testing.T) {
	now := time.Now()
	a := quotaSignalAuth("claude-a", map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Reset":       unixAfter(now, 10*time.Hour),
		"Anthropic-Ratelimit-Unified-7d-Utilization": "0.95",
		"Anthropic-Ratelimit-Unified-5h-Utilization": "0.95",
		"Anthropic-Ratelimit-Unified-5h-Reset":       unixAfter(now, time.Hour),
	})
	b := quotaSignalAuth("claude-b", map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Reset":       unixAfter(now, 90*time.Hour),
		"Anthropic-Ratelimit-Unified-7d-Utilization": "0.00",
		"Anthropic-Ratelimit-Unified-5h-Utilization": "0.97",
		"Anthropic-Ratelimit-Unified-5h-Reset":       unixAfter(now, 2*time.Hour),
	})
	if got := pickSoonestReset(t, a, b); got != "claude-b" {
		t.Fatalf("Pick = %s, want claude-b", got)
	}
}

func TestSoonestResetNormalizesUtilization(t *testing.T) {
	now := time.Now()
	weekly := func(id string, reset time.Duration, used string) *Auth {
		return quotaSignalAuth(id, map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset":       unixAfter(now, reset),
			"Anthropic-Ratelimit-Unified-7d-Utilization": used,
		})
	}
	for _, tc := range []struct {
		name       string
		used       string
		otherReset time.Duration
		otherUsed  string
		want       string
	}{
		{name: "malformed reads as 0", used: "abc", otherReset: 8 * time.Hour, otherUsed: "0.00", want: "claude-b"},
		{name: "NaN reads as 0", used: "NaN", otherReset: 10 * time.Hour, otherUsed: "0.50", want: "claude-a"},
		{name: "negative reads as 0", used: "-0.5", otherReset: 8 * time.Hour, otherUsed: "0.00", want: "claude-b"},
		{name: "above one reads as 1", used: "1.02", otherReset: 20 * time.Hour, otherUsed: "1.00", want: "claude-a"},
		{name: "infinity reads as 1", used: "+Inf", otherReset: 20 * time.Hour, otherUsed: "1.00", want: "claude-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := pickSoonestReset(t, weekly("claude-a", 10*time.Hour, tc.used), weekly("claude-b", tc.otherReset, tc.otherUsed))
			if got != tc.want {
				t.Fatalf("Pick = %s, want %s", got, tc.want)
			}
		})
	}
}

func TestSoonestResetOldSnapshotStillDecides(t *testing.T) {
	now := time.Now()
	weeklySpent := quotaSignalAuth("claude-a", map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Reset":       unixAfter(now, 10*time.Hour),
		"Anthropic-Ratelimit-Unified-7d-Utilization": "0.95",
	})
	weeklySpent.Quota.ObservedAt = now.Add(-72 * time.Hour)
	weeklyLeft := quotaSignalAuth("claude-b", map[string]string{
		"Anthropic-Ratelimit-Unified-7d-Reset":       unixAfter(now, 60*time.Hour),
		"Anthropic-Ratelimit-Unified-7d-Utilization": "0.10",
	})
	if got := pickSoonestReset(t, weeklySpent, weeklyLeft); got != "claude-b" {
		t.Fatalf("weekly Pick = %s, want claude-b", got)
	}
}

func TestWeeklyQuotaReadsClaudeAndCodex(t *testing.T) {
	observed := time.Unix(1761896220, 0)
	reset := time.Unix(1761900000, 0)
	const claude = "Anthropic-Ratelimit-Unified-7d-Utilization"
	const primary = "X-Codex-Primary-Used-Percent"
	const secondary = "X-Codex-Secondary-Used-Percent"
	for _, tc := range []struct {
		name    string
		signals map[string]string
		used    float64
		reset   time.Time
		signal  string
	}{
		{name: "Claude", signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset": "1761900000", claude: "0.4033",
		}, used: 40.33, reset: reset, signal: claude},
		{name: "Codex weekly primary", signals: map[string]string{
			"X-Codex-Primary-Window-Minutes": " 10080 ", primary: "12.3456789", "X-Codex-Primary-Reset-At": "1761900000",
			secondary: "90", "X-Codex-Secondary-Reset-At": "1762000000",
		}, used: 12.3456789, reset: reset, signal: primary},
		{name: "Codex weekly secondary", signals: map[string]string{
			"X-Codex-Primary-Window-Minutes": "300", primary: "95", "X-Codex-Primary-Reset-After-Seconds": "60",
			"X-Codex-Secondary-Window-Minutes": "10080", secondary: "10", "X-Codex-Secondary-Reset-After-Seconds": "3780",
		}, used: 10, reset: reset, signal: secondary},
		{name: "absolute reset before relative", signals: map[string]string{
			secondary: "20", "X-Codex-Secondary-Reset-At": "1761900000", "X-Codex-Secondary-Reset-After-Seconds": "7200",
		}, used: 20, reset: reset, signal: secondary},
		{name: "malformed absolute reset falls back to relative", signals: map[string]string{
			secondary: "20", "X-Codex-Secondary-Reset-At": "bad", "X-Codex-Secondary-Reset-After-Seconds": "3780",
		}, used: 20, reset: reset, signal: secondary},
		{name: "Claude before Codex", signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset": "1761900000", claude: "0.4033",
			secondary: "90", "X-Codex-Secondary-Reset-At": "1762000000",
		}, used: 40.33, reset: reset, signal: claude},
		{name: "malformed Claude reset falls back to Codex", signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset": "bad", claude: "0.9",
			secondary: "20", "X-Codex-Secondary-Reset-At": "1761900000",
		}, used: 20, reset: reset, signal: secondary},
		{name: "past reset returned", signals: map[string]string{
			secondary: "20", "X-Codex-Secondary-Reset-At": "1761800000", "X-Codex-Secondary-Reset-After-Seconds": "3780",
		}, used: 20, reset: time.Unix(1761800000, 0), signal: secondary},
		{name: "absent"},
		{name: "Claude percent absent", signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset": "1761900000",
		}},
		{name: "Codex percent absent", signals: map[string]string{
			"X-Codex-Secondary-Reset-At": "1761900000",
		}},
		{name: "Claude percent malformed", signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset": "1761900000", claude: "bad",
		}},
		{name: "Codex percent malformed", signals: map[string]string{
			secondary: "bad", "X-Codex-Secondary-Reset-At": "1761900000",
		}},
		{name: "Claude NaN", signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset": "1761900000", claude: "NaN",
		}},
		{name: "Codex NaN", signals: map[string]string{
			secondary: "NaN", "X-Codex-Secondary-Reset-At": "1761900000",
		}},
		{name: "Claude negative clamps to zero", signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset": "1761900000", claude: "-0.5",
		}, reset: reset, signal: claude},
		{name: "Codex negative clamps to zero", signals: map[string]string{
			secondary: "-5", "X-Codex-Secondary-Reset-At": "1761900000",
		}, reset: reset, signal: secondary},
		{name: "Claude above one clamps to 100", signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset": "1761900000", claude: "1.02",
		}, used: 100, reset: reset, signal: claude},
		{name: "Codex above 100 clamps to 100", signals: map[string]string{
			secondary: "102", "X-Codex-Secondary-Reset-At": "1761900000",
		}, used: 100, reset: reset, signal: secondary},
		{name: "Claude positive infinity", signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Reset": "1761900000", claude: "+Inf",
		}, used: 100, reset: reset, signal: claude},
		{name: "Codex positive infinity", signals: map[string]string{
			secondary: "+Inf", "X-Codex-Secondary-Reset-At": "1761900000",
		}, used: 100, reset: reset, signal: secondary},
		{name: "no reset", signals: map[string]string{secondary: "20"}},
		{name: "unparsable reset", signals: map[string]string{
			secondary: "20", "X-Codex-Secondary-Reset-At": "bad", "X-Codex-Secondary-Reset-After-Seconds": "bad",
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			used, reset, signal, ok := WeeklyQuota(QuotaState{Signals: tc.signals, ObservedAt: observed})
			wantOK := tc.signal != ""
			if ok != wantOK || math.IsNaN(used) || math.Abs(used-tc.used) > 1e-9 || !reset.Equal(tc.reset) || signal != tc.signal {
				t.Fatalf("WeeklyQuota = (%v, %v, %q, %v), want (%v, %v, %q, %v)", used, reset, signal, ok, tc.used, tc.reset, tc.signal, wantOK)
			}
		})
	}
}

func TestSoonestResetReadsCodexWeeklyUsed(t *testing.T) {
	reset := strconv.FormatInt(time.Now().Add(48*time.Hour).Unix(), 10)
	auths := []*Auth{
		{ID: "codex-a", Provider: "codex", Quota: QuotaState{Signals: map[string]string{
			"X-Codex-Secondary-Reset-At": reset, "X-Codex-Secondary-Used-Percent": "80",
		}}},
		{ID: "codex-b", Provider: "codex", Quota: QuotaState{Signals: map[string]string{
			"X-Codex-Secondary-Reset-At": reset, "X-Codex-Secondary-Used-Percent": "10",
		}}},
	}
	got, errPick := (&SoonestResetSelector{}).Pick(context.Background(), "codex", "gpt-6.1-sol", cliproxyexecutor.Options{}, auths)
	if errPick != nil || got == nil || got.ID != "codex-b" {
		t.Fatalf("Pick = %v, %v; want codex-b", got, errPick)
	}
}
