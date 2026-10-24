package claude

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type heldPromptExecutor struct{ bound string }

func (*heldPromptExecutor) Identifier() string { return "claude" }
func (*heldPromptExecutor) ConfirmsNativeClaudeCode(context.Context, http.Header, []byte) bool {
	return true
}
func (e *heldPromptExecutor) Execute(_ context.Context, a *coreauth.Auth, _ coreexecutor.Request, _ coreexecutor.Options) (coreexecutor.Response, error) {
	e.bound = a.ID
	return coreexecutor.Response{Payload: []byte(`{"id":"bind","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)}, nil
}
func (*heldPromptExecutor) ExecuteStream(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (*coreexecutor.StreamResult, error) {
	return nil, errors.New("unexpected upstream stream")
}
func (*heldPromptExecutor) Refresh(_ context.Context, a *coreauth.Auth) (*coreauth.Auth, error) {
	return a, nil
}
func (*heldPromptExecutor) CountTokens(context.Context, *coreauth.Auth, coreexecutor.Request, coreexecutor.Options) (coreexecutor.Response, error) {
	return coreexecutor.Response{}, errors.New("unexpected count")
}
func (*heldPromptExecutor) HttpRequest(context.Context, *coreauth.Auth, *http.Request) (*http.Response, error) {
	return nil, errors.New("unexpected HTTP")
}

func TestClaudeMessagesHeldSessionPromptTooLong(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%v", stream), func(t *testing.T) {
			selector := coreauth.NewSessionAffinitySelectorWithConfig(coreauth.SessionAffinityConfig{Fallback: &coreauth.SoonestResetSelector{}, TTL: time.Hour, Hold: true})
			t.Cleanup(selector.Stop)
			manager := coreauth.NewManager(nil, selector, nil)
			executor := &heldPromptExecutor{}
			manager.RegisterExecutor(executor)
			model := "held-prompt-model"
			for _, id := range []string{"held-prompt-a", "held-prompt-b"} {
				a := &coreauth.Auth{ID: id, Provider: "claude", Status: coreauth.StatusActive}
				if id == "held-prompt-b" {
					a.Quota.Signals = map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(time.Now().Add(72*time.Hour).Unix(), 10)}
				}
				registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
				t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
				if _, err := manager.Register(context.Background(), a); err != nil {
					t.Fatal(err)
				}
			}
			h := NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(&sdkconfig.SDKConfig{}, manager))
			call := func(stream bool) *httptest.ResponseRecorder {
				recorder := httptest.NewRecorder()
				ctx, _ := gin.CreateTestContext(recorder)
				ctx.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":%v}`, model, stream)))
				ctx.Request.Header.Set("Content-Type", "application/json")
				ctx.Request.Header.Set("X-Claude-Code-Session-Id", "held-prompt-session")
				h.ClaudeMessages(ctx)
				return recorder
			}
			bind := call(false)
			if bind.Code != 200 || executor.bound != "held-prompt-a" {
				t.Fatalf("bind = %d, %q; want 200 on A", bind.Code, executor.bound)
			}
			delay := 90 * time.Minute
			manager.MarkResult(context.Background(), coreauth.Result{AuthID: executor.bound, Provider: "claude", Model: model, CredentialScope: true, RetryAfter: &delay, Error: &coreauth.Error{Code: coreauth.ErrorCodeForceCooldown, HTTPStatus: 429}})
			got := call(stream)
			if got.Code != 400 {
				t.Fatalf("status = %d; want 400; body=%s", got.Code, got.Body.String())
			}
			if ct := got.Header().Get("Content-Type"); ct != "application/json" {
				t.Fatalf("Content-Type = %q; want application/json", ct)
			}
			if retry := got.Header().Get("Retry-After"); retry != "" {
				t.Fatalf("Retry-After = %q; want none", retry)
			}
			var decoded map[string]any
			if err := json.Unmarshal(got.Body.Bytes(), &decoded); err != nil {
				t.Fatal(err)
			}
			want := map[string]any{"type": "error", "error": map[string]any{"type": "invalid_request_error", "message": "prompt is too long: 433001 tokens > 433000 maximum"}}
			if !reflect.DeepEqual(decoded, want) {
				t.Fatalf("decoded = %#v; want %#v", decoded, want)
			}
			wire := `{"type":"error","error":{"type":"invalid_request_error","message":"prompt is too long: 433001 tokens \u003e 433000 maximum"}}`
			if got.Body.String() != wire {
				t.Fatalf("wire = %q; want %q", got.Body.String(), wire)
			}
		})
	}
}
