package auth

import (
	"context"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// SoonestResetSelector ranks the credentials it is asked to pick from by
// weight times weekly share left per hour until the weekly reset; under 5
// hours counts as 5 hours.
// A Claude credential whose 5-hour window is at least 90 percent used with
// its reset ahead is skipped while another credential is below the floor.
// Credentials with an unknown weekly reset are picked first.
// Ties and unknown credentials use round robin.
// This selector never moves a bound session.
type SoonestResetSelector struct {
	rr RoundRobinSelector
}

const (
	// soonestResetFiveHours is the length of the Claude 5-hour window. The rank
	// divides by at least this time: a session cannot spend weekly allowance
	// faster than this window allows, so credentials whose weekly reset is
	// nearer rank by weekly allowance left alone.
	soonestResetFiveHours = 5 * time.Hour
	// soonestResetFiveHourFloor is the 5-hour used share at or above which a
	// credential is skipped while another credential is below it: a new session
	// there would soon meet the 5-hour limit. The Codex usage guard treats 90
	// percent as near the limit too. The floor is a routing preference only: it
	// creates no cooldown or refusal.
	soonestResetFiveHourFloor = 0.9
)

// Pick implements Selector.
func (s *SoonestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	var open []*Auth
	for _, auth := range auths {
		if auth != nil && !fiveHourSpent(auth.Quota, now) {
			open = append(open, auth)
		}
	}
	if len(open) == 0 {
		open = auths
	}
	var unknown, best []*Auth
	bestRate := -1.0
	for _, auth := range open {
		if auth == nil {
			continue
		}
		rate, ok := weeklyRate(auth, now)
		switch {
		case !ok:
			unknown = append(unknown, auth)
		case rate > bestRate:
			best, bestRate = []*Auth{auth}, rate
		case rate == bestRate:
			best = append(best, auth)
		}
	}
	candidates := open
	switch {
	case len(unknown) > 0:
		candidates = unknown
	case len(best) > 0:
		candidates = best
	}
	return s.rr.Pick(ctx, provider, model, opts, candidates)
}

// weeklyResetAt reads the weekly window reset from the last passive quota
// snapshot: Claude "anthropic-ratelimit-unified-7d-reset" and Codex
// "x-codex-secondary-reset-at" (Unix seconds), or Codex
// "x-codex-secondary-reset-after-seconds" from the snapshot time.
func weeklyResetAt(auth *Auth, now time.Time) (time.Time, bool) {
	for _, name := range []string{"Anthropic-Ratelimit-Unified-7d-Reset", "X-Codex-Secondary-Reset-At"} {
		if at := auth.Quota.SignalUnixTime(name); at.After(now) {
			return at, true
		}
	}
	if at := auth.Quota.SignalResetAfter("X-Codex-Secondary-Reset-After-Seconds"); at.After(now) {
		return at, true
	}
	return time.Time{}, false
}

// weeklyRate is the weekly allowance left per hour until the weekly reset: the
// share left times the credential's weight (authWeight: the "weight" field of
// its auth file, 1 when absent), divided by the hours to the reset raised to
// at least soonestResetFiveHours. Only Claude utilization is read; a Codex
// credential counts as a full allowance, so Codex still ranks by its reset. ok
// is false when no future weekly reset is known.
func weeklyRate(auth *Auth, now time.Time) (float64, bool) {
	at, ok := weeklyResetAt(auth, now)
	if !ok {
		return 0, false
	}
	left := at.Sub(now)
	if left < soonestResetFiveHours {
		left = soonestResetFiveHours
	}
	share := 1 - claudeUsedShare(auth.Quota, "Anthropic-Ratelimit-Unified-7d-Utilization")
	return share * float64(authWeight(auth)) / left.Hours(), true
}

// fiveHourSpent reports whether the snapshot shows the Claude 5-hour window at
// or above soonestResetFiveHourFloor with its reset still ahead. Without a
// reset time the window does not count as spent, so a skipped credential,
// which gets no new traffic, cannot stay skipped on an old snapshot.
func fiveHourSpent(q QuotaState, now time.Time) bool {
	return claudeUsedShare(q, "Anthropic-Ratelimit-Unified-5h-Utilization") >= soonestResetFiveHourFloor &&
		now.Before(q.SignalUnixTime("Anthropic-Ratelimit-Unified-5h-Reset"))
}

// claudeUsedShare reads a Claude utilization signal, a used share from 0 to 1.
// An absent, unreadable, overflowing, NaN, or negative value reads as 0; a
// value above 1, +Inf included, reads as 1.
func claudeUsedShare(q QuotaState, name string) float64 {
	used, ok := q.SignalPercent(name)
	switch {
	case !ok || !(used > 0):
		return 0
	case used > 1:
		return 1
	default:
		return used
	}
}
