package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// NativeClaudeCodeConfirmer reports whether headers and payload identify a native
// Claude Code request at the configured baseline that the local refusal permits.
// It reads the request without changing its headers or payload.
type NativeClaudeCodeConfirmer interface {
	ConfirmsNativeClaudeCode(ctx context.Context, headers http.Header, payload []byte) bool
}

type heldSessionMove struct {
	planLimited      map[string]struct{}
	compaction       bool
	contextCompacted bool
	confirmer        NativeClaudeCodeConfirmer
}

type heldSessionMoveContextKey struct{}

type heldSessionCountContextKey struct{}

// Claude Code compacts on this error and retries the turn. Code stays empty so
// the message reaches the client alone; F28 checks the client strings.
const heldSessionPromptTooLongMessage = "prompt is too long: 433001 tokens > 433000 maximum"

func withHeldSessionMove(ctx context.Context, move heldSessionMove) context.Context {
	return context.WithValue(ctx, heldSessionMoveContextKey{}, move)
}

func heldSessionMoveFromContext(ctx context.Context) heldSessionMove {
	move, _ := ctx.Value(heldSessionMoveContextKey{}).(heldSessionMove)
	return move
}

func restsOnPlanLimit(auth *Auth, now time.Time) bool {
	return auth != nil && auth.Quota.Exceeded && auth.Quota.Reason == "credential_quota" && auth.Quota.NextRecoverAt.After(now)
}

// heldSessionMoveLocked snapshots live candidate rests while m.mu is read-locked.
func (m *Manager) heldSessionMoveLocked(selector Selector, candidates []*Auth, now time.Time) heldSessionMove {
	held, ok := selector.(*SessionAffinitySelector)
	if !ok || !held.hold || heldSessionScope(candidates) != "provider:claude" {
		return heldSessionMove{}
	}
	move := heldSessionMove{planLimited: make(map[string]struct{})}
	for _, auth := range candidates {
		if restsOnPlanLimit(auth, now) {
			move.planLimited[auth.ID] = struct{}{}
		}
	}
	executor, _ := m.executorLocked("claude")
	move.confirmer, _ = executor.(NativeClaudeCodeConfirmer)
	return move
}

func validCompactionKind(value string) bool {
	switch strings.TrimSpace(value) {
	case "auto", "manual", "reactive":
		return true
	default:
		return false
	}
}

func (h heldSessionMove) confirmHints(ctx context.Context, opts cliproxyexecutor.Options) heldSessionMove {
	compaction := validCompactionKind(opts.Headers.Get("X-Claude-Code-Compaction"))
	contextCompacted := validCompactionKind(opts.Headers.Get("X-Claude-Code-Context-Compacted"))
	if (compaction || contextCompacted) && len(opts.OriginalRequest) > 0 && h.confirmer != nil && h.confirmer.ConfirmsNativeClaudeCode(ctx, opts.Headers, opts.OriginalRequest) {
		h.compaction = compaction
		h.contextCompacted = contextCompacted
	}
	return h
}

func heldSessionRefusal(ctx context.Context, authID string, opts cliproxyexecutor.Options) error {
	h := heldSessionMoveFromContext(ctx)
	if _, limited := h.planLimited[authID]; limited {
		if h.compaction || h.contextCompacted {
			return nil
		}
		count, _ := ctx.Value(heldSessionCountContextKey{}).(bool)
		if !count && len(opts.OriginalRequest) > 0 && h.confirmer != nil && h.confirmer.ConfirmsNativeClaudeCode(ctx, opts.Headers, opts.OriginalRequest) {
			return &Error{HTTPStatus: http.StatusBadRequest, Message: heldSessionPromptTooLongMessage}
		}
		return errHeldSessionPlanLimited(authID)
	}
	return errHeldSessionUnavailable(authID)
}

func isHeldSessionPromptTooLong(err error) bool {
	var refusal *Error
	return errors.As(err, &refusal) && refusal.StatusCode() == http.StatusBadRequest && refusal.Message == heldSessionPromptTooLongMessage
}

// heldSessionDiscoveryRefusal re-picks once after a credential-scoped plan-limit
// 429 stop and returns the local 400 only when that pick yields it. Otherwise it
// returns nil so the caller returns the upstream error.
func (m *Manager) heldSessionDiscoveryRefusal(ctx context.Context, providers []string, model string, opts cliproxyexecutor.Options, err error) error {
	if !isRequestStopError(err) || !isCredentialScopedError(err) || statusCodeFromError(err) != http.StatusTooManyRequests {
		return nil
	}
	if m.HomeEnabled() {
		return nil
	}
	m.mu.RLock()
	held, ok := m.selector.(*SessionAffinitySelector)
	hold := ok && held.hold
	m.mu.RUnlock()
	if !hold {
		return nil
	}
	if validCompactionKind(opts.Headers.Get("X-Claude-Code-Compaction")) {
		return nil
	}
	_, _, _, pickedErr := m.pickNextMixed(ctx, providers, model, opts, nil)
	if isHeldSessionPromptTooLong(pickedErr) {
		return pickedErr
	}
	return nil
}

const heldSessionWeeklyFloor = 95

func nearPlanLimit(q QuotaState, now time.Time) bool {
	used, reset, _, ok := WeeklyQuota(q)
	return fiveHourSpent(q, now) || (ok && used >= heldSessionWeeklyFloor && reset.After(now))
}

func (s *SessionAffinitySelector) heldSessionRepick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, available, fallbackAuths []*Auth, boundID string, now time.Time) *Auth {
	h := heldSessionMoveFromContext(ctx)
	if !s.hold || !h.contextCompacted || h.compaction {
		return nil
	}
	for _, bound := range available {
		if bound.ID != boundID {
			continue
		}
		if !nearPlanLimit(bound.Quota, now) {
			return nil
		}
		auth, err := s.fallback.Pick(ctx, provider, model, opts, fallbackAuths)
		if err == nil && auth != nil && auth.ID != boundID {
			return auth
		}
		return nil
	}
	return nil
}
