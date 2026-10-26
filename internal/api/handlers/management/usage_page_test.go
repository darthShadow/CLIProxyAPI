package management

import (
	"bytes"
	"encoding/json"
	stdhtml "html"
	"math"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/net/html"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagearchive"
)

func pageRender(t *testing.T, name string, data any) string {
	t.Helper()
	if v, ok := data.(archiveView); ok && name == "usage-archive.html" {
		data = map[string]any{"PriceChecked": v.Summary.Now}
	}
	var b bytes.Buffer
	if err := usageTemplates.ExecuteTemplate(&b, name, data); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
func pageNodes(t *testing.T, markup string, match func(*html.Node) bool) []*html.Node {
	t.Helper()
	n, err := html.Parse(strings.NewReader(markup))
	if err != nil {
		t.Fatal(err)
	}
	var result []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if match(n) {
			result = append(result, n)
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return result
}
func pageAttr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}
func pageClass(n *html.Node, class string) bool {
	return strings.Contains(" "+pageAttr(n, "class")+" ", " "+class+" ")
}
func pageText(n *html.Node, accessible bool) string {
	if accessible && pageAttr(n, "aria-hidden") == "true" {
		return ""
	}
	if n.Type == html.TextNode {
		return n.Data
	}
	var b strings.Builder
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		b.WriteString(pageText(c, accessible))
	}
	return b.String()
}
func pageUsageText(t *testing.T, markup string) string {
	t.Helper()
	var b strings.Builder
	for _, n := range pageNodes(t, markup, func(n *html.Node) bool { return n.Type == html.TextNode }) {
		if n.Parent == nil || !pageClass(n.Parent, "ytick") {
			b.WriteString(n.Data)
		}
	}
	return b.String()
}

func pageView() archiveView {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.FixedZone("host", 19800))
	a := usagearchive.Group{Provider: "claude", Account: "a@example.test", Model: "claude-opus-5-5", Requests: 12345, Processed: 3659839758, Input: 1234567, Cost: usagearchive.Cost{Input: 2, CacheRead: 1, CacheWrite: 3, Output: 4, Total: 10, Savings: 9, Premium: 2}, CostShare: 100, TokenShare: 100}
	s := usagearchive.Summary{Start: now.AddDate(0, 0, -29), End: now, Now: now, All: a, Providers: []usagearchive.Group{a}, Accounts: []usagearchive.Group{a}, Groups: []usagearchive.Group{a}, Daily: []usagearchive.Group{a}, AllTiers: []usagearchive.Group{a}, Tiers: []usagearchive.Group{a}}
	s.All.Provider = ""
	s.AllTiers[0].Tier = "standard"
	s.Tiers[0].Tier = "standard"
	s.Days = []usagearchive.Group{{Day: "2026-10-25", Provider: "claude", Requests: 1234, Processed: 2000000, Cost: usagearchive.Cost{Total: 8}}, {Day: "2026-10-25", Provider: "codex", Requests: 2345, Processed: 3000000, Cost: usagearchive.Cost{Total: 5.7}}}
	as, _ := quotaFixtures()
	v := archiveView{Summary: s, Quota: poolQuota(as, now, nil)}
	for _, m := range []struct{ metric, name string }{{"cost", "Cost"}, {"tok", "Processed Tokens"}, {"req", "Requests"}} {
		v.Charts = append(v.Charts, metricChart{m.metric, m.name, usageChart(s, m.metric)})
	}
	return v
}
func TestUsagePageChartHalos(t *testing.T) {
	s := pageRender(t, "fragment.html", pageView())
	svgs := pageNodes(t, s, func(n *html.Node) bool { return n.Data == "svg" && pageAttr(n, "viewBox") == "0 0 1000 200" })
	if len(svgs) != 3 {
		t.Fatalf("charts=%d want 3", len(svgs))
	}
	for i, svg := range svgs {
		halos := map[string]bool{}
		providers := 0
		drawn := false
		for n := svg.FirstChild; n != nil; n = n.NextSibling {
			if n.Data != "polyline" {
				continue
			}
			if pageAttr(n, "stroke") == "var(--card)" {
				if drawn {
					t.Errorf("metric %d: halo drawn after provider line", i)
				}
				if pageAttr(n, "stroke-width") != "6" {
					t.Error("halo width is not 6")
				}
				halos[pageAttr(n, "points")] = true
			} else {
				drawn = true
				providers++
				if !halos[pageAttr(n, "points")] {
					t.Errorf("metric %d: missing 6px halo for provider geometry", i)
				}
			}
		}
		if providers != 4 {
			t.Errorf("metric %d: strokes=%d want four including partial", i, providers)
		}
	}
}
func TestUsagePageAccessiblePill(t *testing.T) {
	for _, n := range []int64{49, 1} {
		s := pageRender(t, "pill", n)
		pills := pageNodes(t, s, func(n *html.Node) bool { return pageClass(n, "np") })
		if len(pills) != 1 {
			t.Fatalf("pill count=%d", len(pills))
		}
		want := requestCount(n) + " with no price"
		if got := strings.TrimSpace(pageText(pills[0], true)); got != want {
			t.Errorf("accessible text=%q want %q", got, want)
		}
		visible := pageNodes(t, s, func(n *html.Node) bool { return pageAttr(n, "aria-hidden") == "true" })
		if len(visible) != 1 || strings.TrimSpace(pageText(visible[0], false)) != strings.TrimSuffix(requestCount(n), " requests")+" unpriced" && n != 1 {
			t.Errorf("complete visible label must be one aria-hidden span: %s", s)
		}
		if n == 1 && (len(visible) != 1 || pageText(visible[0], false) != "1 unpriced") {
			t.Errorf("singular visible label: %s", s)
		}
	}
}
func TestUsagePageBreakdownDisclosures(t *testing.T) {
	s := pageRender(t, "fragment.html", pageView())
	cards := pageNodes(t, s, func(n *html.Node) bool { return n.Data == "section" && pageAttr(n, "aria-labelledby") == "bt-h" })
	if len(cards) != 1 {
		t.Fatalf("Breakdown Tables cards=%d want 1", len(cards))
	}
	ds := pageNodes(t, nodeHTML(t, cards[0]), func(n *html.Node) bool { return n.Data == "details" })
	if len(ds) != 3 {
		t.Fatalf("native breakdown disclosures=%d want 3", len(ds))
	}
	for i, label := range []string{"Totals by Type", "Cost by Tier", "By Day and Provider"} {
		if !strings.Contains(pageText(ds[i], false), label) {
			t.Errorf("disclosure %d lacks %s", i, label)
		}
	}
	headers := []string{}
	for _, n := range pageNodes(t, pageRender(t, "fragment.html", pageView()), func(n *html.Node) bool { return n.Data == "table" }) {
		if strings.Contains(pageText(n, false), "Tokens and cost per type and provider") {
			for r := n.FirstChild; r != nil; r = r.NextSibling {
				if r.Data == "thead" {
					for _, h := range pageNodes(t, nodeHTML(t, n), func(n *html.Node) bool {
						return n.Data == "th" && n.Parent != nil && n.Parent.Parent != nil && n.Parent.Parent.Data == "thead"
					}) {
						headers = append(headers, strings.TrimSpace(pageText(h, false)))
					}
				}
			}
		}
	}
	if strings.Join(headers, "|") != "Type|Claude|Codex|All Providers" {
		t.Errorf("type comparison columns=%v", headers)
	}
}
func nodeHTML(t *testing.T, n *html.Node) string {
	t.Helper()
	var b bytes.Buffer
	if err := html.Render(&b, n); err != nil {
		t.Fatal(err)
	}
	return b.String()
}
func TestUsagePageSummariesAndShares(t *testing.T) {
	v := pageView()
	v.Summary.AllTiers[0].Cost.Total = 8
	v.Summary.AllTiers[0].CostShare = 80
	v.Summary.AllTiers = append(v.Summary.AllTiers, usagearchive.Group{Tier: "fast", Requests: 2, Cost: usagearchive.Cost{Total: 2}, CostShare: 20}, usagearchive.Group{Tier: "no price", Requests: 49, Unpriced: 49})
	s := pageRender(t, "fragment.html", v)
	requireText(t, s, `>Fast</span>`, `>No Price</span>`, `>49 requests</span>`, `>80.0%</span>`)

	requireText(t, s, `class="grid gap-4 lg:grid-cols-2"`, `id="type-h"`, `id="speed-h"`, `class="hbar h-2.5"`, `class="swatch bg-t1"`, `class="p">20.0%`, `>Cache Savings</span>`, `>$9.00</span>`, `>Standard</span>`, `>Fast Premium</span>`, `>$2.00</span>`, `class="bg-track flex-1"`, `class="track w-16 h-1.5"`, "Tokens: processed tokens", "The tier is the one the provider reported and billed.")
}
func TestUsagePageChartDerivedMarkup(t *testing.T) {
	s := pageRender(t, "fragment.html", pageView())
	for class, want := range map[string]int{"gridline": 15, "xtick": 15, "partial-band": 3, "cdot": 180, "tip-total": 90} {
		got := len(pageNodes(t, s, func(n *html.Node) bool { return pageClass(n, class) }))
		if got != want {
			t.Errorf("%s count=%d want %d", class, got, want)
		}
	}
	requireText(t, s, `class="partial-band" style="left:96.667%;width:3.333%"`, `class="cdot bg-claude" style="bottom:40.000%"`, `class="cdot bg-codex" style="bottom:68.500%"`, `class="gridline" style="bottom:75.000%"`, `class="xtick">Sep 27`, `class="xtick">Oct 25`, `>$20.00</span>`, `>6.00M</span>`, `>4,000</span>`, `legend-line bg-muted opacity-60`)
	for _, n := range pageNodes(t, s, func(n *html.Node) bool { return n.Data == "figure" }) {
		if !strings.Contains(pageAttr(n, "aria-label"), "Sep 26, 2026 to Oct 25, 2026, days in UTC+05:30. Oct 25, 2026 is a partial day.") {
			t.Errorf("figure description=%q", pageAttr(n, "aria-label"))
		}
	}
	totals := pageNodes(t, s, func(n *html.Node) bool { return pageClass(n, "tip-total") })
	var money, requests, tokens bool
	for _, n := range totals {
		text := pageText(n, false)
		money = money || strings.Contains(text, "$13.70")
		requests = requests || strings.Contains(text, "3,579")
		tokens = tokens || strings.Contains(text, "5.00M")
	}
	if !money || !requests || !tokens {
		t.Errorf("combined tooltip values: cost=%v requests=%v tokens=%v", money, requests, tokens)
	}
}
func TestUsagePageSecondaryAction(t *testing.T) {
	for _, name := range []string{"usage-archive.html", "fragment.html"} {
		s := pageRender(t, name, pageView())
		buttons := pageNodes(t, s, func(n *html.Node) bool { return pageAttr(n, "id") == "check-accounts" })
		if len(buttons) != 1 {
			t.Fatalf("%s buttons=%d", name, len(buttons))
		}
		if !pageClass(buttons[0], "btn-outline") || pageClass(buttons[0], "btn-primary") {
			t.Errorf("%s: plan check must be outline", name)
		}
		requireText(t, nodeHTML(t, buttons[0]), `href="#i-pulse"`)
		if !strings.Contains(nodeHTML(t, buttons[0].Parent), `class="legend"`) {
			t.Errorf("%s: no provider legend beside plan check", name)
		}
	}
}
func TestUsagePageSinglePriceNoteAndSpacing(t *testing.T) {
	shell := pageRender(t, "usage-archive.html", pageView())
	fragment := pageRender(t, "fragment.html", pageView())
	if n := strings.Count(shell+fragment, "Estimate at API list prices checked"); n != 1 {
		t.Errorf("price sentences=%d want 1", n)
	}
	if strings.Contains(fragment, "Estimate at API list prices checked") {
		t.Error("price note belongs in shell")
	}
	containers := pageNodes(t, shell, func(n *html.Node) bool { return pageAttr(n, "id") == "usage-data" })
	if len(containers) != 1 || pageClass(containers[0], "mt-8") {
		t.Error("toolbar must not inherit view margin")
	}
	requireText(t, fragment, "<div class=\"mt-8\">\n<section role=\"tabpanel\"")
}
func TestUsagePageAccountHierarchy(t *testing.T) {
	s := pageRender(t, "fragment.html", pageView())
	requireText(t, s, `<span class="ml-3"><span class="em">a@example.test</span><span class="alias">`+alias("a@example.test")+`</span></span>`, `<span class="data-mono ml-6">claude-opus-5-5</span>`, `<tfoot><tr class="tot all-only"><th scope="row" class="td text-left">All Providers</th>`)
}
func TestUsagePageReadableCountsAndCodes(t *testing.T) {
	s := pageRender(t, "fragment.html", pageView())
	requireText(t, s, ">12,345</td>", ">1,234,567</td>", ">3,659,839,758</td>", `<span class="data-mono">Sun, Oct 25, 2026</span>`, `class="td data-mono">7d_oi</td>`, ">Tokens</th>", ">Token Share</th>", ">Cost Share</th>", ">Unpriced</th>")
	live := pageRender(t, "accounts.html", accountsView{Now: pageView().Summary.Now, Rows: []accountRow{{Provider: "codex", Account: "a@example.test", Plan: "pro", Renews: "Oct 25 16:00 UTC+05:30"}}})
	requireText(t, stdhtml.UnescapeString(live), `<span class="data-mono">pro</span>`, `<span class="data-mono">Oct 25 16:00 UTC+05:30</span>`)
}
func TestUsagePageQuotaVisualKeysAndGroups(t *testing.T) {
	s := pageRender(t, "fragment.html", pageView())
	requireText(t, s, `class="h-2 w-5 rounded-full bg-t2"`, `class="stripes h-2 w-5 rounded-full"`, `class="mseg-absent h-2 w-5 rounded-full"`, `class="group-label"`, `>5 accounts, 1 disabled: credential`, `>1 account</span>`, `class="marker">Claude</span>`, `class="marker">Codex</span>`, `class="track w-24 h-1.5"`, `class="badge badge-warn">Stale</span>`, `class="badge">Not Observed Since Start</span>`)
}
func TestUsagePageWrappingLiveDetails(t *testing.T) {
	now := pageView().Summary.Now
	for _, row := range []accountRow{{Provider: "codex", Account: "a@example.test", Error: signInError(accountFakeStatus{})}, {Provider: "codex", Account: "b@example.test", Expiries: []string{"Reset 1: does not expire"}}} {
		s := pageRender(t, "accounts.html", accountsView{Now: now, Rows: []accountRow{row}})
		requireText(t, s, `<tr class="has-sub">`, `<tr class="sub"><td class="td" colspan="7">`, `class="flex flex-col gap-2 py-3"`)
	}
}
func TestUsagePageAccountIdentityGap(t *testing.T) {
	s := pageRender(t, "fragment.html", pageView())
	ns := pageNodes(t, s, func(n *html.Node) bool {
		return pageClass(n, "identity") && strings.Contains(pageText(n, true), "a@example.test")
	})
	if len(ns) < 2 {
		t.Fatalf("identity wrappers=%d want account card and daily row", len(ns))
	}
	for _, n := range ns {
		if !pageClass(n, "gap-2") {
			t.Error("identity dot lacks gap-2")
		}
		var email, masked string
		for _, child := range pageNodes(t, nodeHTML(t, n), func(child *html.Node) bool { return pageClass(child, "em") || pageClass(child, "alias") }) {
			if pageClass(child, "em") {
				email = pageText(child, false)
			} else {
				masked = pageText(child, false)
			}
		}
		if email != "a@example.test" || masked != alias("a@example.test") || !strings.HasSuffix(pageText(n, true), ", Claude") {
			t.Errorf("provider accessible suffix=%q", pageText(n, true))
		}
	}
}
func TestUsagePagePriceDateFromFile(t *testing.T) {
	raw, err := os.ReadFile("../../../usagearchive/prices.json")
	if err != nil {
		t.Fatal(err)
	}
	var price map[string]any
	if err = json.Unmarshal(raw, &price); err != nil {
		t.Fatal(err)
	}
	price["checked"] = "2026-09-17"
	raw, err = json.Marshal(price)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "prices.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	raw, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct{ Checked string }
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	checked, err := time.Parse("2006-01-02", fixture.Checked)
	if err != nil {
		t.Fatal(err)
	}
	page := pageRender(t, "usage-archive.html", map[string]any{"PriceChecked": checked})
	requireText(t, page, "Estimate at API list prices checked Sep 17, 2026.")
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	(&Handler{}).GetUsageArchivePage(c)
	if w.Code != 200 {
		t.Fatalf("page status=%d", w.Code)
	}
	var embedded struct{ Checked string }
	raw, err = os.ReadFile("../../../usagearchive/prices.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &embedded); err != nil {
		t.Fatal(err)
	}
	checked, err = time.Parse("2006-01-02", embedded.Checked)
	if err != nil {
		t.Fatal(err)
	}
	requireText(t, w.Body.String(), "Estimate at API list prices checked "+dateTime(checked)+".")
}

func TestUsagePageEveryMoneySiteGroupsLargeAmounts(t *testing.T) {
	v := pageView()
	cost := usagearchive.Cost{Input: 19727.87, CacheRead: 1234567.891, CacheWrite: 999.995, Output: 19727.87, Total: 19727.87, Savings: -19727.87, Premium: 1234567.891}
	v.Summary.All.Cost = cost
	for _, groups := range [][]usagearchive.Group{v.Summary.Providers, v.Summary.Accounts, v.Summary.Groups, v.Summary.Daily, v.Summary.AllTiers, v.Summary.Tiers, v.Summary.Days} {
		for i := range groups {
			groups[i].Cost = cost
		}
	}
	v.Summary.AllTiers = append(v.Summary.AllTiers, usagearchive.Group{Tier: "fast", Requests: 1, Cost: cost, CostShare: 25})
	for i := range v.Charts {
		v.Charts[i].Geometry = usageChart(v.Summary, v.Charts[i].Metric)
	}
	s := pageRender(t, "fragment.html", v)
	amounts := regexp.MustCompile(`\$(-?[0-9][0-9,]*(?:\.[0-9]+)?)`).FindAllStringSubmatch(s, -1)
	if len(amounts) < 30 {
		t.Fatalf("money coverage too small: %d amounts", len(amounts))
	}
	for _, amount := range amounts {
		value, err := strconv.ParseFloat(strings.ReplaceAll(amount[1], ",", ""), 64)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(value) >= 1000 && !strings.Contains(amount[1], ",") {
			t.Errorf("ungrouped money in rendered fragment: %s", amount[0])
		}
		_, decimals, found := strings.Cut(amount[1], ".")
		if !found || len(decimals) != 2 {
			t.Errorf("money must have exactly two decimals: %s", amount[0])
		}
	}
	if strings.Contains(s, "$-") {
		t.Error("negative money places minus after dollar sign")
	}
	requireText(t, s, "$19,727.87", "$1,234,567.89", "$1,000.00", "-$19,727.87")
	// Every money-bearing surface is populated with a large amount, including the cost chart.
	for _, label := range []string{"Spend, Last 30 Days", "Cost by Type", "Cost by Speed Tier", "Providers", "Breakdown Tables", "Accounts and Models", "Daily Log"} {
		requireText(t, s, label)
	}
}
