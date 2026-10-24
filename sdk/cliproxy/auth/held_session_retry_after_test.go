package auth

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func TestRelease1HeldSessionRetryAfterUntilBoundReset(t *testing.T) {
	model := "claude-opus-5-5"
	now := time.Now()
	reset := now.Add(90 * time.Minute)
	manager := NewManager(nil, nil, nil)
	for _, auth := range []*Auth{
		{ID: "held-resting", Provider: "claude", ModelStates: map[string]*ModelState{model: {Unavailable: true, NextRetryAfter: reset}}},
		{ID: "held-no-reset", Provider: "claude"},
	} {
		if _, errRegister := manager.Register(context.Background(), auth); errRegister != nil {
			t.Fatalf("register %s: %v", auth.ID, errRegister)
		}
	}

	got := manager.heldSessionRetryAfter(errHeldSessionUnavailable("held-resting"), model, now)
	var authErr *Error
	if !errors.As(got, &authErr) || authErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("error = %v, want a 429 *Error", got)
	}
	header := SafeResponseHeaders(got).Get("Retry-After")
	seconds, errParse := strconv.Atoi(header)
	if errParse != nil || seconds < 5390 || seconds > 5401 {
		t.Fatalf("Retry-After = %q, want about 5400 seconds (the bound account's reset)", header)
	}

	plain := manager.heldSessionRetryAfter(errHeldSessionUnavailable("held-no-reset"), model, now)
	if SafeResponseHeaders(plain).Get("Retry-After") != "" {
		t.Fatalf("Retry-After present without a known reset: %v", SafeResponseHeaders(plain))
	}
	if !errors.As(plain, &authErr) || authErr.StatusCode() != http.StatusTooManyRequests {
		t.Fatalf("error without reset = %v, want the 429", plain)
	}

	other := &Error{Code: "auth_not_found", Message: "no auth available"}
	if manager.heldSessionRetryAfter(other, model, now) != other {
		t.Fatal("a non-hold error was changed")
	}
}

// heldPoolExecutor only makes its provider routable; selection sends nothing.
type heldPoolExecutor struct{ provider string }

func (e heldPoolExecutor) Identifier() string { return e.provider }

func (heldPoolExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("unexpected Execute")
}

func (heldPoolExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("unexpected ExecuteStream")
}

func (heldPoolExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }

func (heldPoolExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("unexpected CountTokens")
}

func (heldPoolExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected HttpRequest")
}

// When every candidate is unavailable, the manager's availability check fails
// before the selector runs. A held session must still get the held-session 429
// with Retry-After from its bound credential, not the pool error with the
// earliest reset of any candidate. Both manager paths (single provider and
// mixed) and both reset cases (bound credential resting 90 minutes, bound
// credential unavailable with no known reset) run through the real manager.
func TestRelease1HeldSessionRetryAfterUntilBoundResetWhenEveryCandidateRests(t *testing.T) {
	const provider = "release1-held-pool"
	model := provider + "-model"
	for _, path := range []string{"single", "mixed"} {
		for _, boundReset := range []time.Duration{90 * time.Minute, 0} {
			name := path + ", bound reset " + boundReset.String()
			t.Run(name, func(t *testing.T) {
				ctx := context.Background()
				selector := newHoldTestSelector(t, true, &sequenceSelector{})
				manager := NewManager(nil, selector, nil)
				manager.RegisterExecutor(heldPoolExecutor{provider: provider})
				reg := registry.GetGlobalRegistry()
				for _, id := range []string{provider + "-a", provider + "-b"} {
					reg.RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
					id := id
					t.Cleanup(func() { reg.UnregisterClient(id) })
					if _, errRegister := manager.Register(ctx, &Auth{ID: id, Provider: provider}); errRegister != nil {
						t.Fatalf("register %s: %v", id, errRegister)
					}
				}
				pick := func() (*Auth, error) {
					opts := holdTestOpts("session-pool-"+path, "")
					if path == "single" {
						auth, _, errPick := manager.pickNext(ctx, provider, model, opts, nil)
						return auth, errPick
					}
					auth, _, _, errPick := manager.pickNextMixed(ctx, []string{provider}, model, opts, nil)
					return auth, errPick
				}
				bound, errBind := pick()
				if errBind != nil || bound == nil {
					t.Fatalf("bind pick = %v, %v", bound, errBind)
				}
				now := time.Now()
				manager.mu.Lock()
				for id, auth := range manager.auths {
					state := &ModelState{Unavailable: true, NextRetryAfter: now.Add(10 * time.Minute)}
					if id == bound.ID {
						state = &ModelState{Unavailable: true}
						if boundReset > 0 {
							state.NextRetryAfter = now.Add(boundReset)
						}
					}
					auth.ModelStates = map[string]*ModelState{model: state}
				}
				manager.mu.Unlock()

				got, errPick := pick()
				if errPick == nil {
					t.Fatalf("pick = %s, want the held-session 429 while every candidate rests", got.ID)
				}
				var authErr *Error
				if !errors.As(errPick, &authErr) || authErr.StatusCode() != http.StatusTooManyRequests {
					t.Fatalf("pick error = %v, want the held-session 429 (the pool error is 503)", errPick)
				}
				header := SafeResponseHeaders(errPick).Get("Retry-After")
				if boundReset == 0 {
					if header != "" {
						t.Fatalf("Retry-After = %q, want none: the bound credential has no known reset (the pool's other credential resets in 600 s)", header)
					}
					return
				}
				seconds, errParse := strconv.Atoi(header)
				if errParse != nil || seconds < 5390 || seconds > 5401 {
					t.Fatalf("Retry-After = %q, want about 5400 seconds (the bound credential's reset, not the pool's 600)", header)
				}
			})
		}
	}
}
