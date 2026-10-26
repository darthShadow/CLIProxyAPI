package management

import (
	"reflect"
	"strings"

	"golang.org/x/net/html"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagearchive"
)

func TestModelClientsWithoutPricedCost(t *testing.T) {
	s := usagearchive.Summary{
		Models: []usagearchive.Group{{Provider: "codex", Model: "unpriced", Requests: 168, Unpriced: 168}},
		ModelClients: []usagearchive.Group{
			{Provider: "codex", Model: "unpriced", Client: "codex", Requests: 167, Unpriced: 167},
			{Provider: "codex", Model: "unpriced", Requests: 1, Unpriced: 1},
		},
	}
	if got := modelDetails(s)[0].Clients; got != "Codex, Not Recorded" {
		t.Errorf("unpriced clients=%q want labels without percentages", got)
	}
}

func TestUnpricedModelReplacesCostChart(t *testing.T) {
	for _, tc := range []struct {
		name               string
		requests, unpriced int64
		wantChart          bool
	}{
		{"all unpriced", 2, 2, false},
		{"partly priced", 2, 1, true},
		{"priced zero cost", 2, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := pageView()
			g := usagearchive.Group{Provider: "codex", Model: "model", Requests: tc.requests, Unpriced: tc.unpriced, Input: 200, Processed: 200}
			v.Summary.Models = []usagearchive.Group{g}
			v.Summary.ModelDays = []usagearchive.Group{g}
			markup := pageRender(t, "model-chart", modelDetails(v.Summary)[0])
			figures := pageNodes(t, markup, func(n *html.Node) bool { return n.Data == "figure" })
			if got := len(figures) == 1; got != tc.wantChart {
				t.Errorf("daily cost chart present=%v want %v", got, tc.wantChart)
			}
			if !tc.wantChart {
				requireText(t, markup, "No Price")
			}
			cards := pageNodes(t, pageRender(t, "fragment.html", v), func(n *html.Node) bool { return n.Data == "section" && pageAttr(n, "aria-labelledby") == "models-h" })
			requireText(t, nodeHTML(t, cards[0]), "Tokens by Type", "200")
		})
	}
}

func TestModelDetailsRatios(t *testing.T) {
	for _, tc := range []struct {
		name         string
		group        usagearchive.Group
		price, cache string
	}{
		{"priced", usagearchive.Group{Input: 100, CacheRead: 300, CacheWrite: 100, Output: 500, Processed: 1000, Cost: usagearchive.Cost{Total: .002}}, "$2.00", "60.0%"},
		{"unpriced", usagearchive.Group{Input: 100, Processed: 100, Unpriced: 1, Cost: usagearchive.Cost{Total: .002}}, "Not Available", "0.0%"},
		{"zero tokens", usagearchive.Group{}, "Not Available", "Not Available"},
		{"cache-only input", usagearchive.Group{CacheRead: 400, Output: 100, Processed: 500, Cost: usagearchive.Cost{Total: .001}}, "$2.00", "100.0%"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.group.Provider, tc.group.Model, tc.group.CostShare = "claude", "model", 40
			rows := modelDetails(usagearchive.Summary{Models: []usagearchive.Group{tc.group}})
			if len(rows) != 1 {
				t.Fatalf("models=%d want 1", len(rows))
			}
			got := rows[0]
			if got.CostPerMillion != tc.price || got.CacheHit != tc.cache || got.CostShare != 40 || got.Processed != tc.group.Processed {
				t.Errorf("model=%+v; want price=%q cache=%q share=40 tokens=%d", got, tc.price, tc.cache, tc.group.Processed)
			}
		})
	}
	s := usagearchive.Summary{
		Models: []usagearchive.Group{{Provider: "claude", Model: "shared", Cost: usagearchive.Cost{Total: .002}}, {Provider: "codex", Model: "shared", Cost: usagearchive.Cost{Total: .001}}},
		ModelClients: []usagearchive.Group{
			{Provider: "claude", Model: "shared", Client: "claude-code-sdk-ts", Cost: usagearchive.Cost{Total: .00006}},
			{Provider: "claude", Model: "shared", Client: "claude-code-cli", Cost: usagearchive.Cost{Total: .00184}},
			{Provider: "claude", Model: "shared", Client: "claude-code-sdk-py", Cost: usagearchive.Cost{Total: .00004}},
			{Provider: "claude", Model: "shared", Client: "claude-code-cli-sdk", Cost: usagearchive.Cost{Total: .00006}},
			{Provider: "codex", Model: "shared", Client: "codex", Cost: usagearchive.Cost{Total: .001}},
			{Provider: "claude", Model: "other", Client: "codex", Cost: usagearchive.Cost{Total: 9}},
		},
	}
	rows := modelDetails(s)
	if len(rows) != 2 {
		t.Fatalf("models=%d want 2", len(rows))
	}
	if rows[0].Clients != "Claude Code CLI 92.0%, Agent SDK 8.0%" || rows[1].Clients != "Codex 100.0%" {
		t.Errorf("client shares=%q, %q", rows[0].Clients, rows[1].Clients)
	}
	for client, want := range map[string]string{"claude-code-cli": "Claude Code CLI", "claude-code-sdk-ts": "Agent SDK", "claude-code-sdk-py": "Agent SDK", "claude-code-cli-sdk": "Agent SDK", "codex": "Codex", "": "Not Recorded", "claude-other": "claude-other"} {
		if got := clientLabel(client); got != want {
			t.Errorf("clientLabel(%q)=%q want %q", client, got, want)
		}
	}
}

func TestModelChartReusesDailyChart(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			s := pageView().Summary
			s.Models = []usagearchive.Group{{Provider: provider, Model: "shared"}}
			s.ModelDays = []usagearchive.Group{
				{Day: "2026-10-25", Provider: provider, Model: "shared", Requests: 1, Cost: usagearchive.Cost{Total: 8}},
				{Day: "2026-10-24", Provider: provider, Model: "shared", Requests: 1, Cost: usagearchive.Cost{Total: 2}},
				{Day: "2026-10-25", Provider: provider, Model: "another", Requests: 1, Cost: usagearchive.Cost{Total: 90}},
				{Day: "2026-10-25", Provider: map[string]string{"claude": "codex", "codex": "claude"}[provider], Model: "shared", Requests: 1, Cost: usagearchive.Cost{Total: 80}},
			}
			rows := modelDetails(s)
			want := usageChart(usagearchive.Summary{Start: s.Start, End: s.End, Now: s.Now, Days: s.ModelDays[:2]}, "cost")
			if !reflect.DeepEqual(rows[0].Chart, want) {
				t.Errorf("model chart does not equal usageChart for only its provider and model: columns=%d want %d", len(rows[0].Chart.Columns), len(want.Columns))
			}
			markup := pageRender(t, "model-chart", rows[0])
			t.Logf("%s chart: %d bytes; eight charts: %d bytes", provider, len(markup), 8*len(markup))
			strokes := pageNodes(t, markup, func(n *html.Node) bool { return n.Data == "polyline" && pageAttr(n, "stroke") != "var(--card)" })
			if len(strokes) != 2 {
				t.Fatalf("model provider strokes=%d want 2 (solid and partial)", len(strokes))
			}
			solid, partial := want.ClaudeSolid, want.ClaudePartial
			if provider == "codex" {
				solid, partial = want.CodexSolid, want.CodexPartial
			}
			if pageAttr(strokes[0], "points") != solid || pageAttr(strokes[1], "points") != partial || pageAttr(strokes[1], "stroke-dasharray") != "4 4" {
				t.Errorf("model provider geometry differs from daily line: %s", nodeHTML(t, strokes[0]))
			}
			for _, stroke := range strokes {
				if got := pageAttr(stroke, "stroke"); got != "var(--"+provider+")" {
					t.Errorf("provider stroke=%q want %q", got, provider)
				}
			}
			tips := pageNodes(t, markup, func(n *html.Node) bool { return pageClass(n, "tip") })
			if len(tips) != 30 {
				t.Fatalf("model tooltips=%d want 30", len(tips))
			}
			for _, tip := range tips {
				if got := len(pageNodes(t, nodeHTML(t, tip), func(n *html.Node) bool { return pageClass(n, "tip-row") })); got != 1 {
					t.Errorf("tooltip rows=%d want 1", got)
				}
			}
			if got := pageText(tips[29], false); !strings.Contains(got, "Oct 25") || !strings.Contains(got, "$8.00") || !strings.Contains(got, "Partial") {
				t.Errorf("last tooltip=%q want day, model cost, and partial marker", got)
			}
		})
	}
}

func TestTypeBars(t *testing.T) {
	g := usagearchive.Group{Requests: 2, Input: 100, CacheRead: 200, CacheWrite: 300, Output: 400, Processed: 1000, Cost: usagearchive.Cost{Input: 1, CacheRead: 2, CacheWrite: 3, Output: 4, Total: 10}}
	for _, kind := range []string{"cost", "tok"} {
		bars := typeBars(g, kind)
		if len(bars) != 4 {
			t.Fatalf("%s bars=%d want 4", kind, len(bars))
		}
		for i, name := range []string{"Uncached Input", "Cache Read", "Cache Write", "Output"} {
			wantText := []string{"$1.00", "$2.00", "$3.00", "$4.00"}[i]
			if kind == "tok" {
				wantText = []string{"100", "200", "300", "400"}[i]
			}
			if bars[i].Name != name || bars[i].Class != []string{"bg-t1", "bg-t2", "bg-t3", "bg-t4"}[i] || bars[i].Text != wantText || bars[i].Share != float64((i+1)*10) {
				t.Errorf("%s bar %d=%+v want %s, %s, %d%%", kind, i, bars[i], name, wantText, (i+1)*10)
			}
		}
	}
	for _, empty := range []usagearchive.Group{{}, {Requests: 1, Unpriced: 1}} {
		for _, kind := range []string{"cost", "tok"} {
			for _, bar := range typeBars(empty, kind) {
				want := "No Price"
				if kind == "tok" {
					want = "0"
				}
				if bar.Share != 0 || bar.Text != want {
					t.Errorf("zero %s bar=%+v want 0%% and %q", kind, bar, want)
				}
			}
		}
	}
}

func TestUsageArchiveFragmentModelsCard(t *testing.T) {
	v := pageView()
	a := usagearchive.Group{Provider: "claude", Model: "model <script>", Requests: 2, Input: 100, CacheRead: 300, CacheWrite: 100, Output: 500, Processed: 1000, Cost: usagearchive.Cost{Input: 2, CacheRead: 1, CacheWrite: 3, Output: 4, Total: 10}, CostShare: 100.0 * 10 / 15}
	b := usagearchive.Group{Provider: "codex", Model: "gpt", Requests: 1, Processed: 500, Input: 500, Cost: usagearchive.Cost{Input: 5, Total: 5}, CostShare: 100.0 * 5 / 15}
	c := usagearchive.Group{Provider: "claude", Model: "legacy", Requests: 1, Unpriced: 1}
	v.Summary.Models = []usagearchive.Group{a, b, c}
	v.Summary.ModelClients = []usagearchive.Group{
		{Provider: a.Provider, Model: a.Model, Client: "claude-code-cli", Cost: usagearchive.Cost{Total: 9.2}},
		{Provider: a.Provider, Model: a.Model, Client: "claude-code-sdk-py", Cost: usagearchive.Cost{Total: .8}},
		{Provider: b.Provider, Model: b.Model, Client: "codex", Cost: usagearchive.Cost{Total: 5}},
		{Provider: c.Provider, Model: c.Model},
	}
	v.Summary.ModelDays = []usagearchive.Group{{Day: "2026-10-25", Provider: a.Provider, Model: a.Model, Requests: 2, Cost: a.Cost}, {Day: "2026-10-25", Provider: b.Provider, Model: b.Model, Requests: 1, Cost: b.Cost}}
	markup := pageRender(t, "fragment.html", v)
	cards := pageNodes(t, markup, func(n *html.Node) bool { return n.Data == "section" && pageAttr(n, "aria-labelledby") == "models-h" })
	if len(cards) != 1 {
		t.Fatalf("Models cards=%d want 1", len(cards))
	}
	if strings.Index(markup, `id="models-h"`) > strings.Index(markup, `id="am-h"`) {
		t.Error("Models card must precede Totals per Account and Model")
	}
	details := pageNodes(t, nodeHTML(t, cards[0]), func(n *html.Node) bool { return n.Data == "details" })
	if len(details) != 3 {
		t.Fatalf("model disclosures=%d want 3", len(details))
	}
	for i, detail := range details {
		g := v.Summary.Models[i]
		if got := pageAttr(detail, "data-p"); got != providerLabel(g.Provider) {
			t.Errorf("model %d provider=%q want %q", i, got, providerLabel(g.Provider))
		}
		summary := pageNodes(t, nodeHTML(t, detail), func(n *html.Node) bool { return n.Data == "summary" })
		if len(summary) != 1 || !strings.Contains(pageText(summary[0], false), g.Model) {
			t.Fatalf("model %d summary lacks %q", i, g.Model)
		}
		body := nodeHTML(t, detail)
		for _, want := range []string{"Cost by Type", "Tokens by Type", `class="bg-t1"`, `class="bg-t4"`} {
			if !strings.Contains(body, want) {
				t.Errorf("model %d lacks %q", i, want)
			}
		}
		if got := len(pageNodes(t, body, func(n *html.Node) bool { return pageClass(n, "hbar") })); got != 2 {
			t.Errorf("model %d type bars=%d want 2", i, got)
		}
	}
	text := pageText(cards[0], false)
	for _, want := range []string{"Models", "Claude Code CLI 92.0%, Agent SDK 8.0%", "Codex 100.0%", "Not Recorded", "66.7% of cost", "$10.00", "1,000 tokens", "$10,000.00 per 1M tokens", "Cache hit: 60.0%", "Not Available"} {
		if !strings.Contains(text, want) {
			t.Errorf("Models card lacks %q", want)
		}
	}
	if strings.Contains(markup, "<script>") || !strings.Contains(markup, "model &lt;script&gt;") {
		t.Error("model name must remain escaped")
	}
}
