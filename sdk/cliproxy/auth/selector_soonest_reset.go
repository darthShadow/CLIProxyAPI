package auth

import (
	"context"
	"strconv"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// SoonestResetSelector picks, among the available credentials, the one whose
// weekly allowance resets soonest, so allowance that would expire unused is
// spent first. A credential with no observed weekly reset is picked first, by
// round robin, so its reset becomes known. Ties go to round robin. Under
// session affinity this selector makes only the first pick of a new session;
// a bound session never moves because of it.
type SoonestResetSelector struct {
	rr RoundRobinSelector
}

// Pick implements Selector.
func (s *SoonestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	var unknown, soonest []*Auth
	var soonestAt time.Time
	for _, auth := range auths {
		if auth == nil {
			continue
		}
		at, ok := weeklyResetAt(auth, now)
		switch {
		case !ok:
			unknown = append(unknown, auth)
		case len(soonest) == 0 || at.Before(soonestAt):
			soonest, soonestAt = []*Auth{auth}, at
		case at.Equal(soonestAt):
			soonest = append(soonest, auth)
		}
	}
	candidates := auths
	switch {
	case len(unknown) > 0:
		candidates = unknown
	case len(soonest) > 0:
		candidates = soonest
	}
	return s.rr.Pick(ctx, provider, model, opts, candidates)
}

// weeklyResetAt reads the weekly window reset from the last passive quota
// snapshot: Claude "anthropic-ratelimit-unified-7d-reset" and Codex
// "x-codex-secondary-reset-at" (Unix seconds), or Codex
// "x-codex-secondary-reset-after-seconds" from the snapshot time.
func weeklyResetAt(auth *Auth, now time.Time) (time.Time, bool) {
	signals := auth.Quota.Signals
	if len(signals) == 0 {
		return time.Time{}, false
	}
	for _, name := range []string{"Anthropic-Ratelimit-Unified-7d-Reset", "X-Codex-Secondary-Reset-At"} {
		if seconds, ok := positiveSignalInt(signals[name]); ok {
			if at := time.Unix(seconds, 0); at.After(now) {
				return at, true
			}
		}
	}
	if seconds, ok := positiveSignalInt(signals["X-Codex-Secondary-Reset-After-Seconds"]); ok && !auth.Quota.ObservedAt.IsZero() {
		if at := auth.Quota.ObservedAt.Add(time.Duration(seconds) * time.Second); at.After(now) {
			return at, true
		}
	}
	return time.Time{}, false
}

func positiveSignalInt(value string) (int64, bool) {
	parsed, errParse := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if errParse != nil || parsed <= 0 {
		return 0, false
	}
	return parsed, true
}
