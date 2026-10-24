package auth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sync/atomic"
	"testing"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type scopeTestError struct {
	status     int
	credential bool
}

func (e scopeTestError) Error() string {
	return `{"type":"error","error":{"type":"rate_limit_error","message":"fixture"}}`
}

func (e scopeTestError) StatusCode() int { return e.status }

func (e scopeTestError) IsCredentialScoped() bool { return e.credential }

func TestRequestScopedRuleScopeSplitsPlanLimitFromThrottle(t *testing.T) {
	cfg := &internalconfig.Config{OAuthRequestScopedErrors: map[string][]internalconfig.RequestScopedErrorRule{
		"claude": {
			{Status: 429, Scope: "model", MatchRegexr: []string{"^"}, Action: RequestScopedActionStop},
			{Status: 429, Scope: "credential", MatchRegexr: []string{"^"}, Action: RequestScopedActionStopAndCooldown},
		},
		"codex": {
			{Status: 429, Scope: "modle", MatchRegexr: []string{"^"}, Action: RequestScopedActionStop},
		},
	}}
	claude := &Auth{ID: "scope-claude", Provider: "claude", Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}}
	codex := &Auth{ID: "scope-codex", Provider: "codex", Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}}
	cases := []struct {
		name       string
		auth       *Auth
		err        error
		wantAction string
		wantOK     bool
	}{
		{name: "per-minute throttle stops without rest", auth: claude, err: scopeTestError{status: 429}, wantAction: RequestScopedActionStop, wantOK: true},
		{name: "plan limit stops and rests the account", auth: claude, err: scopeTestError{status: 429, credential: true}, wantAction: RequestScopedActionStopAndCooldown, wantOK: true},
		{name: "unknown scope never matches", auth: codex, err: scopeTestError{status: 429}, wantOK: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			action, ok := matchRequestScopedErrorAction(tc.auth, tc.err, cfg)
			if ok != tc.wantOK || action != tc.wantAction {
				t.Fatalf("matchRequestScopedErrorAction() = %q, %v; want %q, %v", action, ok, tc.wantAction, tc.wantOK)
			}
		})
	}
}

// stopStatusExecutor fails every Execute with one status and counts attempts.
type stopStatusExecutor struct {
	provider     string
	status       int
	calls        atomic.Int32
	secondAuthID string
	secondCalls  atomic.Int32
}

func (e *stopStatusExecutor) Identifier() string { return e.provider }

func (e *stopStatusExecutor) Execute(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	if auth.ID == e.secondAuthID {
		e.secondCalls.Add(1)
	}
	e.calls.Add(1)
	return cliproxyexecutor.Response{}, scopeTestError{status: e.status}
}

func (e *stopStatusExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("unexpected ExecuteStream")
}

func (*stopStatusExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }

func (*stopStatusExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("unexpected CountTokens")
}

func (*stopStatusExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected HttpRequest")
}

// personalProxyStopRules loads P's request-scoped rules and the statuses of
// its unscoped stop rules per provider (429 has a scoped split with its own
// test).
func personalProxyStopRules(t *testing.T) (map[string][]internalconfig.RequestScopedErrorRule, map[string][]int) {
	t.Helper()
	cfg, errLoad := internalconfig.LoadConfig("../../../deploy/P.config.yaml")
	if errLoad != nil {
		t.Fatalf("LoadConfig(P) error = %v", errLoad)
	}
	statuses := map[string][]int{}
	for _, provider := range []string{"claude", "codex"} {
		for _, rule := range cfg.OAuthRequestScopedErrors[provider] {
			if rule.Action == RequestScopedActionStop && rule.Scope == "" {
				statuses[provider] = append(statuses[provider], rule.Status)
			}
		}
		if len(statuses[provider]) == 0 {
			t.Fatalf("P has no unscoped %s stop rules", provider)
		}
	}
	return cfg.OAuthRequestScopedErrors, statuses
}

// Recovered row 97: each unscoped stop status in P's lists returns to the
// client after one attempt, on the per-attempt lookup.
func TestRelease1RequestScopedStopEachStatus(t *testing.T) {
	rules, statuses := personalProxyStopRules(t)
	for _, provider := range []string{"claude", "codex"} {
		for _, status := range statuses[provider] {
			t.Run(fmt.Sprintf("%s/%d", provider, status), func(t *testing.T) {
				ctx := context.Background()
				model := fmt.Sprintf("release1-stop-%s-%d", provider, status)
				authID := model + "-auth"
				executor := &stopStatusExecutor{provider: provider, status: status}
				manager := NewManager(nil, &FillFirstSelector{}, nil)
				manager.SetRetryConfig(0, 0, 0)
				manager.RegisterExecutor(executor)
				manager.runtimeConfig.Store(&internalconfig.Config{OAuthRequestScopedErrors: rules})
				registry.GetGlobalRegistry().RegisterClient(authID, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(authID) })
				if _, errRegister := manager.Register(ctx, &Auth{ID: authID, Provider: provider, Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}}); errRegister != nil {
					t.Fatalf("register: %v", errRegister)
				}
				secondAuthID := model + "-backup"
				executor.secondAuthID = secondAuthID
				registry.GetGlobalRegistry().RegisterClient(secondAuthID, provider, []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(secondAuthID) })
				if _, errRegister := manager.Register(ctx, &Auth{ID: secondAuthID, Provider: provider, Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}}); errRegister != nil {
					t.Fatalf("register second account: %v", errRegister)
				}
				_, errExecute := manager.Execute(ctx, []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
				if got := executor.secondCalls.Load(); got != 0 {
					t.Fatalf("second account calls = %d, want 0 (no move to another account)", got)
				}
				if got := executor.calls.Load(); got != 1 {
					t.Fatalf("attempts = %d, want 1", got)
				}
				account, ok := manager.GetByID(authID)
				if !ok || account.ID != authID {
					t.Fatalf("account = %v, found = %v, want bound account %q", account, ok, authID)
				}
				if !account.NextRetryAfter.IsZero() || account.Quota.Exceeded || !account.Quota.NextRecoverAt.IsZero() {
					t.Fatalf("account cooldown = retry %v, quota %+v, want no cooldown", account.NextRetryAfter, account.Quota)
				}
				for modelID, state := range account.ModelStates {
					if state != nil && (!state.NextRetryAfter.IsZero() || state.Quota.Exceeded || !state.Quota.NextRecoverAt.IsZero()) {
						t.Fatalf("model %q cooldown = retry %v, quota %+v, want no cooldown", modelID, state.NextRetryAfter, state.Quota)
					}
				}
				if got := statusCodeFromError(errExecute); got != status {
					t.Fatalf("status = %d, want %d", got, status)
				}
			})
		}
	}
}
