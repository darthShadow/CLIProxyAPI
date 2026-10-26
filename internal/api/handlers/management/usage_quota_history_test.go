package management

import (
	"fmt"
	stdhtml "html"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/net/html"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagearchive"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestQuotaBurnSumsRisesAcrossReset(t *testing.T) {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	samples := []usagearchive.QuotaSample{
		{At: now.Add(-3 * time.Hour), Reset: now.Add(-time.Hour), Used: 10},
		{At: now.Add(-2 * time.Hour), Reset: now.Add(-time.Hour + time.Minute), Used: 20},
		{At: now.Add(-time.Hour), Reset: now.Add(167 * time.Hour), Used: 0},
		{At: now, Reset: now.Add(167 * time.Hour), Used: 5},
	}
	got, ok := quotaBurn(samples, now.Add(-3*time.Hour), now)
	if !ok || got != 5 {
		t.Errorf("account burn=%v observed=%v want 5,true (pool at weight 3: 15)", got, ok)
	}
	points := quotaDrain([]quotaHistoryAccount{{Weight: 3, Samples: samples}}, now, now, 3)
	if len(points) != 1 || points[0].Value != 15 {
		t.Errorf("weighted pool burn=%v want one point at 15", points)
	}
	state, known := quotaState(samples, now.Add(-90*time.Minute))
	if !known || state.Used != 20 {
		t.Errorf("last state=%+v known=%v want used 20", state, known)
	}
	state, known = quotaState(samples[:2], now)
	if !known || state.Used != 0 {
		t.Errorf("expired state=%+v known=%v want known used 0", state, known)
	}
	if _, known = quotaState(samples, now.Add(-4*time.Hour)); known {
		t.Error("state invented before first observation")
	}
	// Falling usage within one reset does not subtract burn; the state at the
	// window start anchors the first rise, which counts only its share inside
	// the window, and samples after the window stay out.
	same := now.Add(time.Hour)
	samples = []usagearchive.QuotaSample{{At: now.Add(-4 * time.Hour), Reset: same, Used: 0}, {At: now.Add(-2 * time.Hour), Reset: same, Used: 20}, {At: now.Add(-time.Hour), Reset: same, Used: 10}, {At: now, Reset: same, Used: 13}, {At: now.Add(time.Hour), Reset: same, Used: 99}}
	if got, ok := quotaBurn(samples, now.Add(-3*time.Hour), now); !ok || math.Abs(got-13.0/3) > 1e-12 {
		t.Errorf("anchored positive rises=%v,%v want 13/3,true", got, ok)
	}
	// An anchor cleared by its reset starts the new window's use at that reset.
	samples = []usagearchive.QuotaSample{{At: now.Add(-10 * time.Hour), Reset: now.Add(-5 * time.Hour), Used: 50}, {At: now.Add(-time.Hour), Reset: now.Add(163 * time.Hour), Used: 8}}
	if got, ok := quotaBurn(samples, now.Add(-3*time.Hour), now); !ok || math.Abs(got-4.0/3) > 1e-12 {
		t.Errorf("rise after a cleared anchor=%v,%v want 4/3,true", got, ok)
	}
	// A new window that already has use counts its used percent as the rise.
	samples = []usagearchive.QuotaSample{
		{At: now.Add(-3 * time.Hour), Reset: now.Add(-time.Hour), Used: 10},
		{At: now.Add(-2 * time.Hour), Reset: now.Add(-time.Hour), Used: 20},
		{At: now.Add(-time.Hour), Reset: now.Add(167 * time.Hour), Used: 5},
		{At: now, Reset: now.Add(167 * time.Hour), Used: 8},
	}
	if got, ok := quotaBurn(samples, now.Add(-3*time.Hour), now); !ok || got != 6 {
		t.Errorf("rise across reset with used=%v,%v want 6,true", got, ok)
	}
}

func TestQuotaBurnCountsRiseIntoWindow(t *testing.T) {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	reset := now.Add(24 * time.Hour)
	samples := []usagearchive.QuotaSample{{At: now.Add(-4 * time.Hour), Reset: reset, Used: 10}, {At: now.Add(-time.Hour), Reset: reset, Used: 16}}
	if got, ok := quotaBurn(samples, now.Add(-3*time.Hour), now); !ok || math.Abs(got-4.0/3) > 1e-12 {
		t.Errorf("in-window share of the rise from the state at the window start=%v,%v want 4/3,true", got, ok)
	}
}

func TestUsageArchiveFragmentQuotaRangeAndAverageHint(t *testing.T) {
	as, s, q := historyFixture()
	from, to := quotaPage(s.Now, q.Page)
	v := archiveView{Summary: s, Quota: poolQuota(as, s.Now, nil), Query: q, QuotaCharts: quotaChart(as, s, q)}
	requireText(t, stdhtml.UnescapeString(pageRender(t, "fragment.html", v)), dateTime(from)+" to "+dateTime(to.AddDate(0, 0, -1))+"</span>")
	v.QuotaCharts = []quotaChartView{{Provider: "claude", From: from, To: to, RateLabel: "Window average", Average: true}}
	markup := stdhtml.UnescapeString(pageRender(t, "fragment.html", v))
	if strings.Contains(markup, "choose Window average") || !strings.Contains(markup, "Pool Empty: Not Available") {
		t.Errorf("unavailable window average must not suggest itself: %.300s", markup[strings.Index(markup, "Weekly Quota"):])
	}
}

func TestQuotaBurnNeedsTwoSamples(t *testing.T) {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	single := []usagearchive.QuotaSample{{At: now, Reset: now.Add(time.Hour), Used: 50}}
	if rate, ok := quotaBurn(single, now.Add(-3*time.Hour), now); ok || rate != 0 {
		t.Errorf("single sample=%v,%v want 0,false", rate, ok)
	}
	if points := quotaDrain([]quotaHistoryAccount{{Weight: 1, Samples: single}}, now, now, 3); len(points) != 0 {
		t.Errorf("unobserved drain=%v want no points", points)
	}
	// Two equal samples are observed zero burn, not an unavailable rate.
	samples := append([]usagearchive.QuotaSample{{At: now.Add(-time.Hour), Reset: now.Add(time.Hour), Used: 50}}, single...)
	if rate, ok := quotaBurn(samples, now.Add(-3*time.Hour), now); !ok || rate != 0 {
		t.Errorf("observed zero burn=%v,%v want 0,true", rate, ok)
	}
}

func TestQuotaDrainSeries(t *testing.T) {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	samples := []usagearchive.QuotaSample{{At: now.Add(-time.Hour), Reset: now.Add(time.Hour), Used: 10}, {At: now.Add(-10 * time.Minute), Reset: now.Add(time.Hour), Used: 20}, {At: now, Reset: now.Add(time.Hour), Used: 25}}
	accounts := []quotaHistoryAccount{{Weight: 3, Samples: samples}}
	points := quotaDrain(accounts, now.Add(-10*time.Minute), now, 1)
	if len(points) != 2 {
		t.Fatalf("drain points=%v want 2", points)
	}
	for i, p := range points {
		at := now.Add(time.Duration(i-1) * 10 * time.Minute)
		want, _ := quotaBurn(samples, at.Add(-time.Hour), at)
		if !p.At.Equal(at) || p.Value != 3*want {
			t.Errorf("point=%+v want at=%v value=%v", p, at, 3*want)
		}
	}
}

func TestQuotaBurnDelayedEventCountsOnePoint(t *testing.T) {
	now := time.Date(2026, 10, 25, 10, 20, 0, 0, time.UTC)
	dir := t.TempDir()
	lines := "{\"ts\":\"2026-10-25T10:00:00Z\",\"auth_index\":\"0000000000000001\",\"weekly_used\":10,\"weekly_reset\":1792926000,\"weekly_at\":1792922400}\n" +
		"{\"ts\":\"2026-10-25T10:00:00Z\",\"auth_index\":\"0000000000000001\",\"weekly_used\":11,\"weekly_reset\":1792926000,\"weekly_at\":1792923420}\n"
	if err := os.WriteFile(filepath.Join(dir, "usage-2026-10-25.jsonl"), []byte(lines), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := usagearchive.Summarize(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	rate, ok := quotaBurn(s.Quota["0000000000000001"], now.Add(-3*time.Hour), now)
	if !ok || math.Abs(rate-1.0/3) > 1e-12 {
		t.Errorf("observed-time burn=%v,%v want 1/3,true; samples=%+v", rate, ok, s.Quota)
	}
}

func TestQuotaPageBounds(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 1, 16, 0, 0, 0, loc)
	for _, k := range []int{0, -1, -4} {
		from, to := quotaPage(now, k)
		wantFrom := time.Date(2026, 11, 1, 0, 0, 0, 0, loc).AddDate(0, 0, -2+7*k)
		wantTo := wantFrom.AddDate(0, 0, 7)
		if !from.Equal(wantFrom) || !to.Equal(wantTo) || from.Location() != loc || to.Location() != loc {
			t.Errorf("page %d=(%v,%v) want (%v,%v) in %v", k, from, to, wantFrom, wantTo, loc)
		}
	}
}

func TestQuotaFutureRateChoice(t *testing.T) {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	reset := now.Add(84 * time.Hour)
	accounts := []quotaHistoryAccount{{Weight: 3, Known: true, Now: usagearchive.QuotaSample{At: now, Reset: reset, Used: 50}, Samples: []usagearchive.QuotaSample{{At: now.Add(-3 * time.Hour), Reset: reset, Used: 20}, {At: now, Reset: reset, Used: 50}}}}
	if got, ok := averageRate(accounts); !ok || math.Abs(got-150.0/84) > 1e-12 {
		t.Errorf("average rate=%v,%v want 150/84,true", got, ok)
	}
	if got, ok := quotaBurn(accounts[0].Samples, now.Add(-3*time.Hour), now); !ok || 3*got != 30 {
		t.Errorf("burn rate=%v,%v want 30 at weight 3", 3*got, ok)
	}
}

func TestQuotaFutureRateNoFallback(t *testing.T) {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	a := quotaHistoryAccount{Weight: 1, Known: true, Now: usagearchive.QuotaSample{At: now, Reset: now.Add(84 * time.Hour), Used: 50}}
	if rate, ok := quotaBurn(a.Samples, now.Add(-3*time.Hour), now); ok || rate != 0 {
		t.Errorf("missing burn=%v,%v want 0,false", rate, ok)
	}
	if rate, ok := averageRate([]quotaHistoryAccount{a}); !ok || rate != 50.0/84 {
		t.Errorf("available average=%v,%v want 50/84,true", rate, ok)
	}
	a.Now.Reset = now.Add(168 * time.Hour)
	a.Samples = []usagearchive.QuotaSample{{At: now.Add(-time.Hour), Reset: a.Now.Reset, Used: 40}, {At: now, Reset: a.Now.Reset, Used: 50}}
	if rate, ok := averageRate([]quotaHistoryAccount{a}); ok || rate != 0 {
		t.Errorf("missing average must not use burn: %v,%v", rate, ok)
	}
}

func TestQuotaPoolEmptyMovesDemand(t *testing.T) {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	reset := now.Add(100 * time.Hour)
	accounts := []quotaHistoryAccount{
		{Account: "A", Weight: 1, Known: true, Now: usagearchive.QuotaSample{At: now, Reset: reset, Used: 90}},
		{Account: "B", Weight: 1, Known: true, Now: usagearchive.QuotaSample{At: now, Reset: reset, Used: 50}},
	}
	// 20 pool points/hour: both gain 10 points in hour 1; B gains 40 in hours 2 and 3.
	got := quotaProject(accounts, 20, true, now, now.Add(7*24*time.Hour))
	if !got.Available || !got.Empty.Equal(now.Add(3*time.Hour)) {
		t.Errorf("M2 empty=%v available=%v want %v,true", got.Empty, got.Available, now.Add(3*time.Hour))
	}
	if accounts[0].Now.Used != 90 || accounts[1].Now.Used != 50 {
		t.Error("projection mutates observed state")
	}
}

func TestQuotaPoolEmptyResetRefills(t *testing.T) {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	accounts := []quotaHistoryAccount{{Account: "A", Weight: 3, Known: true, Now: usagearchive.QuotaSample{At: now, Reset: now.Add(30 * time.Minute), Used: 90}}}
	got := quotaProject(accounts, 30, true, now, now.Add(7*24*time.Hour))
	if !got.Available || !got.Empty.Equal(now.Add(10*time.Hour+30*time.Minute)) {
		t.Errorf("refilled empty=%v available=%v want %v,true", got.Empty, got.Available, now.Add(10*time.Hour+30*time.Minute))
	}
	if len(got.Resets) == 0 || !got.Resets[0].At.Equal(now.Add(30*time.Minute)) || math.Abs(got.Resets[0].Gain-285) > 1e-9 {
		t.Errorf("reset gains=%+v want first at +30m gain 285", got.Resets)
	}
	// With zero demand the same account refills again after the 168-hour window.
	accounts[0].Now.Reset = now.Add(-time.Hour)
	got = quotaProject(accounts, 0, true, now, now.Add(7*24*time.Hour))
	if !got.Available || !got.Empty.IsZero() || len(got.Resets) != 1 || !got.Resets[0].At.Equal(now.Add(167*time.Hour)) {
		t.Errorf("expired reset recurrence=%+v want next at +167h and no empty", got)
	}
	accounts[0].Now.Reset = now.Add(5 * time.Minute)
	accounts[0].Now.Used = 0
	got = quotaProject(accounts, 30, true, now, now.Add(time.Hour))
	hasGridPoint := false
	for _, point := range got.Points {
		if point.At.Equal(now.Add(10 * time.Minute)) {
			hasGridPoint = true
		}
	}
	if !hasGridPoint {
		t.Error("reset at +5m shifts the next 10-minute bin away from +10m")
	}
}

func TestQuotaPoolEmptyNotByHorizon(t *testing.T) {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	a := quotaHistoryAccount{Account: "A", Weight: 1, Known: true, Now: usagearchive.QuotaSample{At: now, Reset: now.Add(84 * time.Hour), Used: 0}}
	got := quotaProject([]quotaHistoryAccount{a}, 0, true, now, now.Add(7*24*time.Hour))
	if !got.Available || !got.Empty.IsZero() || !got.Horizon.Equal(now.Add(7*24*time.Hour)) {
		t.Errorf("horizon=%v empty=%v available=%v want %v,zero,true", got.Horizon, got.Empty, got.Available, now.Add(7*24*time.Hour))
	}
	got = quotaProject([]quotaHistoryAccount{a}, 0, false, now, now.Add(7*24*time.Hour))
	if got.Available || len(got.Points) != 0 {
		t.Errorf("unavailable chosen rate projected: %+v", got)
	}
}

func TestQuotaPoolEmptyNeedsEveryCountedAccount(t *testing.T) {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	a := quotaHistoryAccount{Account: "A", Weight: 1, Known: true, Now: usagearchive.QuotaSample{At: now, Reset: now.Add(84 * time.Hour), Used: 50}}
	b := quotaHistoryAccount{Account: "B", Weight: 1}
	for _, available := range []bool{false, true} {
		got := quotaProject([]quotaHistoryAccount{a, b}, 10, available, now, now.Add(7*24*time.Hour))
		if got.Available || len(got.Missing) != 1 || got.Missing[0] != "B" || len(got.Points) != 0 || len(got.Resets) != 0 || !got.Empty.IsZero() {
			t.Errorf("missing state must win over rate (available=%v): %+v", available, got)
		}
	}
	b.Known = true
	got := quotaProject([]quotaHistoryAccount{a, b}, 10, true, now, now.Add(7*24*time.Hour))
	if got.Available || len(got.Missing) != 1 {
		t.Errorf("state without reset was usable: %+v", got)
	}
	t.Run("account exclusion", func(t *testing.T) {
		as, now := snapshotAuths()
		as[1].Quota = coreauth.QuotaState{}
		reset := now.Add(84 * time.Hour)
		s := usagearchive.Summary{Now: now, Quota: map[string][]usagearchive.QuotaSample{as[0].Index: {{At: now.Add(-3 * time.Hour), Reset: reset, Used: 20}, {At: now, Reset: reset, Used: 50}}}}
		q := quotaQuery{Burn: 3, Future: "burn"}
		for _, exclude := range []bool{false, true} {
			if exclude {
				q.Exclude = map[string]bool{as[1].Index: true}
			}
			c := quotaChart(as, s, q)[0]
			if c.Burn != 10 || !c.BurnAvailable {
				t.Errorf("A burn lost when missing B excluded=%v: %v,%v", exclude, c.Burn, c.BurnAvailable)
			}
			if !exclude {
				if c.Projection.Available || len(c.Projection.Missing) != 1 || c.Projection.Missing[0] != usageAccount(as[1]) || c.Pool.Projected != "" || c.EmptyX != "" {
					t.Errorf("B missing: available=%v missing=%v projected=%q empty=%q", c.Projection.Available, c.Projection.Missing, c.Pool.Projected, c.EmptyX)
				}
			} else if !c.Projection.Available || !c.Projection.Empty.Equal(now.Add(5*time.Hour)) {
				t.Errorf("excluded B: M2 empty=%v available=%v want %v,true", c.Projection.Empty, c.Projection.Available, now.Add(5*time.Hour))
			}
		}

	})
}

func historyFixture() ([]*coreauth.Auth, usagearchive.Summary, quotaQuery) {
	as, now := snapshotAuths()
	now = now.In(time.FixedZone("host", 19800))
	as[0].Attributes[coreauth.AttributeWeight] = "6"
	as[1].Attributes[coreauth.AttributeWeight] = "3"
	as[0].Quota.Signals["Anthropic-Ratelimit-Unified-7d-Reset"] = fmt.Sprint(now.Add(30 * time.Minute).Unix())
	q := quotaQuery{Provider: "all", View: "pool", Burn: 3, Future: "burn"}
	from, _ := quotaPage(now, 0)
	s := usagearchive.Summary{Now: now, Start: from.AddDate(0, 0, -27), Quota: map[string][]usagearchive.QuotaSample{}}
	for i, a := range as {
		used, reset, _, _ := coreauth.WeeklyQuota(a.Quota)
		s.Quota[a.Index] = []usagearchive.QuotaSample{
			{At: from.Add(-time.Hour), Reset: reset, Used: used - 10},
			{At: now.Add(-3 * time.Hour), Reset: reset, Used: used - 10},
			{At: now, Reset: reset, Used: used},
		}
		if i == 1 {
			s.Quota[a.Index][2].Used = 20
		}
	}
	return as, s, q
}

func TestQuotaChartRecordedThenProjected(t *testing.T) {
	as, s, q := historyFixture()
	charts := quotaChart(as, s, q)
	if len(charts) != 1 {
		t.Fatalf("charts=%d want 1", len(charts))
	}
	c := charts[0]
	if c.Top != 900 || len(c.Recorded) == 0 || len(c.Projected) == 0 {
		t.Fatalf("chart top=%v recorded=%d projected=%d want 900 and both paths", c.Top, len(c.Recorded), len(c.Projected))
	}
	last, first := c.Recorded[len(c.Recorded)-1], c.Projected[0]
	if !last.At.Equal(s.Now) || !first.At.Equal(s.Now) || last.Value != 540 || first.Value != last.Value {
		t.Errorf("join recorded=%+v projected=%+v want Now at 540", last, first)
	}
	if c.Pool.Gridlines[2].Label != "900%" || c.Pool.Gridlines[1].Label != "450%" || c.Pool.Gridlines[0].Label != "0%" {
		t.Errorf("pool ticks=%+v", c.Pool.Gridlines)
	}
	if len(c.Resets) == 0 || !strings.HasPrefix(c.Resets[0].Text, "+") || !strings.HasSuffix(c.Resets[0].Text, "%") {
		t.Fatalf("reset labels=%+v", c.Resets)
	}
	reset := c.Resets[0]
	jump := false
	for i, p := range c.Projected {
		if i > 0 && p.At.Equal(reset.At) && p.At.Equal(c.Projected[i-1].At) && math.Abs(p.Value-c.Projected[i-1].Value-reset.Gain) < 1e-8 {
			jump = true
		}
	}
	if !jump {
		t.Error("reset lacks a vertical jump with the labelled gain")
	}
	if c.NowX == "" || c.EmptyX == "" || c.Drain.Projected == "" {
		t.Errorf("missing Now, Empty, or drain projection: now=%q empty=%q drain=%q", c.NowX, c.EmptyX, c.Drain.Projected)
	}
	// An archived observation newer than the snapshot supplies the Now state.
	as[0].Quota.ObservedAt = s.Now.Add(-time.Hour)
	samples := s.Quota[as[0].Index]
	samples[len(samples)-1].Used = 60
	c = quotaChart(as, s, q)[0]
	if got := c.Recorded[len(c.Recorded)-1].Value; got != 480 {
		t.Errorf("newer archive Now pool=%v want 480", got)
	}
	// A live value alone must never make burn observed.
	s.Quota = nil
	c = quotaChart(as, s, q)[0]
	if c.BurnAvailable || c.Projection.Available || c.Pool.Projected != "" {
		t.Errorf("live snapshot invented burn or projection: burn=%v projection=%v", c.Burn, c.Projection.Available)
	}
}

func TestQuotaChartCarryInAcrossPageStart(t *testing.T) {
	as, s, q := historyFixture()
	q.Page = -1
	from, _ := quotaPage(s.Now, q.Page)
	for _, a := range as {
		_, reset, _, _ := coreauth.WeeklyQuota(a.Quota)
		s.Quota[a.Index] = []usagearchive.QuotaSample{{At: from.Add(-time.Hour), Reset: reset, Used: 20}, {At: from.Add(time.Hour), Reset: reset, Used: 50}}
	}
	charts := quotaChart(as, s, q)
	if len(charts) != 1 || len(charts[0].Recorded) == 0 {
		t.Fatalf("no carry-in chart: %+v", charts)
	}
	c := charts[0]
	if !c.Recorded[0].At.Equal(from) || c.Recorded[0].Value != 720 {
		t.Errorf("carry-in first=%+v want %v,720", c.Recorded[0], from)
	}
	if c.Pool.Projected != "" || c.NowX != "" || c.EmptyX != "" || len(c.Resets) != 0 {
		t.Error("past page draws future markers or projection")
	}
	for index, samples := range s.Quota {
		s.Quota[index] = samples[:1]
	}
	c = quotaChart(as, s, q)[0]
	if !c.NoRecorded || len(c.Recorded) == 0 {
		t.Errorf("carry-in without in-page observations: NoRecorded=%v points=%d want true and carried geometry", c.NoRecorded, len(c.Recorded))
	}
}

func TestQuotaChartStartsWhenAllAccountsKnown(t *testing.T) {
	as, s, q := historyFixture()
	from, _ := quotaPage(s.Now, 0)
	_, reset, _, _ := coreauth.WeeklyQuota(as[1].Quota)
	s.Quota[as[1].Index] = []usagearchive.QuotaSample{{At: from.Add(30 * time.Minute), Reset: reset, Used: 20}}
	charts := quotaChart(as, s, q)
	if len(charts) != 1 || len(charts[0].Recorded) == 0 {
		t.Fatalf("no chart: %+v", charts)
	}
	if got := charts[0].Recorded[0].At; !got.Equal(from.Add(30 * time.Minute)) {
		t.Errorf("line starts=%v want %v", got, from.Add(30*time.Minute))
	}
}

func TestQuotaChartTooltipColumns(t *testing.T) {
	as, s, q := historyFixture()
	charts := quotaChart(as, s, q)
	if len(charts) != 1 {
		t.Fatalf("charts=%d want 1", len(charts))
	}
	c := charts[0]
	if len(c.Pool.Columns) != 42 || len(c.Drain.Columns) != 42 {
		t.Fatalf("tooltip columns pool=%d drain=%d want 42 each", len(c.Pool.Columns), len(c.Drain.Columns))
	}
	want := c.From.Add(2*time.Hour).Format("Mon 15:04 UTC-07:00") + " - 630% left of 900% - recorded"
	if c.Pool.Columns[0].Text != want {
		t.Errorf("first tooltip=%q want %q", c.Pool.Columns[0].Text, want)
	}
	if !strings.Contains(c.Pool.Columns[41].Text, " - projected") {
		t.Errorf("future tooltip=%q", c.Pool.Columns[41].Text)
	}
	if c.Pool.Columns[0].Left || !c.Pool.Columns[41].Left {
		t.Error("tooltip sides must follow the page half")
	}
}

func TestQuotaHistoryLocalDateTicks(t *testing.T) {
	for _, tc := range []struct {
		name   string
		loc    *time.Location
		now    time.Time
		page   int
		labels []string
	}{
		{"today", time.FixedZone("page", 19800), time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC), 0, []string{"Oct 23", "Oct 25", "Oct 27", "Oct 29"}},
		{"previous", time.FixedZone("page", 19800), time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC), -1, []string{"Oct 16", "Oct 18", "Oct 20", "Oct 22"}},
		{"oldest", time.FixedZone("page", 19800), time.Date(2026, 10, 25, 1, 0, 0, 0, time.UTC), -4, []string{"Sep 25", "Sep 27", "Sep 29", "Oct 01"}},
		{"daylight saving", nil, time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), 0, []string{"Oct 31", "Nov 02", "Nov 04", "Nov 06"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.loc == nil {
				var err error
				tc.loc, err = time.LoadLocation("America/New_York")
				if err != nil {
					t.Fatal(err)
				}
			}
			as, s, q := historyFixture()
			s.Now, q.Page = tc.now.In(tc.loc), tc.page
			c := quotaChart(as, s, q)[0]
			for _, figure := range []quotaFigure{c.Pool, c.Drain} {
				markup := pageRender(t, "quota-figure", figure)
				ticks := pageNodes(t, markup, func(n *html.Node) bool { return pageClass(n, "xtick") })
				if len(ticks) != 4 {
					t.Fatalf("%s date ticks=%d want 4", figure.Kind, len(ticks))
				}
				for i, tick := range ticks {
					at := c.From.AddDate(0, 0, 2*i)
					wantPosition := fmt.Sprintf("left:%.3f%%", float64(at.Sub(c.From))/float64(c.To.Sub(c.From))*100)
					if got := pageText(tick, false); got != tc.labels[i] || pageAttr(tick, "style") != wantPosition {
						t.Errorf("%s tick %d=%q at %q want %q at %q", figure.Kind, i, got, pageAttr(tick, "style"), tc.labels[i], wantPosition)
					}
				}
			}
		})
	}
}

func TestUsageArchiveFragmentQuotaChartMarkup(t *testing.T) {
	as, s, q := historyFixture()
	v := archiveView{Summary: s, Quota: poolQuota(as, s.Now, nil), Query: q, QuotaCharts: quotaChart(as, s, q)}
	markup := pageRender(t, "fragment.html", v)
	charts := pageNodes(t, markup, func(n *html.Node) bool { return pageClass(n, "quota-history") })
	if len(charts) != 1 {
		t.Fatalf("history cards=%d want 1", len(charts))
	}
	card := nodeHTML(t, charts[0])
	requireText(t, stdhtml.UnescapeString(card), "Resets on this page", "Now", "Empty", "Burn, last 3h: 30.0% per hour", "Pool Empty:")
	if pageAttr(charts[0], "data-p") != "Claude" {
		t.Error("history card needs provider filter")
	}
	for _, svg := range pageNodes(t, card, func(n *html.Node) bool { return n.Data == "svg" }) {
		halos := map[string]bool{}
		projected := false
		for n := svg.FirstChild; n != nil; n = n.NextSibling {
			if n.Data != "polyline" {
				continue
			}
			if pageAttr(n, "stroke") == "var(--card)" && pageAttr(n, "stroke-width") == "6" {
				halos[pageAttr(n, "points")] = true
			} else if !halos[pageAttr(n, "points")] {
				t.Error("history line has no preceding 6px halo")
			}
			if pageAttr(n, "stroke-dasharray") != "" {
				projected = true
			}
		}
		if !projected {
			t.Error("history figure has no dashed projection")
		}
	}
	if nodes := pageNodes(t, card, func(n *html.Node) bool { return n.Data == "text" }); len(nodes) > 0 {
		t.Error("SVG text stretches under preserveAspectRatio=none")
	}
	// A's burn survives while B's missing state blocks every projected artifact.
	as[1].Quota = coreauth.QuotaState{}
	delete(s.Quota, as[1].Index)
	v.Summary = s
	v.Quota = poolQuota(as, s.Now, nil)
	v.QuotaCharts = quotaChart(as, s, q)
	markup = pageRender(t, "fragment.html", v)
	requireText(t, stdhtml.UnescapeString(markup), "Pool Empty: Not Available; no weekly observation for", usageAccount(as[1]), "Uncheck them under Accounts to project the others.", "Burn, last 3h: 20.0% per hour", "not observed")
	c := v.QuotaCharts[0]
	if c.Pool.Projected != "" || c.Drain.Projected != "" || c.EmptyX != "" || len(c.Resets) != 0 {
		t.Error("missing weekly account still draws a projection or Empty marker")
	}
	s.Quota = nil
	as[0].Quota = coreauth.QuotaState{}
	v.Summary = s
	v.QuotaCharts = quotaChart(as, s, q)
	requireText(t, pageRender(t, "fragment.html", v), "No recorded quota", "Not Available")
}

func TestQuotaHistoryExcludesUnselectedAccounts(t *testing.T) {
	as, s, q := historyFixture()
	q.Exclude = map[string]bool{as[0].Index: true, "ffffffffffffffff": true}
	c := quotaChart(as, s, q)[0]
	if c.Top != 300 || c.Burn != 10 || len(c.LeftOut) != 1 || c.LeftOut[0] != usageAccount(as[0]) || c.Recorded[len(c.Recorded)-1].Value != 240 {
		t.Errorf("excluded history top=%v burn=%v leftOut=%v last=%+v want 300,10,A,240", c.Top, c.Burn, c.LeftOut, c.Recorded[len(c.Recorded)-1])
	}
	as[1].Attributes[coreauth.AttributeWeight] = "0"
	c = quotaChart(as, s, q)[0]
	if c.Top != 0 || c.BurnAvailable || c.Projection.Available || len(c.Pool.Recorded) != 0 {
		t.Errorf("all excluded/zero-weight history top=%v burn=%v projected=%v recorded lines=%d", c.Top, c.BurnAvailable, c.Projection.Available, len(c.Pool.Recorded))
	}
}

func TestUsageArchiveHistoryControls(t *testing.T) {
	as, s, q := historyFixture()
	for _, page := range []int{-4, -1, 0} {
		q.Page = page
		q.Burn = 6
		q.Future = "average"
		markup := pageRender(t, "fragment.html", archiveView{Summary: s, Quota: poolQuota(as, s.Now, nil), Query: q, QuotaCharts: quotaChart(as, s, q)})
		forms := pageNodes(t, markup, func(n *html.Node) bool { return n.Data == "form" && pageAttr(n, "id") == "quota-controls" })
		if len(forms) != 1 {
			t.Fatalf("quota forms=%d want 1", len(forms))
		}
		form := nodeHTML(t, forms[0])
		for _, tc := range []struct {
			kind     string
			disabled bool
		}{{"prev", page == -4}, {"next", page == 0}, {"today", page == 0}} {
			buttons := pageNodes(t, form, func(n *html.Node) bool { return n.Data == "button" && pageAttr(n, "data-page") == tc.kind })
			if len(buttons) != 1 {
				t.Errorf("page %d %s buttons=%d want 1", page, tc.kind, len(buttons))
				continue
			}
			if pageAttr(buttons[0], "type") != "button" || hasPageAttr(buttons[0], "disabled") != tc.disabled {
				t.Errorf("page %d %s=%s want type button, disabled=%v", page, tc.kind, nodeHTML(t, buttons[0]), tc.disabled)
			}
		}
		hidden := pageNodes(t, form, func(n *html.Node) bool { return n.Data == "input" && pageAttr(n, "name") == "qpage" })
		if len(hidden) != 1 || pageAttr(hidden[0], "type") != "hidden" || pageAttr(hidden[0], "value") != fmt.Sprint(page) {
			t.Errorf("hidden page=%v want %d", hidden, page)
		}
		for _, tc := range []struct {
			name, selected string
			values         []string
		}{{"qburn", "6", []string{"1", "3", "6"}}, {"qfuture", "average", []string{"burn", "average"}}} {
			selects := pageNodes(t, form, func(n *html.Node) bool { return n.Data == "select" && pageAttr(n, "name") == tc.name })
			if len(selects) != 1 {
				t.Errorf("select %s count=%d want 1", tc.name, len(selects))
				continue
			}
			if !hasPageAttr(selects[0], "data-recompute") || selects[0].Parent.Data != "label" {
				t.Errorf("select %s needs recompute and label", tc.name)
			}
			options := pageNodes(t, nodeHTML(t, selects[0]), func(n *html.Node) bool { return n.Data == "option" })
			if len(options) != len(tc.values) {
				t.Errorf("%s options=%d want %d", tc.name, len(options), len(tc.values))
				continue
			}
			selected := 0
			for i, o := range options {
				if pageAttr(o, "value") != tc.values[i] {
					t.Errorf("%s value=%s want %s", tc.name, pageAttr(o, "value"), tc.values[i])
				}
				if hasPageAttr(o, "selected") {
					selected++
					if pageAttr(o, "value") != tc.selected {
						t.Errorf("%s selected=%s want %s", tc.name, pageAttr(o, "value"), tc.selected)
					}
				}
			}
			if selected != 1 {
				t.Errorf("%s selected options=%d want 1", tc.name, selected)
			}
		}
	}
}

func TestQuotaDrainTickLabelsFractional(t *testing.T) {
	as, s, q := historyFixture()
	for _, a := range as {
		used, reset, _, _ := coreauth.WeeklyQuota(a.Quota)
		// Weights 6 and 3 with a 0.06 rise in the 3h window give a 0.18%/h drain, so the tick step is 0.05.
		s.Quota[a.Index] = []usagearchive.QuotaSample{{At: s.Now.Add(-3 * time.Hour), Reset: reset, Used: used}, {At: s.Now, Reset: reset, Used: used + 0.06}}
	}
	c := quotaChart(as, s, q)[0]
	var got []string
	for _, g := range c.Drain.Gridlines {
		got = append(got, g.Label)
	}
	want := []string{"0% per hour", "0.05% per hour", "0.1% per hour", "0.15% per hour", "0.2% per hour"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("drain tick labels=%q want %q", got, want)
	}
}

func TestQuotaDrainGapIsUnobserved(t *testing.T) {
	as, s, q := historyFixture()
	for _, a := range as {
		_, reset, _, _ := coreauth.WeeklyQuota(a.Quota)
		// Two bursts; between them no 3h window holds two samples.
		s.Quota[a.Index] = []usagearchive.QuotaSample{{At: s.Now.Add(-10 * time.Hour), Reset: reset, Used: 10}, {At: s.Now.Add(-9 * time.Hour), Reset: reset, Used: 20}, {At: s.Now.Add(-2 * time.Hour), Reset: reset, Used: 30}, {At: s.Now.Add(-time.Hour), Reset: reset, Used: 40}}
	}
	c := quotaChart(as, s, q)[0]
	if len(c.Drain.Recorded) != 2 {
		t.Errorf("drain recorded segments=%d want 2: %q", len(c.Drain.Recorded), c.Drain.Recorded)
	}
	if len(c.Pool.Recorded) != 1 {
		t.Errorf("pool recorded segments=%d want 1", len(c.Pool.Recorded))
	}
	from, to := quotaPage(s.Now, 0)
	checked := 0
	for i, col := range c.Drain.Columns {
		at := from.Add(time.Duration(float64(to.Sub(from)) * (float64(i) + .5) / 42))
		if at.After(s.Now.Add(-6*time.Hour)) && at.Before(s.Now.Add(-3*time.Hour)) {
			checked++
			if !strings.HasSuffix(col.Text, "No recorded quota") {
				t.Errorf("gap column %d=%q want No recorded quota", i, col.Text)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no tooltip column falls inside the gap")
	}
}
