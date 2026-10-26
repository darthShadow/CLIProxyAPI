package management

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func fragmentFixture(t *testing.T, model string, n int) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CPA_USAGE_DIR", dir)
	now := time.Now()
	var b strings.Builder
	for i := 0; i < n; i++ {
		raw, _ := json.Marshal(map[string]any{"ts": now.UTC().Format(time.RFC3339), "provider": "claude", "account": "a@example.test", "model": model, "input": 100, "cache_write": 10, "output": 30})
		b.Write(raw)
		b.WriteByte('\n')
	}
	if e := os.WriteFile(filepath.Join(dir, "usage-"+now.Format("2006-01-02")+".jsonl"), []byte(b.String()), 0600); e != nil {
		t.Fatal(e)
	}
}
func archiveResponse(h *Handler) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	h.GetUsageArchive(c)
	c.Writer.WriteHeaderNow()
	return w
}
func requireText(t *testing.T, s string, parts ...string) {
	t.Helper()
	for _, p := range parts {
		if !strings.Contains(s, p) {
			t.Errorf("missing %q in fragment: %.1500s", p, s)
		}
	}
}
func TestUsageArchiveFragmentEscapesStrings(t *testing.T) {
	fragmentFixture(t, `<script>alert("m")</script>`, 1)
	w := archiveResponse(&Handler{})
	if w.Code != 200 {
		t.Fatalf("status=%d want=200", w.Code)
	}
	requireText(t, w.Body.String(), "&lt;script&gt;")
	if strings.Contains(w.Body.String(), `<script>alert`) {
		t.Fatal("unescaped model")
	}
}
func TestUsageArchiveFragmentShowsRecordingOff(t *testing.T) {
	t.Setenv("CPA_USAGE_DIR", "")
	w := archiveResponse(&Handler{})
	if w.Code != 200 {
		t.Fatalf("status=%d want=200", w.Code)
	}
	requireText(t, w.Body.String(), "Recording is off", "CPA_USAGE_DIR")
	fragmentFixture(t, "claude-opus-5-5", 1)
	old := redisqueue.UsageStatisticsEnabled()
	redisqueue.SetUsageStatisticsEnabled(false)
	t.Cleanup(func() { redisqueue.SetUsageStatisticsEnabled(old) })
	w = archiveResponse(&Handler{})
	requireText(t, w.Body.String(), "Recording is off")
}
func TestUsageArchiveFragmentReportsReadError(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "file")
	os.WriteFile(p, nil, 0600)
	t.Setenv("CPA_USAGE_DIR", p)
	w := archiveResponse(&Handler{})
	var body struct{ Error string }
	json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 500 || !strings.HasPrefix(body.Error, "usage archive:") {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}
func quotaFixtures() ([]*coreauth.Auth, time.Time) {
	now := time.Date(2026, 10, 25, 16, 0, 0, 0, time.FixedZone("host", 19800))
	var as []*coreauth.Auth
	for i, label := range []string{"A", "B", "C", "D"} {
		a := &coreauth.Auth{ID: label, Provider: "claude", Attributes: map[string]string{"auth_kind": "oauth"}, Metadata: map[string]any{"email": label + "@example.test"}}
		if i < 3 {
			used := []float64{.42, 0, .9}[i]
			reset := now.Add(time.Duration(i+1) * time.Hour)
			if i == 2 {
				reset = now.Add(-time.Hour)
			}
			a.Quota = coreauth.QuotaState{ObservedAt: now.Add(-2 * time.Hour), Signals: map[string]string{"aNtHrOpIc-Ratelimit-Unified-5h-Utilization": fmt.Sprint(used), "anthropic-ratelimit-unified-5h-reset": fmt.Sprint(reset.Unix()), "Anthropic-Ratelimit-Unified-7d_oi-Utilization": "0.2", "Anthropic-Ratelimit-Unified-7d_oi-Reset": fmt.Sprint(now.Add(24 * time.Hour).Unix())}}
		}
		as = append(as, a)
	}
	as = append(as, &coreauth.Auth{ID: "key", Provider: "claude", Attributes: map[string]string{"api_key": "sk-fake-upstream-1111"}}, &coreauth.Auth{ID: "disabled", Provider: "claude", Disabled: true, Attributes: map[string]string{"auth_kind": "oauth"}}, &coreauth.Auth{ID: "codex", Provider: "codex", Attributes: map[string]string{"auth_kind": "oauth"}, Quota: coreauth.QuotaState{ObservedAt: now, Signals: map[string]string{"X-Codex-Primary-Used-Percent": "17", "X-Codex-Primary-Window-Minutes": "300", "X-Codex-Primary-Reset-After-Seconds": "1800", "X-Codex-Secondary-Used-Percent": "30", "X-Codex-Secondary-Window-Minutes": "10080"}}})
	return as, now
}
func TestPoolQuotaSumsObservedWindows(t *testing.T) {
	as, now := quotaFixtures()
	v := poolQuota(as, now, nil)
	var found bool
	for _, p := range v.Pools {
		if p.Provider == "claude" && p.Window == "5-Hour" {
			found = true
			if p.Used != 42 || p.Total != 200 || p.Fresh != 2 || p.Accounts != 4 || p.Stale != 1 || !p.Reset.Equal(now.Add(time.Hour)) {
				t.Errorf("pool=%+v", p)
			}
		}
	}
	if !found {
		t.Fatal("missing Claude 5-Hour pool")
	}
	states := map[string]string{}
	for _, a := range v.Accounts {
		if a.Window == "5-Hour" && a.Provider == "claude" {
			states[a.Account] = a.State
			if a.Account == "B@example.test" && (a.State != "fresh" || a.Used != 0) {
				t.Error("fresh zero missing")
			}
			if a.Account == "C@example.test" && (a.State != "stale" || a.Used != 0 || !a.Reset.Equal(now.Add(-time.Hour)) || !a.Observed.Equal(now.Add(-2*time.Hour))) {
				t.Errorf("stale row=%+v", a)
			}
		}
		if strings.Contains(a.Account, "disabled") || strings.Contains(a.Account, "key") {
			t.Error("excluded account present")
		}
	}
	if states["D@example.test"] != "absent" {
		t.Error("absent account missing")
	}
	var names []string
	for _, p := range v.Pools {
		names = append(names, p.Window)
	}
	requireText(t, strings.Join(names, ","), "5-Hour", "Weekly", "7d_oi")
}

type countingAccountReader struct{ calls []string }

func (r *countingAccountReader) Codex(context.Context, *config.Config, *coreauth.Auth) (helps.CodexAccountUsage, error) {
	r.calls = append(r.calls, "codex")
	return helps.CodexAccountUsage{}, nil
}
func (r *countingAccountReader) Claude(context.Context, *config.Config, *coreauth.Auth) (*claude.OAuthPlan, error) {
	r.calls = append(r.calls, "claude")
	return &claude.OAuthPlan{}, nil
}
func TestUsageArchiveFragmentShowsCostAndQuota(t *testing.T) {
	if got := pageUsageText(t, `<span class="ytick">$0.00</span><td>$0.00</td>`); got != "$0.00" {
		t.Fatalf("unpriced check must retain a usage-cell zero while excluding the axis: %q", got)
	}

	fragmentFixture(t, "claude-3-7-sonnet", 1)
	r := &countingAccountReader{}
	old := liveAccountReader
	liveAccountReader = r
	t.Cleanup(func() { liveAccountReader = old })
	manager := coreauth.NewManager(nil, nil, nil)
	as, _ := quotaFixtures()
	for _, a := range as {
		if _, err := manager.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
	}
	w := archiveResponse(&Handler{authManager: manager})
	if w.Code != 200 {
		t.Fatalf("status=%d want=200", w.Code)
	}
	requireText(t, w.Body.String(), "Overview", "No Price", "Quota", "Not Available", "A@example.test", "5-Hour", "7d_oi", "Stale", "Not Observed Since Start")
	page := httptest.NewRecorder()
	pageContext, _ := gin.CreateTestContext(page)
	(&Handler{}).GetUsageArchivePage(pageContext)
	requireText(t, page.Body.String(), "Oct 25, 2026")
	if len(r.calls) != 0 {
		t.Error("GET made live reads")
	}
	if strings.Contains(pageUsageText(t, w.Body.String()), "$0.00") {
		t.Error("unpriced row shows zero dollars")
	}
}
func TestPoolQuotaSegments(t *testing.T) {
	as, now := quotaFixtures()
	v := poolQuota(as, now, nil)
	var segments []quotaSegment
	for _, p := range v.Pools {
		if p.Provider == "claude" && p.Window == "5-Hour" {
			segments = p.Segments
		}
	}
	if len(segments) != 4 {
		t.Fatalf("segments=%+v want four", segments)
	}
	for i, want := range []string{"fresh", "fresh", "stale", "absent"} {
		if segments[i].State != want {
			t.Errorf("segment %d=%+v", i, segments[i])
		}
	}
	if segments[0].Used != 42 || segments[1].Used != 0 {
		t.Error("segment percentages incorrect")
	}
	for _, tc := range []struct{ value, severity string }{{"0.75", "warn"}, {"0.9", "bad"}} {
		as[0].Quota.Signals["aNtHrOpIc-Ratelimit-Unified-5h-Utilization"] = tc.value
		v = poolQuota(as, now, nil)
		for _, p := range v.Pools {
			if p.Provider == "claude" && p.Window == "5-Hour" && p.Segments[0].Severity != tc.severity {
				t.Errorf("severity=%q want=%q", p.Segments[0].Severity, tc.severity)
			}
		}
	}
}
func TestUsageArchiveFragmentPillsAndCopy(t *testing.T) {
	fragmentFixture(t, "unknown", 71)
	dir := os.Getenv("CPA_USAGE_DIR")
	now := time.Now()
	f, e := os.OpenFile(filepath.Join(dir, "usage-"+now.Format("2006-01-02")+".jsonl"), os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	fmt.Fprintf(f, "{\"ts\":%q,\"provider\":\"claude\",\"account\":\"a@example.test\",\"model\":\"claude-opus-5-5\",\"input\":100}\n", now.UTC().Format(time.RFC3339))
	f.Close()
	w := archiveResponse(&Handler{})
	if w.Code != 200 {
		t.Fatalf("status=%d want=200", w.Code)
	}
	s := w.Body.String()
	requireText(t, s, `<span aria-hidden="true">71 unpriced</span>`, `71 requests with no price`, "No Price", "Appends Lost", "Unreadable Lines Skipped", "Partial Day", "Not Available")
	fragmentFixture(t, "unknown", 1)
	w = archiveResponse(&Handler{})
	requireText(t, w.Body.String(), "1 request with no price")
	if strings.Contains(s, "Times are host time") || strings.Contains(pageUsageText(t, w.Body.String()), "$0.00") {
		t.Error("forbidden copy or unpriced zero")
	}
}
func TestUsageArchiveFragmentStackedAreasAndPartialDay(t *testing.T) {
	fragmentFixture(t, "claude-opus-5-5", 1)
	s := archiveResponse(&Handler{}).Body.String()
	polygons := regexp.MustCompile(`<polygon points="([^"]*)"`).FindAllStringSubmatch(s, -1)
	if len(polygons) < 2 {
		t.Fatalf("missing stacked polygons: %d", len(polygons))
	}
	points := strings.Fields(polygons[1][1])
	if len(points) != 60 || !strings.HasPrefix(points[29], "983.3,") || !strings.HasPrefix(points[30], "983.3,") {
		t.Errorf("Codex area must return along upper edge in reverse: %v", points)
	}
	if !strings.Contains(s, `stroke-dasharray="4 4"`) {
		t.Error("today has no dashed partial-day segment")
	}
}
func TestUsageArchiveFragmentMatchesApprovedViewStructure(t *testing.T) {
	fragmentFixture(t, "claude-opus-5-5", 1)
	w := archiveResponse(&Handler{})
	s := w.Body.String()
	requireText(t, s, "Cost by Type", "Cost by Speed Tier", ">Tokens</h3>", ">Providers</h3>", "Plans and Codex Resets", `class="tiles"`, `class="p-claude">1 row`, `class="p-codex">0 rows`, "Rounded values. The exact counts per provider are in Breakdown Tables, Totals by Type.", "Cache Write: the recorded total.")
	captions := s + accountsResponse(&Handler{}, "POST").Body.String()
	if strings.Count(captions, "<caption") < 8 {
		t.Errorf("tables lack captions: got %d", strings.Count(captions, "<caption"))
	}
}
