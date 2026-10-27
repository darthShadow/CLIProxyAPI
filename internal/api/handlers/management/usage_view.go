package management

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagearchive"
)

func factTime(t time.Time) string {
	if t.IsZero() {
		return "Not Reported"
	}
	return t.Format("Jan 02, 2006 15:04 UTC-07:00")
}
func cellTime(t time.Time) string {
	if t.IsZero() {
		return "Not Reported"
	}
	return t.Format("Jan 02 15:04 UTC-07:00")
}
func clockTime(t time.Time) string { return t.Format("15:04 UTC-07:00") }
func dateTime(t time.Time) string  { return t.Format("Jan 02, 2006") }
func dayTime(t time.Time) string   { return t.Format("Mon, Jan 02, 2006") }
func relativeTime(d time.Duration) string {
	minutes := int(d / time.Minute)
	hours := minutes / 60
	days := hours / 24
	if days >= 7 {
		return fmt.Sprintf("in %d\u202fdays", days)
	}
	if days >= 1 {
		word := "days"
		if days == 1 {
			word = "day"
		}
		s := fmt.Sprintf("in %d\u202f%s", days, word)
		if hours%24 != 0 {
			s += fmt.Sprintf("\u202f%d\u202fh", hours%24)
		}
		return s
	}
	if hours >= 1 {
		return fmt.Sprintf("in %d\u202fh\u202f%d\u202fmin", hours, minutes%60)
	}
	return fmt.Sprintf("in %d\u202fmin", minutes)
}
func compactTokens(n int64) string {
	if n < 1000 {
		return strconv.FormatInt(n, 10)
	}
	unit := float64(1000)
	suffix := []string{"K", "M", "B"}
	i := 0
	for i < 2 && float64(n)/unit >= 999.5 {
		unit *= 1000
		i++
	}
	v := float64(n) / unit
	if v < 1 {
		v = 1
	}
	scale := math.Pow(10, 2-math.Floor(math.Log10(v)))
	v = math.Round(v*scale) / scale
	digits := 2 - int(math.Floor(math.Log10(v)))
	if digits < 0 {
		digits = 0
	}
	return fmt.Sprintf("%.*f%s", digits, v, suffix[i])
}
func requestCount(n int64) string {
	if n == 1 {
		return "1 request"
	}
	return groupedCount(n) + " requests"
}

func groupedCount(n any) string {
	integer, fraction, decimal := strings.Cut(fmt.Sprint(n), ".")
	for i := len(integer) - 3; i > 0 && integer[i-1] != '-'; i -= 3 {
		integer = integer[:i] + "," + integer[i:]
	}
	if decimal {
		return integer + "." + fraction
	}
	return integer
}
func formatMoney(n float64) string {
	sign := ""
	if n < 0 {
		sign = "-"
	}
	return sign + "$" + groupedCount(fmt.Sprintf("%.2f", math.Abs(n)))
}

func chartTickLabel(metric string, n float64) string {
	if metric == "tok" {
		return compactTokens(int64(n))
	}
	if metric == "cost" {
		return formatMoney(n)
	}
	return groupedCount(strconv.FormatFloat(n, 'f', -1, 64))
}
func tierLabel(tier string) string {
	switch tier {
	case "standard":
		return "Standard"
	case "fast":
		return "Fast"
	case "no price":
		return "No Price"
	}
	return tier
}

type typeCell struct {
	Text     string
	Unpriced int64
}
type typeRow struct {
	Name   string
	Values []typeCell
}

// typeRows transposes provider totals into the fixed type comparison.
func typeRows(s usagearchive.Summary) []typeRow {
	providers := []usagearchive.Group{{}, {}, s.All}
	for _, g := range s.Providers {
		if g.Provider == "claude" {
			providers[0] = g
		}
		if g.Provider == "codex" {
			providers[1] = g
		}
	}
	names := []string{"Uncached Input Tokens", "Cache Read Tokens", "Cache Write Tokens", "Output Tokens", "Processed Tokens", "Uncached Input Cost", "Cache Read Cost", "Cache Write Cost", "Output Cost", "Cache Savings"}
	rows := make([]typeRow, len(names))
	for i, name := range names {
		rows[i].Name = name
	}
	for _, g := range providers {
		for i, n := range []int64{g.Input, g.CacheRead, g.CacheWrite, g.Output, g.Processed} {
			rows[i].Values = append(rows[i].Values, typeCell{Text: groupedCount(n)})
		}
		for i, cost := range []float64{g.Cost.Input, g.Cost.CacheRead, g.Cost.CacheWrite, g.Cost.Output, g.Cost.Savings} {
			value := "Not Available"
			if g.Requests > 0 {
				value = "No Price"
				if g.Unpriced < g.Requests {
					value = formatMoney(cost)
				}
			}
			rows[i+5].Values = append(rows[i+5].Values, typeCell{Text: value, Unpriced: g.Unpriced})
		}
	}
	return rows
}

type chartColumn struct {
	Day                                 time.Time
	X, ClaudeY, CodexY                  string
	Left, Partial                       bool
	Claude, Codex                       usagearchive.Group
	Total                               usagearchive.Group
	DateTick, ClaudeBottom, CodexBottom string
}
type chartTick struct{ Position, Label string }
type chartGeometry struct {
	Gridlines                                                       []chartTick
	PartialLeft, PartialWidth                                       string
	Top, Step                                                       float64
	Ticks                                                           []float64
	Columns                                                         []chartColumn
	ClaudePoints, CodexPoints                                       string
	CodexArea, ClaudeSolid, CodexSolid, ClaudePartial, CodexPartial string
}

func chartTicks(max float64, metric string) (float64, []chartTick) {
	step := 1.0
	if max > 0 {
		power := math.Pow(10, math.Floor(math.Log10(max/4)))
		for _, m := range []float64{1, 2, 2.5, 5, 10} {
			step = m * power
			if 4*step >= max {
				break
			}
		}
	}
	var ticks []chartTick
	for i := 0; i <= 4; i++ {
		ticks = append(ticks, chartTick{fmt.Sprintf("%.3f", float64(i)*25), chartTickLabel(metric, float64(i)*step)})
	}
	return 4 * step, ticks
}

func usageChart(s usagearchive.Summary, metric string) chartGeometry {
	value := func(g usagearchive.Group) float64 {
		switch metric {
		case "tok":
			return float64(g.Processed)
		case "req":
			return float64(g.Requests)
		}
		return g.Cost.Total
	}
	byDay := map[string]map[string]usagearchive.Group{}
	for _, g := range s.Days {
		if byDay[g.Day] == nil {
			byDay[g.Day] = map[string]usagearchive.Group{}
		}
		byDay[g.Day][g.Provider] = g
	}
	max := 0.0
	for _, ps := range byDay {
		total := value(ps["claude"]) + value(ps["codex"])
		if total > max {
			max = total
		}
	}
	top, ticks := chartTicks(max, metric)
	g := chartGeometry{Step: top / 4, Top: top, Gridlines: ticks}
	for i := 0; i <= 4; i++ {
		g.Ticks = append(g.Ticks, float64(i)*g.Step)
	}
	var cp, dp []string
	for i := 0; i < 30; i++ {
		day := s.Start.AddDate(0, 0, i)
		ps := byDay[day.Format("2006-01-02")]
		a, b := ps["claude"], ps["codex"]
		x := fmt.Sprintf("%.1f", (float64(i)+.5)/30*1000)
		cyValue := 200 - value(a)/g.Top*200
		cy := fmt.Sprintf("%.1f", cyValue)
		dyValue := 200 - (value(a)+value(b))/g.Top*200
		dy := fmt.Sprintf("%.1f", dyValue)
		g.Columns = append(g.Columns, chartColumn{Day: day, X: x, ClaudeY: cy, CodexY: dy, Left: i >= 15, Partial: day.Format("2006-01-02") == s.Now.Format("2006-01-02"), Claude: a, Codex: b})
		column := &g.Columns[len(g.Columns)-1]
		column.ClaudeBottom = fmt.Sprintf("%.3f", (200-cyValue)/2)
		column.CodexBottom = fmt.Sprintf("%.3f", (200-dyValue)/2)
		if day.Weekday() == time.Sunday {
			column.DateTick = day.Format("Jan 02")
		}
		column.Total = usagearchive.Group{Requests: a.Requests + b.Requests, Processed: a.Processed + b.Processed, Unpriced: a.Unpriced + b.Unpriced, Cost: usagearchive.Cost{Total: a.Cost.Total + b.Cost.Total}}
		if column.Partial {
			g.PartialLeft = fmt.Sprintf("%.3f", float64(i)/30*100)
			g.PartialWidth = fmt.Sprintf("%.3f", 100.0/30)
		}
		cp = append(cp, x+","+cy)
		dp = append(dp, x+","+dy)
	}
	g.ClaudePoints = strings.Join(cp, " ")
	g.CodexPoints = strings.Join(dp, " ")
	last := 29
	if g.Columns[29].Partial {
		g.ClaudePartial = strings.Join(cp[28:], " ")
		g.CodexPartial = strings.Join(dp[28:], " ")
		last = 28
	}
	g.ClaudeSolid = strings.Join(cp[:last+1], " ")
	g.CodexSolid = strings.Join(dp[:last+1], " ")
	var reverse []string
	for i := len(dp) - 1; i >= 0; i-- {
		reverse = append(reverse, dp[i])
	}
	g.CodexArea = g.ClaudePoints + " " + strings.Join(reverse, " ")
	return g
}

func rowCount(rows []usagearchive.Group, provider string) string {
	n := 0
	for _, row := range rows {
		if provider == "" || row.Provider == provider {
			n++
		}
	}
	if n == 1 {
		return "1 row"
	}
	return fmt.Sprintf("%d rows", n)
}

type modelDetail struct {
	usagearchive.Group
	Clients, CostPerMillion, CacheHit string
	Chart                             chartGeometry
	CostBars, TokenBars               []typeBar
}

func modelDetails(s usagearchive.Summary) []modelDetail {
	rows := make([]modelDetail, 0, len(s.Models))
	for _, g := range s.Models {
		row := modelDetail{Group: g, CostPerMillion: "Not Available", CacheHit: "Not Available"}
		if g.Processed > 0 && g.Unpriced == 0 {
			row.CostPerMillion = formatMoney(g.Cost.Total * 1e6 / float64(g.Processed))
		}
		if input := g.Input + g.CacheRead + g.CacheWrite; input > 0 {
			row.CacheHit = fmt.Sprintf("%.1f%%", float64(g.CacheRead)/float64(input)*100)
		}
		byClient := map[string]float64{}
		for _, client := range s.ModelClients {
			if client.Provider == g.Provider && client.Model == g.Model {
				byClient[clientLabel(client.Client)] += client.Cost.Total
			}
		}
		clients := make([]string, 0, len(byClient))
		for label := range byClient {
			clients = append(clients, label)
		}
		sort.Slice(clients, func(i, j int) bool {
			if byClient[clients[i]] != byClient[clients[j]] {
				return byClient[clients[i]] > byClient[clients[j]]
			}
			return clients[i] < clients[j]
		})
		for i, label := range clients {
			if g.Cost.Total > 0 {
				clients[i] = fmt.Sprintf("%s %.1f%%", label, byClient[label]/g.Cost.Total*100)
			}
		}
		row.Clients = strings.Join(clients, ", ")
		daily := usagearchive.Summary{Start: s.Start, End: s.End, Now: s.Now}
		for _, day := range s.ModelDays {
			if day.Provider == g.Provider && day.Model == g.Model {
				daily.Days = append(daily.Days, day)
			}
		}
		row.Chart = usageChart(daily, "cost")
		row.CostBars = typeBars(g, "cost")
		row.TokenBars = typeBars(g, "tok")
		rows = append(rows, row)
	}
	return rows
}

func clientLabel(client string) string {
	switch client {
	case "claude-code-cli":
		return "Claude Code CLI"
	case "claude-code-shadow":
		return "Shadow Code"
	case "claude-code-t3":
		return "T3 Code"
	case "claude-code-sdk-ts", "claude-code-sdk-py", "claude-code-cli-sdk":
		return "Agent SDK"
	case "codex":
		return "Codex"
	case "":
		return "Not Recorded"
	}
	return client
}

type typeBar struct {
	Name, Class, Text string
	Share             float64
}

func typeBars(g usagearchive.Group, kind string) []typeBar {
	names := []string{"Uncached Input", "Cache Read", "Cache Write", "Output"}
	tokens := []int64{g.Input, g.CacheRead, g.CacheWrite, g.Output}
	costs := []float64{g.Cost.Input, g.Cost.CacheRead, g.Cost.CacheWrite, g.Cost.Output}
	bars := make([]typeBar, len(names))
	for i, name := range names {
		value, total := costs[i], g.Cost.Total
		text := "No Price"
		if g.Unpriced < g.Requests {
			text = formatMoney(value)
		}
		if kind == "tok" {
			value, total = float64(tokens[i]), float64(g.Processed)
			text = groupedCount(tokens[i])
		}
		share := 0.0
		if total > 0 {
			share = value / total * 100
		}
		bars[i] = typeBar{Name: name, Class: fmt.Sprintf("bg-t%d", i+1), Text: text, Share: share}
	}
	return bars
}

// ModelDetails supplies the per-model disclosures from the current summary.
func (v archiveView) ModelDetails() []modelDetail { return modelDetails(v.Summary) }

// TypeBars supplies the overview's cost type comparison from all providers.
func (v archiveView) TypeBars() []typeBar { return typeBars(v.Summary.All, "cost") }
