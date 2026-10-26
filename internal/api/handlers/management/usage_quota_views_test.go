package management

import (
	"fmt"
	"github.com/gin-gonic/gin"
	"golang.org/x/net/html"
	stdhtml "html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRunsOutAtObservedAt(t *testing.T) {
	observed := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name      string
		used      float64
		remaining time.Duration
		at        time.Time
		lasts     bool
	}{
		{"fast", 75, 84 * time.Hour, observed.Add(28 * time.Hour), false},
		{"slow", 25, 84 * time.Hour, time.Time{}, true},
		{"zero", 0, 84 * time.Hour, time.Time{}, true},
		{"spent", 100, 84 * time.Hour, observed, false},
		{"past", 50, -time.Hour, time.Time{}, false},
		{"too long", 50, 169 * time.Hour, time.Time{}, false},
		{"no elapsed", 50, 168 * time.Hour, time.Time{}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, lasts := runsOut(tc.used, observed.Add(tc.remaining), observed)
			if !at.Equal(tc.at) || lasts != tc.lasts {
				t.Errorf("runsOut=(%v,%v) want=(%v,%v)", at, lasts, tc.at, tc.lasts)
			}
		})
	}
}

func snapshotAuths() ([]*coreauth.Auth, time.Time) {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.UTC)
	var auths []*coreauth.Auth
	for i, used := range []float64{.5, .2} {
		a := liveAuth(fmt.Sprint(i), "claude")
		a.Index = fmt.Sprintf("%016x", i+1)
		a.Quota = coreauth.QuotaState{ObservedAt: now, Signals: map[string]string{
			"Anthropic-Ratelimit-Unified-7d-Utilization": fmt.Sprint(used),
			"Anthropic-Ratelimit-Unified-7d-Reset":       fmt.Sprint(now.Add(time.Duration(84+i) * time.Hour).Unix()),
			"Anthropic-Ratelimit-Unified-5h-Utilization": fmt.Sprint(used),
			"Anthropic-Ratelimit-Unified-5h-Reset":       fmt.Sprint(now.Add(time.Duration(i+1) * time.Hour).Unix()),
		}}
		auths = append(auths, a)
	}
	return auths, now
}

func TestUsageArchiveFragmentShowsRunsOut(t *testing.T) {
	as, now := snapshotAuths()
	as[0].Quota.Signals["Anthropic-Ratelimit-Unified-7d-Utilization"] = "0.75"
	v := pageView()
	v.Quota = poolQuota(as, now, nil)
	s := pageRender(t, "fragment.html", v)
	requireText(t, stdhtml.UnescapeString(s), ">Runs Out</th>", "Lasts to reset", cellTime(now.Add(28*time.Hour)))
	localNow := now.In(time.FixedZone("page", 19800))
	for _, row := range poolQuota(as, localNow, nil).Accounts {
		if !row.RunsOut.IsZero() {
			_, got := row.RunsOut.Zone()
			_, want := localNow.Zone()
			if got != want {
				t.Errorf("Runs Out timezone offset=%d want page offset=%d", got, want)
			}
		}
	}
	for _, row := range pageNodes(t, s, func(n *html.Node) bool { return n.Data == "tr" }) {
		if strings.Contains(pageText(row, false), "5-Hour") {
			var cells []*html.Node
			for c := row.FirstChild; c != nil; c = c.NextSibling {
				if c.Data == "td" || c.Data == "th" {
					cells = append(cells, c)
				}
			}
			if len(cells) != 6 || strings.TrimSpace(pageText(cells[5], false)) != "" {
				t.Errorf("5-Hour Runs Out cell must be empty: %s", nodeHTML(t, row))
			}
		}
	}
}

func TestPoolQuotaWeightsBothWindows(t *testing.T) {
	as, now := snapshotAuths()
	as[0].Attributes[coreauth.AttributeWeight] = "6"
	as[1].Metadata[coreauth.AttributeWeight] = float64(3)
	for _, p := range poolQuota(as, now, nil).Pools {
		if p.Used != 360 || p.Total != 900 || len(p.Segments) != 2 || p.Segments[0].Weight != 6 || p.Segments[1].Weight != 3 {
			t.Errorf("weighted %s pool=%+v want 360 of 900, weights 6 and 3", p.Window, p)
		}
	}
	for _, tc := range []struct {
		a    *coreauth.Auth
		want int64
	}{
		{nil, 1}, {liveAuth("default", "claude"), 1},
		{&coreauth.Auth{Attributes: map[string]string{coreauth.AttributeWeight: "invalid"}}, 0},
		{&coreauth.Auth{Attributes: map[string]string{coreauth.AttributeWeight: "2"}, Metadata: map[string]any{coreauth.AttributeWeight: float64(3)}}, 2},
	} {
		if got := coreauth.AuthWeight(tc.a); got != tc.want {
			t.Errorf("AuthWeight=%d want %d", got, tc.want)
		}
	}
	v := pageView()
	v.Quota = poolQuota(as, now, nil)
	s := pageRender(t, "fragment.html", v)
	requireText(t, s, "of 900.0%", "weight 6", "weight 3", "flex-grow:6", "flex-grow:3")
}
func TestPoolQuotaZeroWeightNotCounted(t *testing.T) {
	as, now := snapshotAuths()
	as[0].Attributes[coreauth.AttributeWeight] = "0"
	v := pageView()
	v.Quota = poolQuota(as, now, nil)
	for _, p := range v.Quota.Pools {
		if p.Used != 20 || p.Total != 100 || p.Fresh != 1 {
			t.Errorf("zero weight %s pool=%+v want 20 of 100, 1 fresh", p.Window, p)
		}
	}
	requireText(t, pageRender(t, "fragment.html", v), "weight 0, not counted")
}

func TestPoolQuotaEstimatesGainAtNextReset(t *testing.T) {
	as, now := snapshotAuths()
	as[0].Attributes[coreauth.AttributeWeight] = "6"
	as[1].Attributes[coreauth.AttributeWeight] = "3"
	for _, p := range poolQuota(as, now, nil).Pools {
		if p.Gain != 300 {
			t.Errorf("%s gain=%v want 300", p.Window, p.Gain)
		}
	}
	// Reset observations in the same minute contribute to the same estimated gain.
	as[1].Quota.Signals["Anthropic-Ratelimit-Unified-7d-Reset"] = fmt.Sprint(now.Add(84*time.Hour + 30*time.Second).Unix())
	for _, p := range poolQuota(as, now, nil).Pools {
		if p.Window == "7-Day" && p.Gain != 360 {
			t.Errorf("same-minute gain=%v want 360", p.Gain)
		}
	}
}
func TestUsageArchiveFragmentShowsGain(t *testing.T) {
	as, now := snapshotAuths()
	as[0].Attributes[coreauth.AttributeWeight] = "6"
	as[1].Attributes[coreauth.AttributeWeight] = "3"
	v := pageView()
	v.Quota = poolQuota(as, now, nil)
	requireText(t, pageRender(t, "fragment.html", v), "+300.0% at next reset, estimated at last observation")
}

func TestPoolQuotaExcludesUnselectedAccounts(t *testing.T) {
	as, now := snapshotAuths()
	v := poolQuota(as, now, map[string]bool{as[0].Index: true, "000000000000ffff": true})
	for _, p := range v.Pools {
		if p.Used != 20 || p.Total != 100 || p.Accounts != 1 || p.Gain != 20 {
			t.Errorf("excluded %s pool=%+v want 20 of 100, 1 counted, gain 20", p.Window, p)
		}
	}
	if len(v.Accounts) != 4 {
		t.Errorf("window rows=%d want 4 including excluded account", len(v.Accounts))
	}
	// An excluded account that resets with the counted one adds no gain.
	for _, window := range []string{"7d", "5h"} {
		key := "Anthropic-Ratelimit-Unified-" + window + "-Reset"
		as[0].Quota.Signals[key] = as[1].Quota.Signals[key]
	}
	for _, p := range poolQuota(as, now, map[string]bool{as[0].Index: true}).Pools {
		if p.Gain != 20 {
			t.Errorf("same-reset excluded %s gain=%v want 20", p.Window, p.Gain)
		}
	}
}

func TestQuotaQueryRejectsBadValues(t *testing.T) {
	for _, tc := range []struct{ key, value string }{
		{"qp", "claude"}, {"qp", "other"}, {"qv", "other"}, {"qburn", "2"}, {"qfuture", "other"}, {"qpage", "-5"}, {"qpage", "1"}, {"qpage", "0.0"}, {"qx", "ABCDEF0123456789"}, {"qx", "not-an-index"},
	} {
		t.Run(tc.key+tc.value, func(t *testing.T) {
			values := url.Values{tc.key: {tc.value}}
			if _, err := parseQuotaQuery(values); err == nil || err.Error() != "usage archive: bad "+tc.key {
				t.Errorf("parse error=%v want bad %s", err, tc.key)
			}
			w := quotaQueryResponse(&Handler{}, values)
			if w.Code != 400 || w.Body.String() != `{"error":"usage archive: bad `+tc.key+`"}` {
				t.Errorf("status=%d body=%.200s", w.Code, w.Body.String())
			}
		})
	}
	q, err := parseQuotaQuery(nil)
	if err != nil || q.Provider != "all" || q.View != "pool" || q.Burn != 3 || q.Future != "burn" || q.Page != 0 {
		t.Errorf("defaults=%+v err=%v", q, err)
	}
	for _, page := range []string{"-4", "-3", "-2", "-1", "0"} {
		q, err = parseQuotaQuery(url.Values{"qp": {"Codex"}, "qv": {"drain"}, "qburn": {"6"}, "qfuture": {"average"}, "qpage": {page}, "qx": {"0000000000000001", "0000000000000002"}})
		if err != nil || q.Provider != "Codex" || q.View != "drain" || q.Burn != 6 || q.Future != "average" || fmt.Sprint(q.Page) != page || !q.Exclude["0000000000000001"] || !q.Exclude["0000000000000002"] {
			t.Errorf("valid query=%+v err=%v", q, err)
		}
	}
	for _, burn := range []string{"1", "3"} {
		if _, err := parseQuotaQuery(url.Values{"qp": {"Claude"}, "qburn": {burn}}); err != nil {
			t.Errorf("valid query: %v", err)
		}
	}
}
func quotaQueryResponse(h *Handler, values url.Values) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/?"+values.Encode(), nil)
	h.GetUsageArchive(c)
	c.Writer.WriteHeaderNow()
	return w
}

func TestPoolQuotaCountsDisabledAccounts(t *testing.T) {
	as, now := snapshotAuths()
	as[1].Disabled = true
	disabled := liveAuth("disabled-codex", "codex")
	disabled.Status = coreauth.StatusDisabled
	as = append(as, disabled)
	v := pageView()
	v.Quota = poolQuota(as, now, nil)
	if len(v.Quota.Groups) != 2 {
		t.Fatalf("groups=%+v want Claude and disabled-only Codex", v.Quota.Groups)
	}
	for i, g := range v.Quota.Groups {
		want := 2 - i
		if g.Accounts != want || len(g.Disabled) != 1 {
			t.Errorf("group=%+v want %d accounts, 1 disabled", g, want)
		}
		if g.Provider == "codex" && len(g.Pools) != 0 {
			t.Errorf("disabled Codex pooled: %+v", g.Pools)
		}
	}
	if len(v.Quota.Accounts) != 2 {
		t.Errorf("enabled window rows=%d want 2", len(v.Quota.Accounts))
	}
	s := pageRender(t, "fragment.html", v)
	requireText(t, s, "2 accounts, 1 disabled:", "1 account, 1 disabled:", "disabled-codex@example.test")
}

func TestUsageArchiveFragmentQuotaProviderTabs(t *testing.T) {
	s := pageRender(t, "fragment.html", pageView())
	panels := pageNodes(t, s, func(n *html.Node) bool { return pageAttr(n, "id") == "panel-quota" })
	if len(panels) != 1 {
		t.Fatal("missing quota panel")
	}
	panel := nodeHTML(t, panels[0])
	for _, tc := range []struct{ value, label string }{{"all", "All: 6 accounts"}, {"Claude", "Claude: 5 accounts"}, {"Codex", "Codex: 1 account"}} {
		radios := pageNodes(t, panel, func(n *html.Node) bool {
			return n.Data == "input" && pageAttr(n, "name") == "qp" && pageAttr(n, "value") == tc.value
		})
		if len(radios) != 1 {
			t.Errorf("qp=%s radios=%d want 1", tc.value, len(radios))
			continue
		}
		if !strings.Contains(pageText(radios[0].Parent, false), tc.label) {
			t.Errorf("label=%q want %q", pageText(radios[0].Parent, false), tc.label)
		}
	}
	groups := pageNodes(t, panel, func(n *html.Node) bool { return n.Data == "h3" && pageClass(n, "group-label") })
	for _, g := range groups {
		if pageAttr(g.Parent, "data-p") == "" {
			t.Error("quota group has no data-p")
		}
	}
	rows := pageNodes(t, panel, func(n *html.Node) bool { return n.Data == "tr" && n.Parent != nil && n.Parent.Data == "tbody" })
	if len(rows) == 0 {
		t.Fatal("missing quota rows")
	}
	for _, row := range rows {
		if pageAttr(row, "data-p") == "" {
			t.Error("quota row has no data-p")
		}
	}
	cols := pageNodes(t, panel, func(n *html.Node) bool { return pageClass(n, "cols") })
	if len(cols) != 1 {
		t.Errorf("quota .cols wrappers=%d want 1", len(cols))
	}
}

func TestUsageArchiveFragmentEchoesControls(t *testing.T) {
	as, _ := snapshotAuths()
	as[0].Attributes[coreauth.AttributeWeight] = "0"
	h := accountsHandler(t, as, &accountFixtureReader{})
	first := quotaQueryResponse(h, nil)
	if first.Code != 200 {
		t.Fatalf("initial status=%d", first.Code)
	}
	radios := pageNodes(t, first.Body.String(), func(n *html.Node) bool {
		return n.Data == "input" && pageAttr(n, "name") == "qp" && strings.Contains(pageText(n.Parent, false), "Codex:")
	})
	if len(radios) != 1 {
		t.Fatalf("Codex radios=%d want 1", len(radios))
	}
	value := pageAttr(radios[0], "value")
	excluded := as[1].Index
	w := quotaQueryResponse(h, url.Values{"qp": {value}, "qx": {excluded}, "qv": {"drain"}})
	if w.Code != 200 {
		t.Fatalf("round-trip status=%d body=%.200s", w.Code, w.Body.String())
	}
	s := w.Body.String()
	forms := pageNodes(t, s, func(n *html.Node) bool { return n.Data == "form" && pageAttr(n, "id") == "quota-controls" })
	if len(forms) != 1 {
		t.Fatalf("quota forms=%d want 1", len(forms))
	}
	form := nodeHTML(t, forms[0])
	checked := pageNodes(t, form, func(n *html.Node) bool {
		return n.Data == "input" && pageAttr(n, "name") == "qp" && hasPageAttr(n, "checked")
	})
	if len(checked) != 1 || pageAttr(checked[0], "value") != "Codex" {
		t.Errorf("checked provider=%v want only Codex", checked)
	}
	viewRadios := pageNodes(t, form, func(n *html.Node) bool {
		return n.Data == "input" && pageAttr(n, "name") == "qv" && hasPageAttr(n, "checked")
	})
	if len(viewRadios) != 1 || pageAttr(viewRadios[0], "value") != "drain" {
		t.Errorf("checked quota view=%v want only drain", viewRadios)
	}
	requireText(t, form, "Pool Left", "Drain Rate")

	boxes := pageNodes(t, form, func(n *html.Node) bool {
		return n.Data == "input" && pageAttr(n, "type") == "checkbox" && hasPageAttr(n, "data-recompute")
	})
	if len(boxes) != 2 {
		t.Fatalf("account boxes=%d want 2", len(boxes))
	}
	for _, box := range boxes {
		if pageAttr(box, "value") == excluded && hasPageAttr(box, "checked") {
			t.Error("excluded account remains checked")
		}
		if strings.Contains(pageText(box.Parent, false), "weight 0") && !hasPageAttr(box, "disabled") {
			t.Error("zero-weight box must be disabled")
		}
	}
	requireText(t, form, "Accounts (all providers): 0 of 2 counted", "Changes this page only; routing is unchanged.", "weight 0, not counted")
}

func TestQuotaHeadingCopyAndWeightedLabels(t *testing.T) {
	as, now := snapshotAuths()
	as[0].Attributes[coreauth.AttributeWeight] = "6"
	as[1].Attributes[coreauth.AttributeWeight] = "3"
	v := pageView()
	v.Quota = poolQuota(as, now, nil)
	markup := pageRender(t, "fragment.html", v)
	forms := pageNodes(t, markup, func(n *html.Node) bool { return n.Data == "form" && pageAttr(n, "id") == "quota-controls" })
	form := forms[0]
	if form.FirstChild == nil || !pageClass(form.FirstChild, "view-head") || !strings.Contains(pageText(form.FirstChild, false), "Quota and Plans") {
		t.Error("quota heading and legend must be the first content inside the controls form")
	}
	requireText(t, pageText(form, false), "Pools apply account weights and the Accounts selection; this table keeps every account.", "A stale window's reset has passed; its old percent describes the previous window and is not shown.")
	labels := pageNodes(t, nodeHTML(t, form), func(n *html.Node) bool {
		return n.Data == "span" && pageClass(n, "text-center") && strings.Contains(pageText(n, false), "weight ")
	})
	if len(labels) != 4 {
		t.Fatalf("weighted segment labels=%d want 4", len(labels))
	}
	for i, label := range labels {
		weight := []string{"6", "3"}[i%2]
		if got := pageAttr(label, "style"); got != "flex-grow:"+weight {
			t.Errorf("segment label %d style=%q want flex-grow:%s", i, got, weight)
		}
	}
}
func hasPageAttr(n *html.Node, key string) bool {
	for _, a := range n.Attr {
		if a.Key == key {
			return true
		}
	}
	return false
}

func TestUsageArchiveFragmentMasksEveryEmail(t *testing.T) {
	v := pageView()
	v.Quota.Groups[0].Disabled = []string{"disabled@example.test"}
	s := pageRender(t, "fragment.html", v)
	emails := pageNodes(t, s, func(n *html.Node) bool { return n.Type == html.TextNode && strings.Contains(n.Data, "@") })
	if len(emails) < 8 {
		t.Fatalf("email sites=%d want archive and snapshot sites", len(emails))
	}
	for _, n := range emails {
		if !pageClass(n.Parent, "em") {
			t.Errorf("email outside .em: %q", n.Data)
		}
		if alias(n.Data) == "" {
			t.Errorf("missing alias for %q", n.Data)
		}
	}
	requireText(t, s, `class="alias">account `)
	if alias("credential 1234") != "" || alias(" A@EXAMPLE.TEST ") != alias("a@example.test") || alias("a@example.test") == alias("b@example.test") {
		t.Error("alias must be stable, case-insensitive, and email-only")
	}
}
func TestUsageAccountsMasksEmail(t *testing.T) {
	s := pageRender(t, "accounts.html", accountsView{Rows: []accountRow{{Provider: "claude", Account: "a@example.test"}}})
	requireText(t, s, `<span class="em">a@example.test</span><span class="alias">`+alias("a@example.test")+`</span>`)
}
func TestUsageArchiveShellHasShowEmailsToggle(t *testing.T) {
	s := pageRender(t, "usage-archive.html", pageView())
	ns := pageNodes(t, s, func(n *html.Node) bool { return pageAttr(n, "id") == "show-emails" })
	if len(ns) != 1 {
		t.Fatalf("Show Emails toggles=%d want 1", len(ns))
	}
	n := ns[0]
	if pageAttr(n, "type") != "checkbox" || hasPageAttr(n, "checked") || pageAttr(n, "autocomplete") != "off" {
		t.Errorf("toggle=%s", nodeHTML(t, n))
	}
	if !strings.Contains(pageText(n.Parent, false), "Show Emails") {
		t.Error("toggle needs Show Emails label")
	}
	for p := n.Parent; p != nil; p = p.Parent {
		if pageAttr(p, "id") == "usage-data" {
			t.Error("toggle must be in shell")
		}
	}
}

func TestUsageArchiveFragmentRendersRefreshAll(t *testing.T) {
	v := pageView()
	v.RefreshLabels = map[string]string{"fake-auth-id": "refresh@example.test"}
	for _, name := range []string{"fragment.html", "usage-archive.html"} {
		s := pageRender(t, name, v)
		buttons := pageNodes(t, s, func(n *html.Node) bool { return n.Data == "button" && pageAttr(n, "id") == "refresh-all" })
		if len(buttons) != 1 {
			t.Errorf("%s: refresh buttons=%d want 1", name, len(buttons))
		} else if pageText(buttons[0], false) != "Refresh All Credentials" {
			t.Errorf("refresh copy=%q", pageText(buttons[0], false))
		}
		templates := pageNodes(t, s, func(n *html.Node) bool { return n.Data == "template" && pageAttr(n, "id") == "refresh-labels" })
		if len(templates) != 1 {
			t.Errorf("%s: refresh label templates=%d want 1", name, len(templates))
		}
		if name == "usage-archive.html" && len(templates) == 1 {
			inside := false
			for parent := templates[0].Parent; parent != nil; parent = parent.Parent {
				if pageAttr(parent, "id") == "usage-data" {
					inside = true
				}
			}
			if !inside {
				t.Error("initial refresh-labels must be replaced with usage-data to avoid duplicate IDs after a swap")
			}
		}
		if name == "fragment.html" {
			requireText(t, s, `data-auth-id="fake-auth-id"`, `<span class="em">refresh@example.test</span><span class="alias">`+alias("refresh@example.test")+`</span>`)
			if strings.Contains(strings.ToLower(s), "<script") {
				t.Error("fragment contains script")
			}
		}
	}
	as, _ := snapshotAuths()
	h := accountsHandler(t, as, &accountFixtureReader{})
	s := quotaQueryResponse(h, nil).Body.String()
	for _, a := range as {
		requireText(t, s, `data-auth-id="`+a.ID+`"`)
	}
}
func TestUsageArchiveShellHasRefreshResult(t *testing.T) {
	shell := pageRender(t, "usage-archive.html", pageView())
	ns := pageNodes(t, shell, func(n *html.Node) bool { return pageAttr(n, "id") == "refresh-result" })
	if len(ns) != 1 {
		t.Fatalf("refresh-result elements=%d want 1", len(ns))
	}
	if pageAttr(ns[0], "role") != "status" {
		t.Error("refresh-result needs role=status")
	}
	for p := ns[0].Parent; p != nil; p = p.Parent {
		if pageAttr(p, "id") == "usage-data" {
			t.Error("refresh-result must be outside swapped fragment")
		}
	}
	if strings.Contains(pageRender(t, "fragment.html", pageView()), `id="refresh-result"`) {
		t.Error("refresh-result must be shell-only")
	}
}
