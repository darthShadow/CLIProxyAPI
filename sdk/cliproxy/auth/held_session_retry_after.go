package auth

import (
	"errors"
	"net/http"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	cliproxysession "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/session"
)

// heldSessionUnavailableError is returned in hold mode when a session's bound
// credential is not available for this request. It names the bound credential
// so the manager can add Retry-After until that credential's own reset.
type heldSessionUnavailableError struct {
	base   *Error
	authID string
}

func errHeldSessionUnavailable(authID string) error {
	return &heldSessionUnavailableError{
		base:   &Error{Code: "auth_unavailable", Message: "the credential bound to this session is unavailable; the session stays on it", HTTPStatus: http.StatusTooManyRequests},
		authID: authID,
	}
}

func (e *heldSessionUnavailableError) Error() string { return e.base.Error() }

func (e *heldSessionUnavailableError) StatusCode() int { return e.base.HTTPStatus }

func (e *heldSessionUnavailableError) Unwrap() error { return e.base }

// heldSessionRetryAfter adds Retry-After to a held-session refusal: the time
// until the bound credential is available again for the model. Without a known
// reset the 429 goes back without Retry-After.
func (m *Manager) heldSessionRetryAfter(err error, model string, now time.Time) error {
	var held *heldSessionUnavailableError
	if m == nil || !errors.As(err, &held) || held == nil {
		return err
	}
	var next time.Time
	m.mu.RLock()
	if auth := m.auths[held.authID]; auth != nil {
		_, _, next = isAuthBlockedForModel(auth, model, now)
	}
	m.mu.RUnlock()
	if !next.After(now) {
		return err
	}
	return &authUnavailableError{base: held.base, retryAfter: next.Sub(now)}
}

// heldBinding reports the credential a held session is bound to, without
// refreshing or changing the binding. It derives the binding key the way Pick
// does for explicit and extracted session IDs; a session that only the LCP
// matcher identifies takes Pick's LCP path, which hold mode does not change.
func (s *SessionAffinitySelector) heldBinding(provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (string, bool) {
	if s == nil || !s.hold {
		return "", false
	}
	primaryID, fallbackID := extractExplicitSessionIDs(opts.Headers, opts.OriginalRequest, opts.Metadata)
	if primaryID == "" {
		primaryID, fallbackID = extractSessionIDs(opts.Headers, opts.OriginalRequest, opts.Metadata)
	}
	if primaryID == "" {
		return "", false
	}
	primaryID = cliproxysession.BoundSessionIdentity(primaryID)
	modelKey := canonicalModelKey(model)
	if scope := heldSessionScope(auths); scope != "" {
		modelKey = scope
	}
	if authID, ok := s.cache.Get(provider + "::" + primaryID + "::" + modelKey); ok {
		return authID, true
	}
	if fallbackID == "" {
		return "", false
	}
	fallbackID = cliproxysession.BoundSessionIdentity(fallbackID)
	if fallbackID == primaryID {
		return "", false
	}
	isFork, _ := opts.Metadata[cliproxyexecutor.IsForkMetadataKey].(bool)
	if !isFork && isSubagentSession(primaryID, fallbackID) && !s.subagentAffinity {
		return "", false
	}
	return s.cache.Get(provider + "::" + fallbackID + "::" + modelKey)
}

// heldSessionErrorForUnavailablePool runs when no candidate is available, so
// the selector never sees the request. For a held session it replaces the
// pool-wide error, whose Retry-After is the earliest reset of any candidate,
// with the held-session refusal naming the bound credential; the manager then
// adds Retry-After from that credential (heldSessionRetryAfter). The caller
// holds m.mu for reading; the binding lookup takes only the session cache lock.
func heldSessionErrorForUnavailablePool(selector Selector, provider, model string, opts cliproxyexecutor.Options, candidates []*Auth, err error) error {
	held, ok := selector.(*SessionAffinitySelector)
	if !ok {
		return err
	}
	authID, bound := held.heldBinding(provider, model, opts, candidates)
	if !bound {
		return err
	}
	return errHeldSessionUnavailable(authID)
}
