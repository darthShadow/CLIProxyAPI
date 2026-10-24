package auth

import (
	"context"
	"errors"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type countThrottleExecutor struct {
	identifier string
	calls      atomic.Int32
	authIDs    chan string
}

func (e *countThrottleExecutor) Identifier() string { return e.identifier }

func (e *countThrottleExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, errors.New("unexpected Execute")
}

func (e *countThrottleExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("unexpected ExecuteStream")
}

func (*countThrottleExecutor) Refresh(_ context.Context, auth *Auth) (*Auth, error) { return auth, nil }

func (e *countThrottleExecutor) CountTokens(_ context.Context, auth *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls.Add(1)
	e.authIDs <- auth.ID
	return cliproxyexecutor.Response{}, &Error{HTTPStatus: http.StatusTooManyRequests, Message: "rate limited"}
}

func (*countThrottleExecutor) HttpRequest(context.Context, *Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected HttpRequest")
}

func TestRelease1CountTokens429NeitherRestsNorMovesAccount(t *testing.T) {
	cases := []struct {
		name         string
		globalRetry  int
		accountRetry int
	}{
		{name: "zero retries", globalRetry: 0, accountRetry: 0},
		{name: "positive account retry", globalRetry: 0, accountRetry: 2},
		{name: "account disables global retry", globalRetry: 2, accountRetry: 0},
		{name: "positive global and account retry", globalRetry: 2, accountRetry: 2},
		{name: "positive global retry without override", globalRetry: 3, accountRetry: -1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := "release1-count-429"
			model := provider + "-model"
			manager := NewManager(nil, nil, nil)
			manager.SetRetryConfig(tc.globalRetry, 0, 2)
			executor := &countThrottleExecutor{identifier: provider, authIDs: make(chan string, 4)}
			manager.RegisterExecutor(executor)
			reg := registry.GetGlobalRegistry()
			for _, id := range []string{provider + "-a", provider + "-b"} {
				reg.RegisterClient(id, provider, []*registry.ModelInfo{{ID: model}})
				id := id
				t.Cleanup(func() { reg.UnregisterClient(id) })
				metadata := map[string]any{}
				if tc.accountRetry >= 0 {
					metadata["request_retry"] = tc.accountRetry
				}
				if _, errRegister := manager.Register(context.Background(), &Auth{ID: id, Provider: provider, Metadata: metadata}); errRegister != nil {
					t.Fatalf("register %s: %v", id, errRegister)
				}
			}

			_, errCount := manager.ExecuteCount(context.Background(), []string{provider}, cliproxyexecutor.Request{Model: model}, cliproxyexecutor.Options{})
			if statusCodeFromError(errCount) != http.StatusTooManyRequests {
				t.Fatalf("count error = %v, want the upstream 429", errCount)
			}
			if got := executor.calls.Load(); got != 1 {
				t.Fatalf("count calls = %d, want 1 (no move to another account)", got)
			}
			first := <-executor.authIDs
			auth, ok := manager.GetByID(first)
			if !ok {
				t.Fatalf("auth %s missing", first)
			}
			if auth.Unavailable {
				t.Fatalf("auth %s rests after a count_tokens 429", first)
			}
			if state := auth.ModelStates[model]; state != nil && state.Unavailable {
				t.Fatalf("auth %s model %s rests after a count_tokens 429", first, model)
			}
		})
	}
}
