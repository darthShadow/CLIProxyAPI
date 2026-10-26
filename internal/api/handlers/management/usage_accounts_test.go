package management

import (
	"context"
	"encoding/json"
	"fmt"
	"html"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type accountFixtureReader struct {
	plan  *claude.OAuthPlan
	calls []string
	usage helps.CodexAccountUsage
	err   error
}

func (r *accountFixtureReader) Codex(_ context.Context, _ *config.Config, a *coreauth.Auth) (helps.CodexAccountUsage, error) {
	r.calls = append(r.calls, a.ID)
	return r.usage, r.err
}
func (r *accountFixtureReader) Claude(_ context.Context, _ *config.Config, a *coreauth.Auth) (*claude.OAuthPlan, error) {
	r.calls = append(r.calls, a.ID)
	if r.plan != nil {
		return r.plan, nil
	}
	return &claude.OAuthPlan{}, nil
}

type accountFakeStatus struct{}

func (accountFakeStatus) Error() string   { return "HTTP 401 sk-fake-upstream-1111 fake-header-value" }
func (accountFakeStatus) StatusCode() int { return 401 }
func accountsHandler(t *testing.T, as []*coreauth.Auth, r *accountFixtureReader) *Handler {
	t.Helper()
	old := liveAccountReader
	liveAccountReader = r
	t.Cleanup(func() { liveAccountReader = old })
	m := coreauth.NewManager(nil, nil, nil)
	for _, a := range as {
		if _, e := m.Register(context.Background(), a); e != nil {
			t.Fatal(e)
		}
	}
	return &Handler{cfg: &config.Config{}, authManager: m}
}
func accountsResponse(h *Handler, method string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/", nil)
	h.PostUsageAccounts(c)
	c.Writer.WriteHeaderNow()
	return w
}
func liveAuth(id, p string) *coreauth.Auth {
	return &coreauth.Auth{ID: id, Provider: p, Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"access_token": "fake-access-token", "email": id + "@example.test", "account_uuid": "fake-account"}}
}

func TestUsageAccountsConcurrentConfigReload(t *testing.T) {
	r := &accountFixtureReader{}
	h := accountsHandler(t, []*coreauth.Auth{liveAuth("a", "claude"), liveAuth("b", "codex")}, r)
	started := make(chan struct{})
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		h.SetConfig(&config.Config{})
		close(started)
		for {
			select {
			case <-stop:
				return
			default:
				h.SetConfig(&config.Config{})
			}
		}
	}()
	defer func() {
		close(stop)
		<-done
	}()
	<-started
	for range 100 {
		w := accountsResponse(h, "POST")
		if w.Code != 200 {
			t.Fatalf("status=%d want=200", w.Code)
		}
	}
	wantCalls := strings.Repeat("ab", 100)
	if got := strings.Join(r.calls, ""); got != wantCalls {
		t.Errorf("calls=%q want=%q", got, wantCalls)
	}
}

func TestUsageAccountsReadsOnlyOnPostAndShowsStatusOnly(t *testing.T) {
	a := liveAuth("a", "claude")
	b := liveAuth("b", "codex")
	key := liveAuth("key", "claude")
	key.Attributes = map[string]string{"auth_kind": "apikey", "api_key": "sk-fake-upstream-1111"}
	disabled := liveAuth("disabled", "codex")
	disabled.Disabled = true
	expired := liveAuth("expired", "claude")
	expired.Metadata["expired"] = time.Now().Add(-time.Hour).Format(time.RFC3339)
	r := &accountFixtureReader{err: accountFakeStatus{}}
	h := accountsHandler(t, []*coreauth.Auth{b, a, key, disabled, expired}, r)
	w := accountsResponse(h, "GET")
	if len(r.calls) != 0 {
		t.Error("GET reads accounts")
	}
	w = accountsResponse(h, "POST")
	if w.Code != 200 {
		t.Fatalf("status=%d want=200", w.Code)
	}
	if !reflect.DeepEqual(r.calls, []string{"a", "b"}) {
		t.Errorf("calls=%v want=[a b]", r.calls)
	}
	s := w.Body.String()
	requireText(t, s, "Not Reported", "Failed", "HTTP 401", "The provider did not accept this account&#39;s sign-in (HTTP 401). Sign in to this account again.", "token expired", "Not Available")
	for _, bad := range []string{"sk-fake-upstream-1111", "fake-header-value", "fake-access-token", "Reset quota", "/consume"} {
		if strings.Contains(s, bad) {
			t.Errorf("leaked or unsafe %q", bad)
		}
	}
	for _, msg := range []string{"request failed", "answered for a different account", "reset details unreadable"} {
		r.err = fmt.Errorf("%s", msg)
		s = accountsResponse(h, "POST").Body.String()
		requireText(t, s, msg)
	}
}
func TestUsageAccountsShowsResetExpiries(t *testing.T) {
	now := time.Now()
	future := now.Add(22*24*time.Hour + time.Hour)
	past := now.Add(-time.Hour)
	r := &accountFixtureReader{usage: helps.CodexAccountUsage{Plan: "pro", ResetsAvailable: 2, ResetExpiries: []*time.Time{&future, nil}}}
	h := accountsHandler(t, []*coreauth.Auth{liveAuth("b", "codex")}, r)
	w := accountsResponse(h, "POST")
	if w.Code != 200 {
		t.Fatalf("status=%d want=200", w.Code)
	}
	requireText(t, html.UnescapeString(w.Body.String()), "Reset 1: expires "+future.Local().Format("Jan 02, 2006 15:04 UTC-07:00")+" (in 22\u202fdays)", "Reset 2: does not expire", "Checked")
	r.usage.ResetExpiries = []*time.Time{&past}
	requireText(t, accountsResponse(h, "POST").Body.String(), "expired at")
	r.usage.ResetDetailsErr = accountDetails404{}
	requireText(t, accountsResponse(h, "POST").Body.String(), "pro", "2", "reset details: HTTP 404")
}

type accountDetails404 struct{}

func (accountDetails404) Error() string   { return "HTTP 404" }
func (accountDetails404) StatusCode() int { return 404 }

func TestUsageAccountsMatchesClaudePlanReference(t *testing.T) {
	for _, tc := range []struct{ name, body, want string }{
		{"Max", `{"account":{"has_claude_max":true,"has_claude_pro":false}}`, "Max"},
		{"Pro", `{"account":{"has_claude_max":false,"has_claude_pro":true}}`, "Pro"},
		{"active Team", `{"account":{"has_claude_max":false,"has_claude_pro":false},"organization":{"organization_type":"claude_team","subscription_status":"active"}}`, "Team"},
		{"inactive Team", `{"account":{"has_claude_max":false,"has_claude_pro":false},"organization":{"organization_type":"claude_team","subscription_status":"cancelled"}}`, "Free"},
		{"missing flags", `{}`, "Not Reported"},
		{"one missing flag", `{"account":{"has_claude_max":false}}`, "Not Reported"},
		{"Free", `{"account":{"has_claude_max":false,"has_claude_pro":false}}`, "Free"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var plan claude.OAuthPlan
			if e := json.Unmarshal([]byte(tc.body), &plan); e != nil {
				t.Fatal(e)
			}
			r := &accountFixtureReader{plan: &plan}
			h := accountsHandler(t, []*coreauth.Auth{liveAuth("a", "claude")}, r)
			w := accountsResponse(h, "POST")
			if w.Code != 200 {
				t.Fatalf("status=%d", w.Code)
			}
			requireText(t, w.Body.String(), `<span class="em">a@example.test</span><span class="alias">`+alias("a@example.test")+`</span></th><td class="td"><span class="data-mono">`+tc.want+`</span>`)
		})
	}
}
