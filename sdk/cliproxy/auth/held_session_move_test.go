package auth

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	log "github.com/sirupsen/logrus"
)

type confirmingClaudeExecutor struct {
	heldPoolExecutor
	confirm  bool
	recorder *claudeConfirmationRecorder
}

type claudeConfirmationCall struct {
	headers http.Header
	body    []byte
}

type claudeConfirmationRecorder struct {
	calls []claudeConfirmationCall
}

func (e confirmingClaudeExecutor) ConfirmsNativeClaudeCode(_ context.Context, headers http.Header, body []byte) bool {
	if e.recorder != nil {
		e.recorder.calls = append(e.recorder.calls, claudeConfirmationCall{headers: headers.Clone(), body: bytes.Clone(body)})
	}
	return e.confirm
}

const heldMoveModel = "claude-opus-5-5"
const heldMovePlain = "the credential bound to this session is unavailable; the session stays on it"
const heldMoveHint = "the account bound to this session is at its plan limit"

type heldMoveFixture struct {
	t              *testing.T
	manager        *Manager
	path, provider string
}

func newHeldMoveFixture(t *testing.T, path, provider string) *heldMoveFixture {
	t.Helper()
	m := NewManager(nil, newHoldTestSelector(t, true, &SoonestResetSelector{}), nil)
	if provider == "claude" {
		m.RegisterExecutor(confirmingClaudeExecutor{heldPoolExecutor: heldPoolExecutor{provider}, confirm: true})
	} else {
		m.RegisterExecutor(heldPoolExecutor{provider})
	}
	reg := registry.GetGlobalRegistry()
	for _, id := range []string{"held-move-a", "held-move-b"} {
		reg.RegisterClient(id, provider, []*registry.ModelInfo{{ID: heldMoveModel}})
		t.Cleanup(func() { reg.UnregisterClient(id) })
		a := &Auth{ID: id, Provider: provider}
		if id == "held-move-b" {
			a.Quota.Signals = map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(time.Now().Add(72*time.Hour).Unix(), 10)}
		}
		if _, err := m.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	f := &heldMoveFixture{t, m, path, provider}
	f.want(f.opts("", ""), "held-move-a")
	return f
}
func heldMovePaths(t *testing.T, run func(*testing.T, string)) {
	t.Helper()
	for _, path := range []string{"single", "mixed"} {
		t.Run(path, func(t *testing.T) { run(t, path) })
	}
}
func (f *heldMoveFixture) opts(agent, kind string) cliproxyexecutor.Options {
	o := holdTestOpts("held-move-session", agent)
	o.OriginalRequest = []byte(`{"messages":[]}`)
	if kind != "" {
		o.Headers.Set("X-Claude-Code-Compaction", kind)
	}
	return o
}
func (f *heldMoveFixture) pick(o cliproxyexecutor.Options) (*Auth, error) {
	if f.path == "single" {
		a, _, err := f.manager.pickNext(context.Background(), f.provider, heldMoveModel, o, nil)
		return a, err
	}
	a, _, _, err := f.manager.pickNextMixed(context.Background(), []string{f.provider}, heldMoveModel, o, nil)
	return a, err
}
func (f *heldMoveFixture) want(o cliproxyexecutor.Options, id string) {
	f.t.Helper()
	a, err := f.pick(o)
	if err != nil || a == nil || a.ID != id {
		f.t.Fatalf("pick = %v, %v; want %s", a, err, id)
	}
}
func (f *heldMoveFixture) refusal(o cliproxyexecutor.Options, message string) error {
	f.t.Helper()
	a, err := f.pick(o)
	var e *Error
	if !errors.As(err, &e) || e.StatusCode() != 429 || e.Code != "auth_unavailable" || e.Message != message {
		f.t.Fatalf("pick = %v, %v; want 429 auth_unavailable %q", a, err, message)
	}
	return err
}
func (f *heldMoveFixture) rest(id string, credential bool) {
	f.t.Helper()
	d := 90 * time.Minute
	if !credential {
		d = 10 * time.Minute
	}
	f.manager.MarkResult(context.Background(), Result{AuthID: id, Provider: f.provider, Model: heldMoveModel, CredentialScope: credential, RetryAfter: &d, Error: &Error{Code: ErrorCodeForceCooldown, HTTPStatus: 429}})
}
func (f *heldMoveFixture) signals(id string, signals map[string]string) {
	f.manager.mu.Lock()
	defer f.manager.mu.Unlock()
	if f.manager.auths[id].Quota.Signals == nil {
		f.manager.auths[id].Quota.Signals = make(map[string]string)
	}
	for k, v := range signals {
		if v == "" {
			delete(f.manager.auths[id].Quota.Signals, k)
		} else {
			f.manager.auths[id].Quota.Signals[k] = v
		}
	}
}
func TestHeldSessionCompactionMovesOffPlanLimit(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		for _, kind := range []string{"manual", "auto", "reactive"} {
			t.Run(kind, func(t *testing.T) {
				f := newHeldMoveFixture(t, path, "claude")
				f.rest("held-move-a", true)
				f.want(f.opts("", kind), "held-move-b")
				f.manager.ResetQuota(context.Background(), "held-move-a")
				o := f.opts("", "")
				o.Headers.Set("X-Claude-Code-Session-Id", "new-session")
				f.want(o, "held-move-a")
				f.want(f.opts("", ""), "held-move-b")
			})
		}
	})
}
func TestHeldSessionPlanLimitRefusalAsksForCompact(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		hook := setupTestLoggerHook(t)
		log.SetLevel(log.InfoLevel)
		f := newHeldMoveFixture(t, path, "claude")
		f.manager.RegisterExecutor(confirmingClaudeExecutor{heldPoolExecutor: heldPoolExecutor{"claude"}, confirm: false})
		f.rest("held-move-a", true)
		err := f.refusal(f.opts("", ""), heldMoveHint)
		seconds, e := strconv.Atoi(SafeResponseHeaders(err).Get("Retry-After"))
		if e != nil || seconds < 5390 || seconds > 5401 {
			t.Fatalf("Retry-After = %d, %v; want about 5400", seconds, e)
		}
		for _, entry := range hook.AllEntries() {
			if entry.Message == heldMovePromptTooLongLog(path) {
				t.Fatalf("hint refusal logs prompt-too-long: %s", entry.Message)
			}
		}
	})
}

const heldMovePromptTooLong = "prompt is too long: 433001 tokens > 433000 maximum"

func heldMovePromptTooLongLog(path string) string {
	provider := "claude"
	if path == "mixed" {
		provider = "mixed"
	}
	return "session-affinity: held session gets a local prompt-too-long at a plan-limited auth | session=" + truncateSessionID("claude:held-move-session") + " bound_auth=held-move-a provider=" + provider + " model=" + heldMoveModel
}

func wantHeldMovePromptTooLong(t *testing.T, err error) {
	t.Helper()
	var e *Error
	if !errors.As(err, &e) || e.StatusCode() != 400 || e.Code != "" || e.Message != heldMovePromptTooLong {
		t.Fatalf("error = %v; want 400 with empty code and message %q", err, heldMovePromptTooLong)
	}
	if got := SafeResponseHeaders(err).Get("Retry-After"); got != "" {
		t.Fatalf("Retry-After = %q; want none", got)
	}
}

func (f *heldMoveFixture) promptTooLong(o cliproxyexecutor.Options) {
	f.t.Helper()
	_, err := f.pick(o)
	wantHeldMovePromptTooLong(f.t, err)
}

func TestHeldSessionPlainRequestAtPlanLimitGetsPromptTooLong(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		hook := setupTestLoggerHook(t)
		log.SetLevel(log.InfoLevel)
		f := newHeldMoveFixture(t, path, "claude")
		recorder := &claudeConfirmationRecorder{}
		f.manager.RegisterExecutor(confirmingClaudeExecutor{heldPoolExecutor: heldPoolExecutor{"claude"}, confirm: true, recorder: recorder})
		plain := f.opts("", "")
		f.want(plain, "held-move-a")
		if got := len(recorder.calls); got != 0 {
			t.Fatalf("healthy plain pick confirmer calls = %d, want 0", got)
		}
		wantCalls := func(count int, opts cliproxyexecutor.Options) {
			t.Helper()
			if got := len(recorder.calls); got != count {
				t.Fatalf("confirmer calls = %d, want %d", got, count)
			}
			call := recorder.calls[count-1]
			if got, want := call.headers.Get("X-Claude-Code-Session-Id"), opts.Headers.Get("X-Claude-Code-Session-Id"); got != want {
				t.Fatalf("confirmer session header = %q, want %q", got, want)
			}
			if !bytes.Equal(call.body, opts.OriginalRequest) {
				t.Fatalf("confirmer body = %q, want %q", call.body, opts.OriginalRequest)
			}
		}
		f.rest("held-move-a", true)
		hook.Reset()
		f.promptTooLong(plain)
		wantCalls(1, plain)
		entries := hook.AllEntries()
		if len(entries) != 1 || entries[0].Level != log.InfoLevel || entries[0].Message != heldMovePromptTooLongLog(path) {
			t.Fatalf("refusal logs = %#v; want one Info entry %q", entries, heldMovePromptTooLongLog(path))
		}
		reactive := f.opts("", "reactive")
		f.want(reactive, "held-move-b")
		wantCalls(2, reactive)
		f.want(f.opts("", ""), "held-move-b")
	})
}

func TestHeldSessionCountTokensKeepsHint(t *testing.T) {
	f := newHeldMoveFixture(t, "mixed", "claude")
	f.rest("held-move-a", true)
	_, err := f.manager.ExecuteCount(context.Background(), []string{"claude"}, cliproxyexecutor.Request{Model: heldMoveModel}, f.opts("", ""))
	var e *Error
	if !errors.As(err, &e) || e.StatusCode() != 429 || e.Code != "auth_unavailable" || e.Message != heldMoveHint {
		t.Fatalf("count error = %v; want 429 auth_unavailable %q", err, heldMoveHint)
	}
}

type heldMoveDiscoveryExecutor struct {
	confirmingClaudeExecutor
	calls map[string]int
}

func (e *heldMoveDiscoveryExecutor) Execute(_ context.Context, a *Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.calls[a.ID]++
	if a.ID == "held-move-a" {
		return cliproxyexecutor.Response{}, scopeTestError{status: 429, credential: true}
	}
	return cliproxyexecutor.Response{Payload: []byte(`{"ok":true}`)}, nil
}

func (e *heldMoveDiscoveryExecutor) ExecuteStream(ctx context.Context, a *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	_, err := e.Execute(ctx, a, req, opts)
	if err != nil {
		return nil, err
	}
	chunks := make(chan cliproxyexecutor.StreamChunk)
	close(chunks)
	return &cliproxyexecutor.StreamResult{Chunks: chunks}, nil
}

func TestHeldSessionDiscoveryRequestGetsPromptTooLong(t *testing.T) {
	for _, stream := range []bool{false, true} {
		path := "execute"
		if stream {
			path = "stream"
		}
		t.Run(path, func(t *testing.T) {
			for _, name := range []string{"a-confirmed", "b-unconfirmed", "d-compaction"} {
				t.Run(name, func(t *testing.T) {
					f := newHeldMoveFixture(t, "mixed", "claude")
					rules, _ := personalProxyStopRules(t)
					f.manager.runtimeConfig.Store(&internalconfig.Config{OAuthRequestScopedErrors: rules})
					f.manager.SetRetryConfig(0, 1, 0)
					for _, id := range []string{"held-move-a", "held-move-b"} {
						a, _ := f.manager.GetByID(id)
						a.Attributes = map[string]string{AttributeAuthKind: AuthKindOAuth}
						if _, err := f.manager.Update(context.Background(), a); err != nil {
							t.Fatal(err)
						}
					}
					e := &heldMoveDiscoveryExecutor{confirmingClaudeExecutor: confirmingClaudeExecutor{heldPoolExecutor: heldPoolExecutor{"claude"}, confirm: name != "b-unconfirmed"}, calls: map[string]int{}}
					f.manager.RegisterExecutor(e)
					opts := f.opts("", "")
					if name == "d-compaction" {
						opts.Headers.Set("X-Claude-Code-Compaction", "manual")
					}
					req := cliproxyexecutor.Request{Model: heldMoveModel, Payload: opts.OriginalRequest}
					var err error
					if stream {
						_, err = f.manager.ExecuteStream(context.Background(), []string{"claude"}, req, opts)
					} else {
						_, err = f.manager.Execute(context.Background(), []string{"claude"}, req, opts)
					}
					a, _ := f.manager.GetByID("held-move-a")
					if !restsOnPlanLimit(a, time.Now()) {
						t.Fatalf("discovery did not establish plan-limit rest: %+v", a.Quota)
					}
					if e.calls["held-move-a"] != 1 || e.calls["held-move-b"] != 0 {
						t.Fatalf("executor calls = %v; want A=1 B=0", e.calls)
					}
					selector := f.manager.selector.(*SessionAffinitySelector)
					b, _ := f.manager.GetByID("held-move-b")
					if id, ok := selector.heldBinding("mixed", heldMoveModel, opts, []*Auth{a, b}); !ok || id != "held-move-a" {
						t.Fatalf("binding = %q, %v; want held-move-a", id, ok)
					}
					if name == "a-confirmed" {
						wantHeldMovePromptTooLong(t, err)
						f.want(f.opts("", "reactive"), "held-move-b")
					} else if statusCodeFromError(err) != 429 || err.Error() != (scopeTestError{status: 429, credential: true}).Error() {
						t.Fatalf("discovery error = %v; want upstream 429 %s", err, (scopeTestError{}).Error())
					}
				})
			}
		})
	}
}

func TestHeldSessionCompactionMoveFailsClosed(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		for _, name := range []string{"unconfirmed", "empty-payload", "no-method", "false"} {
			t.Run(name, func(t *testing.T) {
				f := newHeldMoveFixture(t, path, "claude")
				f.rest("held-move-a", true)
				o := f.opts("", "manual")
				switch name {
				case "unconfirmed":
					f.manager.RegisterExecutor(confirmingClaudeExecutor{heldPoolExecutor: heldPoolExecutor{"claude"}, confirm: false})
				case "empty-payload":
					o.OriginalRequest = nil
				case "no-method":
					f.manager.RegisterExecutor(heldPoolExecutor{"claude"})
				case "false":
					o.Headers.Set("X-Claude-Code-Compaction", "false")
				}
				if name == "false" {
					f.promptTooLong(o)
				} else {
					f.refusal(o, heldMoveHint)
				}
				if name == "false" || name == "empty-payload" {
					f.promptTooLong(f.opts("", ""))
				} else {
					f.refusal(f.opts("", ""), heldMoveHint)
				}
			})
		}
	})
}
func TestHeldSessionCompactionOnHealthyAccountStays(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		f := newHeldMoveFixture(t, path, "claude")
		f.signals("held-move-a", map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": strconv.FormatInt(time.Now().Add(120*time.Hour).Unix(), 10)})
		f.want(f.opts("", "manual"), "held-move-a")
	})
}
func TestHeldSessionModelBlockRefusesWithoutMove(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		f := newHeldMoveFixture(t, path, "claude")
		f.rest("held-move-a", false)
		f.refusal(f.opts("", "manual"), heldMovePlain)
		f.refusal(f.opts("", ""), heldMovePlain)
	})
}
func TestHeldSessionCompactionMoveSubagents(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		for _, name := range []string{"new-after-move", "bound-before-rest", "new-compaction", "new-plain"} {
			t.Run(name, func(t *testing.T) {
				f := newHeldMoveFixture(t, path, "claude")
				if name == "bound-before-rest" {
					f.want(f.opts("agent-1", ""), "held-move-a")
				}
				f.rest("held-move-a", true)
				switch name {
				case "new-after-move":
					f.want(f.opts("", "manual"), "held-move-b")
					f.want(f.opts("agent-2", ""), "held-move-b")
				case "bound-before-rest":
					f.want(f.opts("", "manual"), "held-move-b")
					f.promptTooLong(f.opts("agent-1", ""))
					f.want(f.opts("agent-1", "reactive"), "held-move-b")
				case "new-compaction":
					f.want(f.opts("agent-3", "manual"), "held-move-b")
					f.promptTooLong(f.opts("", ""))
				case "new-plain":
					f.promptTooLong(f.opts("agent-3", ""))
				}
			})
		}
	})
}
func TestHeldSessionEveryAccountLimitedKeepsRefusal(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		f := newHeldMoveFixture(t, path, "claude")
		f.rest("held-move-a", true)
		f.rest("held-move-b", true)
		for _, kind := range []string{"manual", ""} {
			err := f.refusal(f.opts("", kind), heldMovePlain)
			seconds, e := strconv.Atoi(SafeResponseHeaders(err).Get("Retry-After"))
			if e != nil || seconds < 5390 || seconds > 5401 {
				t.Fatalf("Retry-After = %d, %v; want about 5400", seconds, e)
			}
		}
	})
}
func TestHeldSessionCodexHoldUnchanged(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		f := newHeldMoveFixture(t, path, "codex")
		f.rest("held-move-a", true)
		f.refusal(f.opts("", "manual"), heldMovePlain)
	})
}

func (f *heldMoveFixture) compacted() cliproxyexecutor.Options {
	o := f.opts("", "")
	o.Headers.Set("X-Claude-Code-Context-Compacted", "auto")
	return o
}
func (f *heldMoveFixture) noBReset() {
	f.signals("held-move-b", map[string]string{"Anthropic-Ratelimit-Unified-7d-Reset": ""})
}
func TestHeldSessionContextCompactedRepicksAtFiveHourFloor(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		f := newHeldMoveFixture(t, path, "claude")
		f.signals("held-move-a", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.92", "Anthropic-Ratelimit-Unified-5h-Reset": unixAfter(time.Now(), time.Hour), "Anthropic-Ratelimit-Unified-7d-Utilization": "0.5", "Anthropic-Ratelimit-Unified-7d-Reset": unixAfter(time.Now(), 72*time.Hour)})
		f.signals("held-move-b", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.1", "Anthropic-Ratelimit-Unified-5h-Reset": unixAfter(time.Now(), time.Hour), "Anthropic-Ratelimit-Unified-7d-Utilization": "0.2", "Anthropic-Ratelimit-Unified-7d-Reset": unixAfter(time.Now(), 72*time.Hour)})
		f.want(f.compacted(), "held-move-b")
		f.want(f.opts("", ""), "held-move-b")
	})
}
func TestHeldSessionContextCompactedBelowThresholdsStays(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		f := newHeldMoveFixture(t, path, "claude")
		f.noBReset()
		f.signals("held-move-a", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.89", "Anthropic-Ratelimit-Unified-5h-Reset": unixAfter(time.Now(), time.Hour), "Anthropic-Ratelimit-Unified-7d-Utilization": "0.94", "Anthropic-Ratelimit-Unified-7d-Reset": unixAfter(time.Now(), 72*time.Hour)})
		f.want(f.compacted(), "held-move-a")
	})
}
func TestHeldSessionContextCompactedWeeklyOnly(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		for _, share := range []string{"0.95", "0.94"} {
			t.Run(share, func(t *testing.T) {
				f := newHeldMoveFixture(t, path, "claude")
				f.noBReset()
				f.signals("held-move-a", map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": share, "Anthropic-Ratelimit-Unified-7d-Reset": unixAfter(time.Now(), 24*time.Hour)})
				want := "held-move-a"
				if share == "0.95" {
					want = "held-move-b"
				}
				f.want(f.compacted(), want)
			})
		}
	})
}
func TestHeldSessionContextCompactedStaleSnapshotStays(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		for _, window := range []string{"5h", "7d"} {
			t.Run(window, func(t *testing.T) {
				f := newHeldMoveFixture(t, path, "claude")
				f.noBReset()
				signals := map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "0.5", "Anthropic-Ratelimit-Unified-7d-Reset": unixAfter(time.Now(), 72*time.Hour), "Anthropic-Ratelimit-Unified-5h-Utilization": "0.95", "Anthropic-Ratelimit-Unified-5h-Reset": unixAfter(time.Now(), -time.Minute)}
				if window == "7d" {
					signals = map[string]string{"Anthropic-Ratelimit-Unified-7d-Utilization": "0.97", "Anthropic-Ratelimit-Unified-7d-Reset": unixAfter(time.Now(), -time.Minute)}
				}
				f.signals("held-move-a", signals)
				f.want(f.compacted(), "held-move-a")
			})
		}
	})
}
func TestHeldSessionCompactionNearLimitRunsOnBound(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		f := newHeldMoveFixture(t, path, "claude")
		f.noBReset()
		f.signals("held-move-a", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.95", "Anthropic-Ratelimit-Unified-5h-Reset": unixAfter(time.Now(), time.Hour)})
		o := f.compacted()
		o.Headers.Set("X-Claude-Code-Compaction", "auto")
		f.want(o, "held-move-a")
	})
}
func TestHeldSessionContextCompactedAfterPlanRestRepicks(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		for _, snapshot := range []bool{true, false} {
			name := "without-snapshot"
			if snapshot {
				name = "with-snapshot"
			}
			t.Run(name, func(t *testing.T) {
				f := newHeldMoveFixture(t, path, "claude")
				f.noBReset()
				if snapshot {
					f.signals("held-move-a", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.92", "Anthropic-Ratelimit-Unified-5h-Reset": unixAfter(time.Now(), time.Hour)})
				}
				f.rest("held-move-a", true)
				f.want(f.compacted(), "held-move-b")
			})
		}
	})
}
func TestHeldSessionContextCompactedFailsClosed(t *testing.T) {
	heldMovePaths(t, func(t *testing.T, path string) {
		for _, name := range []string{"a", "b", "c", "d", "e"} {
			t.Run(name, func(t *testing.T) {
				f := newHeldMoveFixture(t, path, "claude")
				f.noBReset()
				o := f.compacted()
				if name == "a" || name == "b" || name == "e" {
					f.signals("held-move-a", map[string]string{"Anthropic-Ratelimit-Unified-5h-Utilization": "0.92", "Anthropic-Ratelimit-Unified-5h-Reset": unixAfter(time.Now(), time.Hour)})
				}
				switch name {
				case "a":
					f.manager.RegisterExecutor(confirmingClaudeExecutor{heldPoolExecutor: heldPoolExecutor{"claude"}, confirm: false})
				case "b":
					o.OriginalRequest = nil
				case "c":
					f.manager.RegisterExecutor(confirmingClaudeExecutor{heldPoolExecutor: heldPoolExecutor{"claude"}, confirm: false})
					f.rest("held-move-a", true)
				case "d":
					f.rest("held-move-a", false)
				case "e":
					o.Headers.Set("X-Claude-Code-Context-Compacted", "false")
				}
				switch name {
				case "c":
					f.refusal(o, heldMoveHint)
				case "d":
					f.refusal(o, heldMovePlain)
				default:
					f.want(o, "held-move-a")
				}
			})
		}
	})
}
