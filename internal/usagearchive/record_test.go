package usagearchive

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func fixtureRecord() coreusage.Record {
	return coreusage.Record{Provider: "claude", AuthType: "oauth", AuthIndex: "0123456789abcdef", Source: "a@example.test", Model: "claude-opus-5-5", RequestedAt: time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC), Detail: coreusage.Detail{InputTokens: 10, OutputTokens: 20, CacheReadTokens: 30, CacheCreationTokens: 40, CacheCreation5mTokens: 15, CacheCreation1hTokens: 25}}
}
func projected(t *testing.T, r coreusage.Record) line {
	t.Helper()
	l, ok := project(r, "")
	if !ok {
		t.Fatal("want line, got no line")
	}
	return l
}
func bytesLine(t *testing.T, l line) string {
	t.Helper()
	b, e := json.Marshal(l)
	if e != nil {
		t.Fatal(e)
	}
	return string(b)
}
func TestProjectWritesOnlyAllowlistedKeys(t *testing.T) {
	r := fixtureRecord()
	r.APIKey = "sk-fake-proxy-0000"
	rawUserAgent := "claude-cli/2.1.310 (external, cli)"
	r.ResponseHeaders = http.Header{
		"Secret": {"sk-fake-upstream-1111"}, "User-Agent": {rawUserAgent},
		"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.4033"},
		"Anthropic-Ratelimit-Unified-7d-Reset":       {"1761900000"},
	}
	r.ResponseHeaderTimes = make(map[string]time.Time)
	for key := range r.ResponseHeaders {
		r.ResponseHeaderTimes[key] = time.Unix(1761896220, 0)
	}
	r.Fail = coreusage.Failure{Body: "sk-fake-upstream-1111"}
	r.SessionID = "fake-session"
	r.ParentSessionID = "fake-parent"
	r.TraceID = "fake-trace"
	r.EffectiveServiceTier = "priority"
	r.ResponseServiceTier = "fast"
	r.Detail.TotalTokens = 1
	l, ok := project(r, "claude-code-cli")
	if !ok {
		t.Fatal("want line, got no line")
	}
	b := bytesLine(t, l)
	var m map[string]any
	json.Unmarshal([]byte(b), &m)
	var keys []string
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	want := strings.Fields("account auth_index cache_read cache_write cache_write_1h cache_write_5m client input model output provider quality session tier ts weekly_at weekly_reset weekly_used")
	sort.Strings(want)
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("keys=%v want=%v", keys, want)
	}
	for _, s := range []string{r.APIKey, "sk-fake-upstream-1111", r.SessionID, r.ParentSessionID, r.TraceID, rawUserAgent} {
		if strings.Contains(b, s) {
			t.Errorf("secret in line: %q", s)
		}
	}
}
func TestProjectOmitsAccountWhenSourceIsClientKey(t *testing.T) {
	for _, kind := range []string{"oauth", "apikey"} {
		r := fixtureRecord()
		r.AuthType = kind
		r.Source = "sk-fake-upstream-1111"
		r.APIKey = r.Source
		l := projected(t, r)
		if l.Account != "" || strings.Contains(bytesLine(t, l), r.APIKey) {
			t.Fatal("client key retained")
		}
	}
	r := fixtureRecord()
	r.APIKey = r.Source
	if projected(t, r).Account != "" {
		t.Fatal("source equal to client key retained")
	}
}
func TestProjectInputIsUncachedForClaudeAndCodex(t *testing.T) {
	for _, tc := range []struct {
		p           string
		input, want int64
	}{{"claude", 10, 10}, {"codex", 130, 60}} {
		r := fixtureRecord()
		r.Provider = tc.p
		r.Detail.InputTokens = tc.input
		l := projected(t, r)
		if l.Input != tc.want {
			t.Errorf("%s input=%d want=%d", tc.p, l.Input, tc.want)
		}
	}
}
func TestProjectKeepsRawInputWhenAccountingInconsistent(t *testing.T) {
	r := fixtureRecord()
	r.Detail.TotalTokens = 1
	l := projected(t, r)
	if l.Input != 10 || l.Quality != "inconsistent" {
		t.Fatalf("line=%+v", l)
	}
}
func TestProjectStoresSessionHash(t *testing.T) {
	r := fixtureRecord()
	r.SessionID = "fake-session"
	want := fmt.Sprintf("%x", sha256.Sum256([]byte(r.SessionID)))[:12]
	for i := 0; i < 2; i++ {
		if l := projected(t, r); l.Session != want {
			t.Fatalf("session=%q want=%q", l.Session, want)
		}
	}
	r.SessionID = ""
	if strings.Contains(bytesLine(t, projected(t, r)), `"session"`) {
		t.Fatal("empty session stored")
	}
}
func TestProjectStoresUpstreamTier(t *testing.T) {
	for _, tc := range []struct{ v, want string }{{" Fast", "fast"}, {"", ""}, {strings.Repeat("a", 40), ""}, {"fa st", ""}} {
		r := fixtureRecord()
		r.ResponseServiceTier = tc.v
		r.EffectiveServiceTier = "priority"
		if got := projected(t, r).Tier; got != tc.want {
			t.Errorf("tier=%q want=%q", got, tc.want)
		}
	}
}
func TestProjectSkipsZeroTokenRecords(t *testing.T) {
	for _, r := range []coreusage.Record{{}, {Provider: "claude", Failed: true}, {Provider: "codex", Detail: coreusage.Detail{ReasoningTokens: 3}}, {Provider: "codex", Detail: coreusage.Detail{TotalTokens: 3}}} {
		if _, ok := project(r, ""); ok {
			t.Errorf("zero-token record gives a line: %+v", r)
		}
	}
}

func TestProjectWritesClaudeWeeklyQuota(t *testing.T) {
	r := fixtureRecord()
	r.ResponseHeaders = http.Header{
		"Anthropic-Ratelimit-Unified-7d-Utilization": {"0.4033"},
		"Anthropic-Ratelimit-Unified-7d-Reset":       {"1761900000"},
	}
	r.ResponseHeaderTimes = make(map[string]time.Time)
	for key := range r.ResponseHeaders {
		r.ResponseHeaderTimes[key] = time.Unix(1761896220, 0)
	}
	l := projected(t, r)
	if l.WeeklyUsed == nil || *l.WeeklyUsed != 40.33 || l.WeeklyReset != 1761900000 || l.WeeklyAt != 1761896220 {
		t.Fatalf("quota line=%s, want used=40.33 reset=1761900000 at=1761896220", bytesLine(t, l))
	}
}

func TestProjectWritesZeroWeeklyUsed(t *testing.T) {
	r := fixtureRecord()
	r.ResponseHeaders = http.Header{
		"Anthropic-Ratelimit-Unified-7d-Utilization": {"0"},
		"Anthropic-Ratelimit-Unified-7d-Reset":       {"1761900000"},
	}
	r.ResponseHeaderTimes = make(map[string]time.Time)
	for key := range r.ResponseHeaders {
		r.ResponseHeaderTimes[key] = time.Unix(1761896220, 0)
	}
	l := projected(t, r)
	if l.WeeklyUsed == nil || *l.WeeklyUsed != 0 || !strings.Contains(bytesLine(t, l), `"weekly_used":0`) {
		t.Fatalf("zero usage missing: %s", bytesLine(t, l))
	}
	if l.WeeklyReset != 1761900000 || l.WeeklyAt != 1761896220 {
		t.Fatalf("zero usage lacks reset/time: %s", bytesLine(t, l))
	}
}

func TestProjectKeepsWeeklyWindowTimeAcrossOneWindowEvent(t *testing.T) {
	r := fixtureRecord()
	r.Provider = "codex"
	day := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	r.RequestedAt = day.Add(15 * time.Minute)
	weeklyAt := day.Add(17 * time.Minute)
	primaryAt := day.Add(25 * time.Minute)
	r.ResponseHeaders = http.Header{
		"X-Codex-Secondary-Window-Minutes":      {"10080"},
		"X-Codex-Secondary-Used-Percent":        {"11"},
		"X-Codex-Secondary-Reset-After-Seconds": {"2580"},
		"X-Codex-Primary-Window-Minutes":        {"300"},
		"X-Codex-Primary-Used-Percent":          {"40"},
		"X-Codex-Primary-Reset-After-Seconds":   {"600"},
	}
	r.ResponseHeaderTimes = make(map[string]time.Time)
	for key := range r.ResponseHeaders {
		if strings.HasPrefix(key, "X-Codex-Secondary-") {
			r.ResponseHeaderTimes[key] = weeklyAt
		} else {
			r.ResponseHeaderTimes[key] = primaryAt
		}
	}
	l := projected(t, r)
	if l.WeeklyUsed == nil || *l.WeeklyUsed != 11 || l.WeeklyReset != day.Add(time.Hour).Unix() || l.WeeklyAt != weeklyAt.Unix() || l.TS != r.RequestedAt.Format(time.RFC3339) {
		t.Fatalf("quota line=%s, want used=11 reset=%d at=%d ts=%s", bytesLine(t, l), day.Add(time.Hour).Unix(), weeklyAt.Unix(), r.RequestedAt.Format(time.RFC3339))
	}
}

func TestProjectUsesResetFromWeeklyObservation(t *testing.T) {
	r := fixtureRecord()
	r.Provider = "codex"
	day := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	observed := day.Add(61 * time.Minute)
	r.ResponseHeaders = http.Header{
		"X-Codex-Primary-Reset-At":            {fmt.Sprint(day.Add(time.Hour).Unix())},
		"X-Codex-Primary-Used-Percent":        {"5"},
		"X-Codex-Primary-Window-Minutes":      {"10080"},
		"X-Codex-Primary-Reset-After-Seconds": {"604740"},
	}
	r.ResponseHeaderTimes = make(map[string]time.Time)
	for key := range r.ResponseHeaders {
		r.ResponseHeaderTimes[key] = observed
	}
	r.ResponseHeaderTimes["X-Codex-Primary-Reset-At"] = day
	l := projected(t, r)
	wantReset := observed.Add(604740 * time.Second).Unix()
	if l.WeeklyReset != wantReset {
		t.Errorf("weekly_reset=%d, want %d (same-observation relative reset)", l.WeeklyReset, wantReset)
	}
	if l.WeeklyUsed == nil || *l.WeeklyUsed != 5 || l.WeeklyAt != observed.Unix() {
		t.Errorf("weekly sample=%s, want used=5 at=%d", bytesLine(t, l), observed.Unix())
	}
}

func TestProjectOmitsIncompleteQuota(t *testing.T) {
	observed := time.Unix(1761896220, 0)
	for _, name := range []string{"no times", "no used-key time", "no reset", "older reset"} {
		t.Run(name, func(t *testing.T) {
			r := fixtureRecord()
			r.Provider = "codex"
			r.ResponseHeaders = http.Header{
				"X-Codex-Primary-Window-Minutes": {"10080"},
				"X-Codex-Primary-Used-Percent":   {"5"},
				"X-Codex-Primary-Reset-At":       {"1761900000"},
			}
			r.ResponseHeaderTimes = make(map[string]time.Time)
			for key := range r.ResponseHeaders {
				r.ResponseHeaderTimes[key] = observed
			}
			switch name {
			case "no times":
				r.ResponseHeaderTimes = nil
			case "no used-key time":
				delete(r.ResponseHeaderTimes, "X-Codex-Primary-Used-Percent")
			case "no reset":
				delete(r.ResponseHeaders, "X-Codex-Primary-Reset-At")
				delete(r.ResponseHeaderTimes, "X-Codex-Primary-Reset-At")
			case "older reset":
				r.ResponseHeaderTimes["X-Codex-Primary-Reset-At"] = observed.Add(-time.Minute)
			}
			l := projected(t, r)
			var fields map[string]json.RawMessage
			if err := json.Unmarshal([]byte(bytesLine(t, l)), &fields); err != nil {
				t.Fatal(err)
			}
			for _, key := range []string{"weekly_used", "weekly_reset", "weekly_at"} {
				if value, ok := fields[key]; ok {
					t.Errorf("incomplete quota includes %s=%s", key, value)
				}
			}
			for _, key := range []string{"input", "output", "cache_read", "cache_write", "cache_write_5m", "cache_write_1h"} {
				if _, ok := fields[key]; !ok {
					t.Errorf("token field %s missing", key)
				}
			}
			if l.Input != 10 || l.Output != 20 {
				t.Errorf("token values lost: %+v", l)
			}
		})
	}
}
