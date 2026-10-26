package executor

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// CodexExecutor is a stateless executor for Codex (OpenAI Responses API entrypoint).
// If api_key is unavailable on auth, it falls back to legacy via ClientAdapter.
type CodexExecutor struct {
	cfg *config.Config
}

func NewCodexExecutor(cfg *config.Config) *CodexExecutor { return &CodexExecutor{cfg: cfg} }

func (e *CodexExecutor) Identifier() string { return "codex" }

func (e *CodexExecutor) modelLevelCooling() bool {
	return e != nil && e.cfg != nil && e.cfg.Codex.ModelLevelCooling
}

// SupportsApplyPatch reports the actual executor contract, independent of its provider name.
func (e *CodexExecutor) SupportsApplyPatch() bool { return e != nil }

// codexUsageGuardError is the local refusal of a GPT request while included
// usage is exhausted or unknown. It is request-scoped: no retry, account move,
// or cooldown.
type codexUsageGuardError struct {
	statusErr
}

func (codexUsageGuardError) IsRequestScoped() bool {
	return true
}

const (
	// codexUsageAllowedTTL bounds a usage snapshot whose windows name no reset
	// time. An allowed snapshot goes stale at codexUsageFreshFor first, so this
	// bound decides only for an exhausted snapshot.
	codexUsageAllowedTTL = 5 * time.Minute
	// codexUsageProbeTimeout bounds one usage read. It is an exception to the
	// AGENTS.md rule against timeouts after connection: the
	// guard holds a request until its read returns, so an unbounded read would
	// hold every GPT request on the account. CodexBar bounds the same read at
	// 30 seconds (CodexOAuthUsageFetcher.fetchUsage, timeoutInterval: 30).
	codexUsageProbeTimeout = 15 * time.Second
	// codexUsageFreshFor is how long an allowed read stands without a new read.
	// Another client on the same account (Codex CLI) spends
	// usage the proxy does not see, so an older read may be stale. One minute is
	// CodexBar's shortest usage refresh timer (refreshFrequency oneMinute).
	codexUsageFreshFor = time.Minute
	// codexUsageNearLimitPercent: at or above this used percent, in the last read
	// or in the proxy's own latest response headers, every admission reads first.
	codexUsageNearLimitPercent = 90.0
	// codexDefaultBackendBaseURL is the Codex OAuth inference base; the usage
	// endpoint sits beside it under backend-api.
	codexDefaultBackendBaseURL = "https://chatgpt.com/backend-api/codex"
)

// codexUsageSnapshot is the result of one usage read for one Codex auth.
type codexUsageSnapshot struct {
	allowed     bool
	validUntil  time.Time
	readAt      time.Time
	usedPercent float64
	detail      string
}

type codexUsageEntry struct {
	mu   sync.Mutex
	snap *codexUsageSnapshot
}

// codexUsageEntries holds one entry per Codex auth ID. The entry lock makes
// concurrent requests on one account share a single usage read.
var codexUsageEntries sync.Map

// codexUsageStatus is the part of the usage payload the guard reads. Source:
// Codex CLI's /status call, GET <backend-api>/wham/usage
// (references/codex codex-rs/backend-client/src/client/rate_limit_resets.rs).
type codexUsageStatus struct {
	AccountID string `json:"account_id"`
	RateLimit *struct {
		Allowed         *bool             `json:"allowed"`
		LimitReached    *bool             `json:"limit_reached"`
		PrimaryWindow   *codexUsageWindow `json:"primary_window"`
		SecondaryWindow *codexUsageWindow `json:"secondary_window"`
	} `json:"rate_limit"`
}

type codexUsageWindow struct {
	UsedPercent float64 `json:"used_percent"`
	ResetAt     int64   `json:"reset_at"`
}

// admitCodexIncludedUsage is the included-usage guard. With
// codex.refuse-exhausted-included-usage on, a GPT request on an OAuth Codex
// credential is sent only while the latest usage read allows included usage.
// The first request after start reads usage. An allowed snapshot is reused
// only while it cannot be stale (codexUsageSnapshotMayBeStale): younger than
// codexUsageFreshFor, before its earliest window reset, and below
// codexUsageNearLimitPercent; otherwise the request reads again before it is
// sent. A response that reports an exhausted window closes admission until
// its reset.
// Unknown, failed, mismatched-account, and exhausted states refuse with a
// local 429; nothing is sent upstream.
func admitCodexIncludedUsage(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, now time.Time) error {
	if cfg == nil || !cfg.Codex.RefuseExhaustedIncludedUsage || auth == nil || auth.AuthKind() == cliproxyauth.AuthKindAPIKey {
		return nil
	}
	value, _ := codexUsageEntries.LoadOrStore(auth.ID, &codexUsageEntry{})
	entry := value.(*codexUsageEntry)
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if until, exhausted := helps.CodexIncludedUsageExhausted(auth.Quota, now); exhausted {
		entry.snap = nil
		if !until.IsZero() {
			return codexUsageRefusal("included ChatGPT usage is exhausted until " + until.UTC().Format(time.RFC3339))
		}
	}
	if codexUsageSnapshotMayBeStale(entry.snap, auth.Quota, now) {
		entry.snap = probeCodexIncludedUsage(ctx, cfg, auth, now)
	}
	if entry.snap.allowed {
		return nil
	}
	return codexUsageRefusal(entry.snap.detail)
}

// codexUsageSnapshotMayBeStale reports whether admission must read usage
// again. A failed or unknown read never stands (its validUntil is its read
// time). An exhausted read stands until its reset, because included usage
// only returns at a reset. An allowed read stands only while it is younger
// than codexUsageFreshFor, before its earliest window reset, and below
// codexUsageNearLimitPercent in both the read and the proxy's own latest
// response headers.
func codexUsageSnapshotMayBeStale(snap *codexUsageSnapshot, quota cliproxyauth.QuotaState, now time.Time) bool {
	if snap == nil || !now.Before(snap.validUntil) {
		return true
	}
	if !snap.allowed {
		return false
	}
	if now.Sub(snap.readAt) >= codexUsageFreshFor {
		return true
	}
	return snap.usedPercent >= codexUsageNearLimitPercent || codexObservedUsedPercent(quota) >= codexUsageNearLimitPercent
}

// codexObservedUsedPercent is the highest used percent in the proxy's own
// latest Codex response headers (the canonical keys sdk/cliproxy/auth
// quota_signals.go stores in QuotaState.Signals).
func codexObservedUsedPercent(quota cliproxyauth.QuotaState) float64 {
	highest := 0.0
	for _, key := range []string{"X-Codex-Primary-Used-Percent", "X-Codex-Secondary-Used-Percent"} {
		if value, ok := quota.SignalPercent(key); ok && value > highest {
			highest = value
		}
	}
	return highest
}

// probeCodexIncludedUsage reads usage once. It sends the account's own token
// and account ID, no reserve header, and follows no redirect. Any result
// other than an allowed decision is a refusal.
func probeCodexIncludedUsage(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, now time.Time) *codexUsageSnapshot {
	unknown := func(reason string) *codexUsageSnapshot {
		return &codexUsageSnapshot{validUntil: now, readAt: now, detail: "included ChatGPT usage is unknown (" + reason + ")"}
	}
	// token, baseURL := codexCreds(auth)
	// if strings.TrimSpace(token) == "" {
	// return unknown("no access token")
	// }
	// if strings.TrimSpace(baseURL) == "" {
	// baseURL = codexDefaultBackendBaseURL
	// }
	// usageURL := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(baseURL), "/"), "/codex") + "/wham/usage"
	// if ctx == nil {
	// ctx = context.Background()
	// }
	// req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, usageURL, nil)
	// if errReq != nil {
	// return unknown("usage request could not be built")
	// }
	// // Header set as CodexBar sends it on this read (Authorization, User-Agent,
	// // Accept, ChatGPT-Account-Id); the User-Agent value is the executor's own
	// // Codex identity, the one the same account's inference requests carry.
	// req.Header.Set("Authorization", "Bearer "+token)
	// req.Header.Set("User-Agent", codexUserAgent)
	// req.Header.Set("Accept", "application/json")
	// accountID := ""
	// if auth.Metadata != nil {
	// accountID, _ = auth.Metadata["account_id"].(string)
	// }
	// if accountID != "" {
	// req.Header.Set("ChatGPT-Account-Id", accountID)
	// }
	id, reason := codexBackendIdentity(auth)
	if reason != "" {
		return unknown(reason)
	}
	accountID := id.AccountID
	req, errReq := helps.NewCodexBackendRequest(ctx, id, "usage")
	if errReq != nil {
		return unknown("usage request could not be built")
	}
	resp, errDo := helps.NewUtlsHTTPClient(ctx, cfg, auth, codexUsageProbeTimeout).Do(req)
	if errDo != nil {
		return unknown("usage read failed")
	}
	defer func() { _ = resp.Body.Close() }()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errRead != nil || resp.StatusCode != http.StatusOK {
		return unknown(fmt.Sprintf("usage read returned status %d", resp.StatusCode))
	}
	var status codexUsageStatus
	if errJSON := json.Unmarshal(body, &status); errJSON != nil || status.RateLimit == nil || status.RateLimit.Allowed == nil || status.RateLimit.LimitReached == nil {
		return unknown("usage read carried no rate_limit decision")
	}
	if accountID != "" && status.AccountID != "" && status.AccountID != accountID {
		return unknown("usage read answered for a different account")
	}
	var reset time.Time
	usedPercent := 0.0
	for _, window := range []*codexUsageWindow{status.RateLimit.PrimaryWindow, status.RateLimit.SecondaryWindow} {
		if window == nil {
			continue
		}
		if window.UsedPercent > usedPercent {
			usedPercent = window.UsedPercent
		}
		if window.ResetAt <= 0 {
			continue
		}
		at := time.Unix(window.ResetAt, 0)
		if at.After(now) && (reset.IsZero() || at.Before(reset)) {
			reset = at
		}
	}
	validUntil := now.Add(codexUsageAllowedTTL)
	if !reset.IsZero() {
		validUntil = reset
	}
	if *status.RateLimit.Allowed && !*status.RateLimit.LimitReached {
		return &codexUsageSnapshot{allowed: true, validUntil: validUntil, readAt: now, usedPercent: usedPercent}
	}
	detail := "included ChatGPT usage is exhausted (no reset time reported)"
	if !reset.IsZero() {
		detail = "included ChatGPT usage is exhausted until " + reset.UTC().Format(time.RFC3339)
	}
	return &codexUsageSnapshot{validUntil: validUntil, readAt: now, usedPercent: usedPercent, detail: detail}
}

// codexUsageRefusal builds the local 429 body.
func codexUsageRefusal(detail string) error {
	body, _ := json.Marshal(map[string]any{"error": map[string]any{
		"type":    "rate_limit_error",
		"code":    "included_usage_exhausted",
		"message": "request refused by proxy: " + detail + "; nothing was sent upstream",
	}})
	return codexUsageGuardError{statusErr{code: http.StatusTooManyRequests, msg: string(body)}}
}

// codexBackendIdentity resolves the executor's active token, including SIWC.
func codexBackendIdentity(auth *cliproxyauth.Auth) (helps.CodexBackendIdentity, string) {
	token, baseURL := codexCreds(auth)
	if strings.TrimSpace(token) == "" {
		return helps.CodexBackendIdentity{}, "no access token"
	}
	if strings.TrimSpace(baseURL) == "" {
		baseURL = codexDefaultBackendBaseURL
	}
	id := helps.CodexBackendIdentity{BaseURL: baseURL, Token: token, UserAgent: codexUserAgent}
	if auth != nil && auth.Metadata != nil {
		id.AccountID, _ = auth.Metadata["account_id"].(string)
	}
	return id, ""
}

// ReadCodexAccountUsage reads the account's plan, then its reset credits.
// A failed second read preserves the first result and sets ResetDetailsErr.
// The 15-second bound is the user's explicit 2026-10-25 exception to AGENTS.md:60.
func ReadCodexAccountUsage(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) (helps.CodexAccountUsage, error) {
	id, reason := codexBackendIdentity(auth)
	if reason != "" {
		return helps.CodexAccountUsage{}, fmt.Errorf("request failed")
	}
	// Only the provider origin or a loopback fixture receives this button read.
	origin, errOrigin := url.Parse(id.BaseURL)
	if errOrigin != nil {
		return helps.CodexAccountUsage{}, fmt.Errorf("request failed")
	}
	ip := net.ParseIP(origin.Hostname())
	loopback := ip != nil && ip.IsLoopback()
	if origin.User != nil || (!loopback && (origin.Scheme != "https" || origin.Hostname() != "chatgpt.com")) {
		return helps.CodexAccountUsage{}, fmt.Errorf("request failed")
	}
	body, err := helps.ReadCodexBackend(ctx, cfg, auth, id, "usage", codexUsageProbeTimeout)
	if err != nil {
		return helps.CodexAccountUsage{}, err
	}
	result, err := helps.ParseCodexAccountUsage(body, id.AccountID)
	if err != nil {
		return result, err
	}
	body, err = helps.ReadCodexBackend(ctx, cfg, auth, id, "rate-limit-reset-credits", codexUsageProbeTimeout)
	if err == nil {
		var n int
		var expiries []*time.Time
		n, expiries, err = helps.ParseCodexResetCredits(body)
		if err == nil {
			result.ResetsAvailable = n
			result.ResetExpiries = expiries
		}
	}
	result.ResetDetailsErr = err
	return result, nil
}
