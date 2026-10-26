package management

import (
	"bytes"
	"crypto/sha256"
	"embed"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/redisqueue"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagearchive"
)

//go:embed usage_archive/*
var usageAssets embed.FS
var usageTemplates = template.Must(template.New("archive").Funcs(template.FuncMap{
	"alias":    alias,
	"factTime": factTime, "cellTime": cellTime, "clockTime": clockTime, "dateTime": dateTime, "dayTime": dayTime,
	"money": formatMoney, "count": groupedCount, "tierLabel": tierLabel, "typeRows": typeRows,
	"rowCount": rowCount, "compact": compactTokens, "requests": requestCount, "provider": providerLabel,
	"percent": func(v float64) string { return fmt.Sprintf("%.1f", v) },
	"localDay": func(day string, now time.Time) time.Time {
		v, _ := time.ParseInLocation("2006-01-02", day, now.Location())
		return v
	},
	"until": func(at, now time.Time) string {
		if at.IsZero() || !at.After(now) {
			return ""
		}
		return relativeTime(at.Sub(now))
	},
}).ParseFS(usageAssets, "usage_archive/*.html"))

type metricChart struct {
	Metric, Name string
	Geometry     chartGeometry
}
type archiveView struct {
	QuotaCharts   []quotaChartView
	RefreshLabels map[string]string
	Query         quotaQuery
	Summary       usagearchive.Summary
	Quota         quotaView
	Lost          uint64
	LastFailure   time.Time
	Off           bool
	Charts        []metricChart
}

func providerLabel(p string) string {
	switch p {
	case "claude":
		return "Claude"
	case "codex":
		return "Codex"
	case "":
		return "All Providers"
	}
	return p
}
func renderUsage(c *gin.Context, name string, data any) {
	var b bytes.Buffer
	if errExecuteTemplate := usageTemplates.ExecuteTemplate(&b, name, data); errExecuteTemplate != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usage archive: render failed"})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Data(http.StatusOK, "text/html; charset=utf-8", b.Bytes())
}

// GetUsageArchive returns escaped archive and passive quota fragments.
// It reads only local files and in-memory snapshots; it makes no live request.
func (h *Handler) GetUsageArchive(c *gin.Context) {
	query, err := parseQuotaQuery(c.Request.URL.Query())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	now := time.Now()
	dir := os.Getenv("CPA_USAGE_DIR")
	var s usagearchive.Summary
	if dir != "" {
		s, err = usagearchive.Summarize(dir, now)
	} else {
		s.Now = now
		s.End = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
		s.Start = s.End.AddDate(0, 0, -29)
	}
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "usage archive: " + err.Error()})
		return
	}
	lost, last := usagearchive.Losses()
	view := archiveView{Query: query, Summary: s, Lost: lost, LastFailure: last, Off: dir == "" || !redisqueue.UsageStatisticsEnabled()}
	if h.authManager != nil {
		auths := h.authManager.List()
		view.Quota = poolQuota(auths, now, query.Exclude)
		view.QuotaCharts = quotaChart(auths, s, query)
		view.RefreshLabels = make(map[string]string)
		for _, a := range usageInventory(auths) {
			view.RefreshLabels[a.ID] = usageAccount(a)
		}
	}
	for _, m := range []struct{ metric, name string }{{"cost", "Cost"}, {"tok", "Processed Tokens"}, {"req", "Requests"}} {
		view.Charts = append(view.Charts, metricChart{m.metric, m.name, usageChart(s, m.metric)})
	}
	renderUsage(c, "fragment.html", view)
}

// GetUsageArchivePage serves the shell with the price-file date and no account data.
func (h *Handler) GetUsageArchivePage(c *gin.Context) {
	usageHeaders(c)
	renderUsage(c, "usage-archive.html", struct{ PriceChecked time.Time }{usagearchive.PricesChecked()})
}

// GetUsageArchiveScript serves the external page script.
func (h *Handler) GetUsageArchiveScript(c *gin.Context) {
	serveUsageAsset(c, "usage-archive.js", "text/javascript; charset=utf-8")
}

func serveUsageAsset(c *gin.Context, name, contentType string) {
	b, err := usageAssets.ReadFile("usage_archive/" + name)
	if err != nil {
		c.Status(http.StatusInternalServerError)
		return
	}
	usageHeaders(c)
	c.Data(http.StatusOK, contentType, b)
}

func usageHeaders(c *gin.Context) {
	c.Header("Content-Security-Policy", "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'")
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Referrer-Policy", "no-referrer")
	c.Header("Cache-Control", "no-store")
}

type quotaQuery struct {
	Provider, View, Future string
	Burn, Page             int
	Exclude                map[string]bool
}

var quotaAuthIndex = regexp.MustCompile(`^[0-9a-f]{16}$`)

func parseQuotaQuery(values url.Values) (quotaQuery, error) {
	q := quotaQuery{Provider: "all", View: "pool", Burn: 3, Future: "burn", Exclude: map[string]bool{}}
	for _, field := range []struct {
		key     string
		choices []string
	}{
		{"qp", []string{"all", "Claude", "Codex"}},
		{"qv", []string{"pool", "drain"}},
		{"qburn", []string{"1", "3", "6"}},
		{"qfuture", []string{"burn", "average"}},
		{"qpage", []string{"-4", "-3", "-2", "-1", "0"}},
	} {
		entries, present := values[field.key]
		if !present {
			continue
		}
		valid := false
		if len(entries) == 1 {
			for _, choice := range field.choices {
				if entries[0] == choice {
					valid = true
					break
				}
			}
		}
		if !valid {
			return quotaQuery{}, fmt.Errorf("usage archive: bad %s", field.key)
		}
		switch field.key {
		case "qp":
			q.Provider = entries[0]
		case "qv":
			q.View = entries[0]
		case "qburn":
			q.Burn, _ = strconv.Atoi(entries[0])
		case "qfuture":
			q.Future = entries[0]
		case "qpage":
			q.Page, _ = strconv.Atoi(entries[0])
		}
	}
	for _, index := range values["qx"] {
		if !quotaAuthIndex.MatchString(index) {
			return quotaQuery{}, fmt.Errorf("usage archive: bad qx")
		}
		q.Exclude[index] = true
	}
	return q, nil
}

func alias(label string) string {
	if !strings.Contains(label, "@") {
		return ""
	}
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(label))))
	return fmt.Sprintf("account %x", sum[:2])
}
