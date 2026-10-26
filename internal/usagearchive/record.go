package usagearchive

import (
	"crypto/sha256"
	"fmt"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

type line struct {
	TS           string   `json:"ts"`
	Provider     string   `json:"provider"`
	AuthIndex    string   `json:"auth_index"`
	Account      string   `json:"account,omitempty"`
	Model        string   `json:"model"`
	Session      string   `json:"session,omitempty"`
	Tier         string   `json:"tier,omitempty"`
	Input        int64    `json:"input"`
	Output       int64    `json:"output"`
	CacheRead    int64    `json:"cache_read"`
	CacheWrite   int64    `json:"cache_write"`
	CacheWrite5m int64    `json:"cache_write_5m"`
	CacheWrite1h int64    `json:"cache_write_1h"`
	Quality      string   `json:"quality,omitempty"`
	Client       string   `json:"client,omitempty"`
	WeeklyUsed   *float64 `json:"weekly_used,omitempty"`
	WeeklyReset  int64    `json:"weekly_reset,omitempty"`
	WeeklyAt     int64    `json:"weekly_at,omitempty"`
}

var validTier = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

func project(r coreusage.Record, client string) (line, bool) {
	if r.Detail.InputTokens == 0 && r.Detail.OutputTokens == 0 && r.Detail.CacheReadTokens == 0 && r.Detail.CacheCreationTokens == 0 {
		return line{}, false
	}
	d := coreusage.EnsureTokenBreakdownForProvider(r.Detail, r.Provider, r.ExecutorType)
	ts := r.RequestedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	l := line{Client: client, TS: ts.UTC().Format(time.RFC3339), Provider: r.Provider, AuthIndex: r.AuthIndex, Model: r.Model, Input: d.TokenBreakdown.Input.UncachedTokens, Output: r.Detail.OutputTokens, CacheRead: r.Detail.CacheReadTokens, CacheWrite: r.Detail.CacheCreationTokens, CacheWrite5m: r.Detail.CacheCreation5mTokens, CacheWrite1h: r.Detail.CacheCreation1hTokens}
	if !d.TokenBreakdown.Valid() || d.TokenBreakdown.Quality == coreusage.TokenAccountingQualityInconsistent {
		l.Input = r.Detail.InputTokens
		l.Quality = "inconsistent"
	}
	if r.AuthType == "oauth" && strings.Contains(r.Source, "@") && r.Source != r.APIKey {
		l.Account = r.Source
	}
	if r.SessionID != "" {
		l.Session = fmt.Sprintf("%x", sha256.Sum256([]byte(r.SessionID)))[:12]
	}
	tier := strings.ToLower(strings.TrimSpace(r.ResponseServiceTier))
	if validTier.MatchString(tier) {
		l.Tier = tier
	}
	if len(r.ResponseHeaderTimes) == 0 {
		return l, true
	}
	var latest time.Time
	for _, at := range r.ResponseHeaderTimes {
		if at.After(latest) {
			latest = at
		}
	}
	var q coreauth.QuotaState
	q.ObserveResponseHeadersForProvider(r.Provider, r.ResponseHeaders, latest)
	_, _, signal, _ := coreauth.WeeklyQuota(q)
	at := r.ResponseHeaderTimes[signal]
	if signal == "" || at.IsZero() {
		return l, true
	}
	observedHeaders := make(http.Header)
	for key, values := range r.ResponseHeaders {
		if r.ResponseHeaderTimes[http.CanonicalHeaderKey(key)].Equal(at) {
			observedHeaders[key] = values
		}
	}
	q = coreauth.QuotaState{}
	q.ObserveResponseHeadersForProvider(r.Provider, observedHeaders, at)
	used, reset, _, ok := coreauth.WeeklyQuota(q)
	if !ok {
		return l, true
	}
	used = math.Round(used*100) / 100
	l.WeeklyUsed, l.WeeklyReset, l.WeeklyAt = &used, reset.Unix(), at.Unix()
	return l, true
}
