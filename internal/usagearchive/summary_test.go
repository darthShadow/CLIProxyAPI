package usagearchive

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func summaryNow() time.Time {
	return time.Date(2026, 10, 25, 16, 0, 0, 0, time.FixedZone("host", 19800))
}
func fixtureLine(day int, p, account, model, session string) line {
	return line{TS: summaryNow().AddDate(0, 0, day).UTC().Format(time.RFC3339), Provider: p, AuthIndex: "0123456789abcdef", Account: account, Model: model, Session: session, Input: 10, Output: 20, CacheRead: 30, CacheWrite: 40, CacheWrite5m: 15, CacheWrite1h: 25}
}
func writeLines(t *testing.T, dir string, ls ...line) {
	t.Helper()
	for _, l := range ls {
		at, e := time.Parse(time.RFC3339, l.TS)
		if e != nil {
			t.Fatal(e)
		}
		b, e := json.Marshal(l)
		if e != nil {
			t.Fatal(e)
		}
		f, e := os.OpenFile(archivePath(dir, at.In(summaryNow().Location())), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		if _, e = f.Write(append(b, '\n')); e != nil {
			t.Fatal(e)
		}
		if e = f.Close(); e != nil {
			t.Fatal(e)
		}
	}
}
func summarize(t *testing.T, dir string) Summary {
	t.Helper()
	s, e := Summarize(dir, summaryNow())
	if e != nil {
		t.Fatal(e)
	}
	return s
}
func groupFor(t *testing.T, gs []Group, p string) Group {
	t.Helper()
	for _, g := range gs {
		if g.Provider == p {
			return g
		}
	}
	t.Fatalf("missing %s group: %+v", p, gs)
	return Group{}
}
func closeFloat(t *testing.T, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-10 {
		t.Errorf("got %.12g want %.12g", got, want)
	}
}
func TestSummarizeGroupsByAccountModelAndDay(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, dir, fixtureLine(0, "claude", "z@example.test", "m1", ""), fixtureLine(-1, "claude", "z@example.test", "m1", ""), fixtureLine(0, "claude", "a@example.test", "m2", ""), fixtureLine(0, "codex", "", "gpt-6-sol", ""))
	s := summarize(t, dir)
	if len(s.Groups) != 3 || len(s.Daily) != 4 {
		t.Fatalf("groups=%+v daily=%+v", s.Groups, s.Daily)
	}
	g := s.Groups[1]
	if g.Requests != 2 || g.Input != 20 || g.Output != 40 || g.CacheRead != 60 || g.CacheWrite5m != 30 || g.CacheWrite1h != 50 || g.Account != "z@example.test" {
		t.Errorf("group=%+v", g)
	}
	if s.Groups[2].Account != "credential 0123456789abcdef" {
		t.Error("fallback account missing")
	}
	if s.Daily[0].Day != "2026-10-25" || s.Daily[3].Day != "2026-10-24" {
		t.Errorf("daily sort=%+v", s.Daily)
	}
}
func TestSummarizeSkipsBadLinesAndUnterminatedTail(t *testing.T) {
	dir := t.TempDir()
	l := fixtureLine(0, "claude", "a", "m", "")
	b, _ := json.Marshal(l)
	raw := "{\"torn\":\ninvalid\n" + strings.Repeat("x", 70000) + "\n" + string(b) + "\n" + string(b)
	os.WriteFile(archivePath(dir, summaryNow()), []byte(raw), 0600)
	s := summarize(t, dir)
	if s.Unreadable != 3 || s.All.Requests != 1 {
		t.Fatalf("unreadable=%d requests=%d", s.Unreadable, s.All.Requests)
	}
}
func TestSummarizeWindowAndNoTTLSplit(t *testing.T) {
	dir := t.TempDir()
	var ls []line
	for _, d := range []int{-30, -29, 0, 1} {
		ls = append(ls, fixtureLine(d, "claude", "a", "m", ""))
	}
	valid := fixtureLine(0, "codex", "b", "m", "")
	valid.CacheWrite = 100
	valid.CacheWrite5m = 0
	valid.CacheWrite1h = 0
	ls = append(ls, valid)
	for _, split := range [][2]int64{{0, 150}, {-10, 0}, {0, -1}} {
		bad := valid
		bad.CacheWrite5m = split[0]
		bad.CacheWrite1h = split[1]
		ls = append(ls, bad)
	}
	writeLines(t, dir, ls...)
	s := summarize(t, dir)
	if s.All.Requests != 6 || s.All.Unverified != 3 || s.All.NoTTLSplit != 100 {
		t.Fatalf("totals=%+v", s.All)
	}
	if s.Start.Format("2006-01-02") != "2026-09-26" || s.End.Format("2006-01-02") != "2026-10-25" {
		t.Fatalf("window=%v..%v", s.Start, s.End)
	}
}
func TestSummarizeMissingVersusFailedReads(t *testing.T) {
	dir := t.TempDir()
	if _, e := Summarize(filepath.Join(dir, "missing"), summaryNow()); e != nil {
		t.Error(e)
	}
	regular := filepath.Join(dir, "file")
	os.WriteFile(regular, nil, 0600)
	if s, e := Summarize(regular, summaryNow()); e == nil || s.All.Requests != 0 {
		t.Errorf("regular directory position: summary=%+v error=%v", s, e)
	}
	os.Mkdir(archivePath(dir, summaryNow()), 0700)
	if s, e := Summarize(dir, summaryNow()); e == nil || s.All.Requests != 0 {
		t.Errorf("read directory as file: summary=%+v error=%v", s, e)
	}
}
func TestSummarizeCountsSessionsPerProviderAndDay(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, dir, fixtureLine(0, "claude", "a", "m", "A"), fixtureLine(-1, "claude", "a", "m", "A"), fixtureLine(0, "claude", "a", "m", "B"), fixtureLine(0, "codex", "b", "m", "C"), fixtureLine(0, "claude", "a", "m", ""), fixtureLine(0, "claude", "a", "m", ""))
	s := summarize(t, dir)
	if groupFor(t, s.Providers, "claude").Sessions != 2 || groupFor(t, s.Providers, "codex").Sessions != 1 || s.All.WithoutSession != 2 {
		t.Fatalf("providers=%+v all=%+v", s.Providers, s.All)
	}
	for _, g := range s.Days {
		want := int64(1)
		if g.Provider == "claude" && g.Day == "2026-10-25" {
			want = 2
		}
		if g.Sessions != want {
			t.Errorf("day=%+v want sessions=%d", g, want)
		}
	}
}
func TestSummarizeProviderTotalsAndProcessedTokens(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, dir, fixtureLine(0, "claude", "a", "m", ""), fixtureLine(0, "codex", "b", "m", ""), fixtureLine(0, "codex", "b", "m", ""))
	s := summarize(t, dir)
	if s.All.Processed != 300 || groupFor(t, s.Providers, "claude").Processed != 100 || groupFor(t, s.Providers, "codex").Processed != 200 {
		t.Fatalf("providers=%+v all=%+v", s.Providers, s.All)
	}
	var share float64
	for _, g := range s.Providers {
		share += g.TokenShare
	}
	closeFloat(t, share, 100)
}
func TestSummarizeCostByProviderDayTypeAndTier(t *testing.T) {
	dir := t.TempDir()
	standard := fixtureLine(0, "claude", "a", "claude-opus-5-5", "")
	fast := standard
	fast.Tier = "fast"
	codex := fixtureLine(-1, "codex", "b", "gpt-6.1-sol", "")
	codex.CacheWrite5m = 0
	codex.CacheWrite1h = 0
	unknown := fixtureLine(0, "claude", "a", "claude-3-7-sonnet", "")
	ls := []line{standard, fast, codex, unknown}
	for _, split := range [][2]int64{{0, 150}, {-1, 0}, {0, -1}} {
		l := standard
		l.CacheWrite = 100
		l.CacheWrite5m = split[0]
		l.CacheWrite1h = split[1]
		ls = append(ls, l)
	}
	writeLines(t, dir, ls...)
	s := summarize(t, dir)
	closeFloat(t, s.All.Cost.Total, .002486)
	closeFloat(t, groupFor(t, s.Providers, "claude").Cost.Total, .002163)
	closeFloat(t, groupFor(t, s.Providers, "codex").Cost.Total, .000323)
	closeFloat(t, s.All.Cost.Input+s.All.Cost.CacheRead+s.All.Cost.CacheWrite+s.All.Cost.Output, s.All.Cost.Total)
	closeFloat(t, s.All.Cost.Premium, .000721)
	closeFloat(t, s.All.Cost.Savings, 30*(3.8+7.6+1.9)/1e6)
	if s.All.Unpriced != 4 {
		t.Errorf("unpriced=%d want=4", s.All.Unpriced)
	}
	var share float64
	for _, g := range s.Providers {
		share += g.CostShare
	}
	closeFloat(t, share, 100)
	var unpriced Group
	for _, g := range s.Tiers {
		if g.Tier == "no price" {
			unpriced = g
		}
	}
	if unpriced.Requests != 4 || unpriced.Unpriced != 4 || unpriced.Cost.Total != 0 || unpriced.Processed != 580 {
		t.Errorf("no price row=%+v", unpriced)
	}
	for _, g := range s.Days {
		if g.Day == "2026-10-24" {
			closeFloat(t, g.Cost.Total, .000323)
		} else {
			closeFloat(t, g.Cost.Total, .002163)
		}
	}
}
func TestSummarizeAccountTierAndPartialDayAggregates(t *testing.T) {
	dir := t.TempDir()
	a := fixtureLine(0, "claude", "a", "claude-opus-5-5", "")
	b := a
	b.Model = "claude-sonnet-5-5"
	c := fixtureLine(-1, "codex", "b", "gpt-6-sol", "")
	c.CacheWrite1h = 0
	c.CacheWrite5m = 0
	writeLines(t, dir, a, b, c)
	s := summarize(t, dir)
	if len(s.Accounts) != 2 || len(s.AllTiers) != 1 {
		t.Fatalf("new aggregates empty: accounts=%+v tiers=%+v", s.Accounts, s.AllTiers)
	}
	g := groupFor(t, s.Accounts, "claude")
	if g.Input != 20 || g.CacheWrite != 80 || g.Requests != 2 {
		t.Errorf("account=%+v", g)
	}
	var cost float64
	for _, m := range s.Groups {
		if m.Provider == "claude" {
			cost += m.Cost.Total
		}
		if m.CacheWrite != 40 {
			t.Errorf("model cache write=%d", m.CacheWrite)
		}
	}
	closeFloat(t, g.Cost.Total, cost)
	var tierCost float64
	for _, g := range s.Tiers {
		tierCost += g.Cost.Total
	}
	closeFloat(t, s.AllTiers[0].Cost.Total, tierCost)
	if !s.Days[0].Partial {
		t.Error("today is not partial")
	}
	older := t.TempDir()
	writeLines(t, older, fixtureLine(-1, "claude", "a", "claude-opus-5-5", ""))
	old := summarize(t, older)
	if len(old.Days) == 0 || old.Days[0].Partial {
		t.Error("yesterday is partial")
	}
}
func TestSummarizeAllProviderShares(t *testing.T) {
	dir := t.TempDir()
	writeLines(t, dir, fixtureLine(0, "claude", "a", "claude-opus-5-5", ""))
	s := summarize(t, dir)
	closeFloat(t, s.All.TokenShare, 100)
	closeFloat(t, s.All.CostShare, 100)
}

func TestSummarizeSkipsProcessStartBlankLines(t *testing.T) {
	p, dir := archiveSetup(t)
	p.HandleUsage(context.Background(), fixtureRecord())
	second := &archivePlugin{now: p.now}
	second.HandleUsage(context.Background(), fixtureRecord())
	s, err := Summarize(dir, p.now())
	if err != nil {
		t.Fatal(err)
	}
	if s.Unreadable != 0 || s.All.Requests != 2 {
		t.Fatalf("process-start blank lines: unreadable=%d requests=%d want 0 and 2", s.Unreadable, s.All.Requests)
	}
}

func TestSummarizeCountsTornFragmentAfterProcessStart(t *testing.T) {
	p, dir := archiveSetup(t)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(archivePath(dir, p.now()), []byte(`{"torn":`), 0600); err != nil {
		t.Fatal(err)
	}
	p.HandleUsage(context.Background(), fixtureRecord())
	s, err := Summarize(dir, p.now())
	if err != nil {
		t.Fatal(err)
	}
	if s.Unreadable != 1 || s.All.Requests != 1 {
		t.Fatalf("torn fragment: unreadable=%d requests=%d want 1 and 1", s.Unreadable, s.All.Requests)
	}
}

func TestSummarizeReadsOldLinesUnchanged(t *testing.T) {
	dir := t.TempDir()
	raw := `{"ts":"2026-10-25T10:00:00Z","provider":"claude","auth_index":"0123456789abcdef","account":"a@example.test","model":"claude-opus-5-5","input":10,"output":20,"cache_read":30,"cache_write":40,"cache_write_5m":15,"cache_write_1h":25}`
	if err := os.WriteFile(archivePath(dir, summaryNow()), []byte(raw+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var decoded line
	if err := json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Client != "" || decoded.WeeklyUsed != nil || decoded.WeeklyReset != 0 || decoded.WeeklyAt != 0 {
		t.Fatalf("old line invents optional fields: %+v", decoded)
	}
	s := summarize(t, dir)
	if len(s.Quota) != 0 {
		t.Fatalf("old line invents quota: %v", s.Quota)
	}
	if len(s.Groups) != 1 || len(s.Daily) != 1 || len(s.Providers) != 1 || s.All.Requests != 1 || s.All.Input != 10 || s.All.Output != 20 || s.All.CacheRead != 30 || s.All.CacheWrite != 40 || s.All.CacheWrite5m != 15 || s.All.CacheWrite1h != 25 || s.All.Processed != 100 {
		t.Fatalf("old line totals=%+v groups=%+v daily=%+v", s.All, s.Groups, s.Daily)
	}
	if s.Groups[0].Account != "a@example.test" || s.Groups[0].Model != "claude-opus-5-5" || s.Daily[0].Day != "2026-10-25" {
		t.Fatalf("old line grouping=%+v daily=%+v", s.Groups, s.Daily)
	}
}

func TestSummarizeBinsWeeklySamples(t *testing.T) {
	day := time.Date(2026, 10, 25, 10, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		minutes   [4]int
		used      [4]float64
		wantFirst float64
	}{
		{"latest observation", [4]int{8, 2, 5, 17}, [4]float64{8, 2, 5, 17}, 8},
		{"later line on tie", [4]int{8, 8, 2, 17}, [4]float64{8, 9, 2, 17}, 9},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var ls []line
			reset := day.Add(time.Hour)
			for i, minute := range tc.minutes {
				l := fixtureLine(0, "claude", "a@example.test", "claude-opus-5-5", "")
				l.TS = day.Add(-time.Hour).Format(time.RFC3339)
				l.WeeklyAt = day.Add(time.Duration(minute) * time.Minute).Unix()
				l.WeeklyReset = reset.Unix()
				used := tc.used[i]
				l.WeeklyUsed = &used
				ls = append(ls, l)
			}
			writeLines(t, dir, ls...)
			s := summarize(t, dir)
			samples := s.Quota["0123456789abcdef"]
			if len(s.Quota) != 1 || len(samples) != 2 {
				t.Fatalf("Quota=%v, want one account with two samples", s.Quota)
			}
			for i, want := range []struct {
				minute int
				used   float64
			}{{8, tc.wantFirst}, {17, 17}} {
				sample := samples[i]
				wantAt := day.Add(time.Duration(want.minute) * time.Minute)
				if !sample.At.Equal(wantAt) || !sample.Reset.Equal(reset) || sample.Used != want.used {
					t.Errorf("sample[%d]=%+v, want at=%v reset=%v used=%v", i, sample, wantAt, reset, want.used)
				}
			}
			if s.All.Requests != 4 {
				t.Errorf("requests=%d, want 4 (quota binning must not deduplicate usage)", s.All.Requests)
			}
		})
	}
}

func TestSummarizeGroupsByModelClientAndDay(t *testing.T) {
	dir := t.TempDir()
	for _, day := range []int{0, -1} {
		for i, account := range []string{"a@example.test", "b@example.test"} {
			l := fixtureLine(day, "claude", account, "claude-opus-5-5", "shared")
			l.Client = []string{"claude-code-cli", "claude-code-sdk-ts"}[i]
			writeLines(t, dir, l)
		}
	}
	s := summarize(t, dir)
	if len(s.Models) != 1 || len(s.ModelClients) != 2 || len(s.ModelDays) != 2 {
		t.Fatalf("models=%d clients=%d days=%d; want 1, 2, 2", len(s.Models), len(s.ModelClients), len(s.ModelDays))
	}
	g := s.Models[0]
	if g.Provider != "claude" || g.Model != "claude-opus-5-5" || g.Account != "" || g.Client != "" || g.Day != "" || g.Requests != 4 || g.Processed != 400 || g.Input != 40 || g.Output != 80 || g.CacheRead != 120 || g.CacheWrite != 160 || g.Sessions != 1 {
		t.Errorf("model totals=%+v", g)
	}
	closeFloat(t, g.Cost.Total, s.All.Cost.Total)
	closeFloat(t, g.CostShare, 100)
	if g.Cost.Total <= 0 {
		t.Error("model cost must include priced requests")
	}
	for i, g := range s.ModelClients {
		wantClient := []string{"claude-code-cli", "claude-code-sdk-ts"}[i]
		if g.Client != wantClient || g.Provider != "claude" || g.Model != "claude-opus-5-5" || g.Account != "" || g.Day != "" || g.Requests != 2 || g.Processed != 200 || g.Sessions != 1 {
			t.Errorf("client %d=%+v", i, g)
		}
		closeFloat(t, g.Cost.Total, s.All.Cost.Total/2)
	}
	for i, g := range s.ModelDays {
		wantDay := []string{"2026-10-25", "2026-10-24"}[i]
		if g.Day != wantDay || g.Provider != "claude" || g.Model != "claude-opus-5-5" || g.Account != "" || g.Client != "" || g.Requests != 2 || g.Processed != 200 || g.Sessions != 1 || g.Partial != (i == 0) {
			t.Errorf("day %d=%+v", i, g)
		}
		closeFloat(t, g.Cost.Total, s.All.Cost.Total/2)
	}
	if len(s.Groups) != 2 || len(s.Daily) != 4 || len(s.Accounts) != 2 || len(s.Days) != 2 {
		t.Errorf("existing group counts: groups=%d daily=%d accounts=%d days=%d", len(s.Groups), len(s.Daily), len(s.Accounts), len(s.Days))
	}
	for _, rows := range [][]Group{s.Groups, s.Daily, s.Providers, s.Days, s.Accounts, s.Tiers, s.AllTiers} {
		var requests, processed int64
		var cost float64
		for _, g := range rows {
			if g.Client != "" {
				t.Errorf("existing group acquired a client: %+v", g)
			}
			requests += g.Requests
			processed += g.Processed
			cost += g.Cost.Total
		}
		if requests != 4 || processed != 400 {
			t.Errorf("existing group sums: requests=%d processed=%d; want 4, 400", requests, processed)
		}
		closeFloat(t, cost, s.All.Cost.Total)
	}
}
