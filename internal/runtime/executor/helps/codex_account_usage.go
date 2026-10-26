package helps

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// CodexBackendIdentity is the account identity shared by guard and button reads.
type CodexBackendIdentity struct{ BaseURL, Token, AccountID, UserAgent string }

// CodexBackendStatusError exposes only an upstream HTTP status.
type CodexBackendStatusError struct{ Status int }

func (e CodexBackendStatusError) Error() string { return fmt.Sprintf("HTTP %d", e.Status) }

// StatusCode returns the upstream status without headers or body text.
func (e CodexBackendStatusError) StatusCode() int { return e.Status }

// CodexAccountUsage contains only the allowlisted plan and reset facts.
type CodexAccountUsage struct {
	Plan            string
	ResetsAvailable int
	ResetExpiries   []*time.Time
	ResetDetailsErr error
}

// NewCodexBackendRequest builds one of the two read-only backend GETs.
// It accepts nil context and rejects every other path, including consume.
func NewCodexBackendRequest(ctx context.Context, id CodexBackendIdentity, path string) (*http.Request, error) {
	if path != "usage" && path != "rate-limit-reset-credits" {
		return nil, fmt.Errorf("request failed")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	url := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSpace(id.BaseURL), "/"), "/codex") + "/wham/" + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("request failed")
	}
	req.Header.Set("Authorization", "Bearer "+id.Token)
	req.Header.Set("User-Agent", id.UserAgent)
	req.Header.Set("Accept", "application/json")
	if id.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", id.AccountID)
	}
	return req, nil
}

// ReadCodexBackend uses the account's inference transport and follows no redirect.
// It reads at most 1 MiB and exposes only sanitized request/status errors.
func ReadCodexBackend(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, id CodexBackendIdentity, path string, timeout time.Duration) ([]byte, error) {
	req, err := NewCodexBackendRequest(ctx, id, path)
	if err != nil {
		return nil, err
	}
	resp, errDo := NewUtlsHTTPClient(ctx, cfg, auth, timeout).Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("request failed")
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Warn("usage accounts: close response body failed")
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, CodexBackendStatusError{resp.StatusCode}
	}
	body, errReadAll := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if errReadAll != nil {
		return nil, fmt.Errorf("request failed")
	}
	return body, nil
}

// ParseCodexAccountUsage keeps the plan and summary reset count, rejecting
// a response that names another account and discarding all other fields.
func ParseCodexAccountUsage(body []byte, accountID string) (CodexAccountUsage, error) {
	var payload struct {
		AccountID string `json:"account_id"`
		Plan      string `json:"plan_type"`
		Credits   struct {
			Available int `json:"available_count"`
		} `json:"rate_limit_reset_credits"`
	}
	if json.Unmarshal(body, &payload) != nil {
		return CodexAccountUsage{}, fmt.Errorf("request failed")
	}
	if accountID != "" && payload.AccountID != "" && payload.AccountID != accountID {
		return CodexAccountUsage{}, fmt.Errorf("answered for a different account")
	}
	return CodexAccountUsage{Plan: payload.Plan, ResetsAvailable: payload.Credits.Available}, nil
}

// ParseCodexResetCredits keeps only available-credit expiries, soonest first.
// An invalid expiry rejects the details response; no credit identifier survives.
func ParseCodexResetCredits(body []byte) (int, []*time.Time, error) {
	var payload struct {
		Available int `json:"available_count"`
		Credits   []struct {
			Status  string     `json:"status"`
			Expires *time.Time `json:"expires_at"`
		} `json:"credits"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.Available < 0 {
		return 0, nil, fmt.Errorf("reset details unreadable")
	}
	var expiries []*time.Time
	for _, credit := range payload.Credits {
		if credit.Status == "available" {
			expiries = append(expiries, credit.Expires)
		}
	}
	sort.SliceStable(expiries, func(i, j int) bool {
		if expiries[i] == nil {
			return false
		}
		return expiries[j] == nil || expiries[i].Before(*expiries[j])
	})
	if len(expiries) > payload.Available {
		expiries = expiries[:payload.Available]
	}
	return payload.Available, expiries, nil
}
