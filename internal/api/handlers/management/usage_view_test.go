package management

import (
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagearchive"
)

func TestUsageArchiveFormatters(t *testing.T) {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.FixedZone("host", 19800))
	for _, tc := range []struct{ got, want string }{{factTime(now), "Oct 25, 2026 16:00 UTC+05:30"}, {cellTime(now), "Oct 25 16:00 UTC+05:30"}, {clockTime(now), "16:00 UTC+05:30"}, {dateTime(now), "Oct 25, 2026"}, {dayTime(now), "Sun, Oct 25, 2026"}, {relativeTime(29 * time.Minute), "in 29\u202fmin"}, {relativeTime(89 * time.Minute), "in 1\u202fh\u202f29\u202fmin"}, {relativeTime(42 * time.Hour), "in 1\u202fday\u202f18\u202fh"}, {relativeTime(75 * time.Hour), "in 3\u202fdays\u202f3\u202fh"}, {relativeTime(22*24*time.Hour + time.Hour), "in 22\u202fdays"}, {relativeTime(24 * time.Hour), "in 1\u202fday"}, {compactTokens(6050000000), "6.05B"}, {compactTokens(5000000000), "5.00B"}, {compactTokens(64300), "64.3K"}, {compactTokens(1270), "1.27K"}, {compactTokens(999), "999"}, {compactTokens(999999), "1.00M"}, {requestCount(1), "1 request"}, {requestCount(71), "71 requests"}} {
		if tc.got != tc.want {
			t.Errorf("got %q want %q", tc.got, tc.want)
		}
	}
}
func TestUsageArchiveChartGeometry(t *testing.T) {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	s := usagearchive.Summary{Start: now.AddDate(0, 0, -29), End: now, Now: now}
	for i := 0; i < 30; i++ {
		d := s.Start.AddDate(0, 0, i).Format("2006-01-02")
		a, b := 2.0, 3.0
		if i == 29 {
			a, b = 8, 5.7
		}
		s.Days = append(s.Days, usagearchive.Group{Day: d, Provider: "claude", Cost: usagearchive.Cost{Total: a}}, usagearchive.Group{Day: d, Provider: "codex", Cost: usagearchive.Cost{Total: b}})
	}
	g := usageChart(s, "cost")
	if len(g.Columns) != 30 {
		t.Fatalf("empty chart: %+v", g)
	}
	if g.Step != 5 || g.Top != 20 || len(g.Ticks) != 5 || g.Ticks[4] != 20 {
		t.Errorf("scale=%+v", g)
	}
	for _, tc := range []struct {
		i         int
		x, cy, dy string
	}{{0, "16.7", "180.0", "150.0"}, {29, "983.3", "120.0", "63.0"}} {
		c := g.Columns[tc.i]
		if c.X != tc.x || c.ClaudeY != tc.cy || c.CodexY != tc.dy {
			t.Errorf("column %d=%+v", tc.i, c)
		}
	}
	if g.Gridlines[3].Position != "75.000" || g.Gridlines[4].Label != "$20.00" {
		t.Errorf("grid ticks=%+v", g.Gridlines)
	}
	if g.PartialLeft != "96.667" || g.PartialWidth != "3.333" {
		t.Errorf("partial band: %s / %s", g.PartialLeft, g.PartialWidth)
	}
	if g.Columns[29].ClaudeBottom != "40.000" || g.Columns[29].CodexBottom != "68.500" || g.Columns[29].Total.Cost.Total != 13.7 {
		t.Errorf("derived last column=%+v", g.Columns[29])
	}
	for i, c := range g.Columns {
		want := ""
		if i%7 == 1 {
			want = c.Day.Format("Jan 02")
		}
		if c.DateTick != want {
			t.Errorf("date tick %d=%q want %q", i, c.DateTick, want)
		}
	}
	if g.Columns[14].Left || !g.Columns[15].Left {
		t.Error("tooltip sides incorrect")
	}
	s.Days = nil
	g = usageChart(s, "cost")
	if g.Gridlines[4].Label != "$4.00" || g.Columns[29].ClaudeBottom != "0.000" || g.Columns[29].CodexBottom != "0.000" {
		t.Errorf("zero derived geometry=%+v", g)
	}
	if g.Step != 1 || g.Top != 4 || len(g.Columns) != 30 {
		t.Errorf("zero geometry=%+v", g)
	}
}
func TestUsageArchiveCompactRoundingAcrossDecades(t *testing.T) {
	for _, tc := range []struct {
		n    int64
		want string
	}{{9999, "10.0K"}, {99999, "100K"}, {999499, "999K"}, {999500, "1.00M"}} {
		if got := compactTokens(tc.n); got != tc.want {
			t.Errorf("compact(%d)=%q want=%q", tc.n, got, tc.want)
		}
	}
}

func TestUsageArchiveMoneyFormatter(t *testing.T) {
	for _, tc := range []struct {
		value float64
		want  string
	}{
		{19727.87, "$19,727.87"}, {0, "$0.00"}, {999.995, "$1,000.00"}, {1234567.891, "$1,234,567.89"}, {-19727.87, "-$19,727.87"}, {-0.01, "-$0.01"},
	} {
		if got := formatMoney(tc.value); got != tc.want {
			t.Errorf("formatMoney(%v)=%q want %q", tc.value, got, tc.want)
		}
	}
}
