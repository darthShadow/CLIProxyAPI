package management

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagearchive"
)

func inlineScriptError(s string, shell bool) error {
	lower := strings.ToLower(s)
	if regexp.MustCompile(`\son[a-z]+\s*=`).MatchString(lower) || strings.Contains(lower, "javascript:") {
		return fmt.Errorf("inline event or javascript URL")
	}
	tags := regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script\s*>`).FindAllString(lower, -1)
	if !shell {
		if len(tags) > 0 || strings.Contains(lower, "<script") {
			return fmt.Errorf("fragment script")
		}
		return nil
	}
	if len(tags) != 1 || tags[0] != `<script src="/usage-archive.js"></script>` {
		return fmt.Errorf("expected exactly one external script")
	}
	head, script, style, end := strings.Index(lower, "<head>"), strings.Index(lower, tags[0]), strings.Index(lower, "<style>"), strings.Index(lower, "</head>")
	if !(head >= 0 && head < script && script < style && style < end) {
		return fmt.Errorf("script must precede style inside head")
	}
	return nil
}
func TestUsageArchiveAssetsHaveNoInlineScript(t *testing.T) {
	if inlineScriptError(`<script>document.documentElement.classList.add("dark")</script>`, true) == nil {
		t.Fatal("known-bad inline script accepted")
	}
	for _, name := range []string{"usage-archive.html", "fragment.html", "accounts.html"} {
		b, e := usageAssets.ReadFile("usage_archive/" + name)
		if e != nil {
			t.Errorf("read %s: %v", name, e)
			continue
		}
		if e := inlineScriptError(string(b), name == "usage-archive.html"); e != nil {
			t.Errorf("%s: %v", name, e)
		}
	}
}

var cssClass = regexp.MustCompile(`\.(?:\\[0-9a-fA-F]{1,6}\s?|\\[^\r\n]|[a-zA-Z0-9_-])+`)
var cssEscape = regexp.MustCompile(`\\([0-9a-fA-F]{1,6})\s?|\\(.)`)

func decodedSelector(s string) string {
	return cssEscape.ReplaceAllStringFunc(s, func(v string) string {
		m := cssEscape.FindStringSubmatch(v)
		if m[1] != "" {
			n, _ := strconv.ParseInt(m[1], 16, 32)
			return string(rune(n))
		}
		return m[2]
	})
}
func missingClasses(css string, templates ...string) []string {
	selectors := map[string]bool{}
	for _, sel := range cssClass.FindAllString(css, -1) {
		selectors[decodedSelector(sel[1:])] = true
	}
	missing := map[string]bool{}
	for _, s := range templates {
		s = regexp.MustCompile(`(?s){{.*?}}`).ReplaceAllString(s, "")
		for _, match := range regexp.MustCompile(`\bclass="([^"]*)"`).FindAllStringSubmatch(s, -1) {
			for _, token := range strings.Fields(match[1]) {
				if !selectors[token] {
					missing[token] = true
				}
			}
		}
	}
	var rows []string
	for c := range missing {
		rows = append(rows, c)
	}
	sort.Strings(rows)
	return rows
}
func TestUsageArchiveCSSCoversTemplateClasses(t *testing.T) {
	var templates []string
	for _, name := range []string{"usage-archive.html", "fragment.html", "accounts.html"} {
		b, e := usageAssets.ReadFile("usage_archive/" + name)
		if e != nil {
			t.Fatalf("read %s: %v", name, e)
		}
		templates = append(templates, string(b))
	}
	shell := templates[0]
	start, end := strings.Index(shell, "/* tailwind:begin */"), strings.Index(shell, "/* tailwind:end */")
	if start < 0 || end <= start {
		t.Fatal("missing CSS markers")
	}
	css := shell[start:end]
	if !strings.Contains(css, "tailwindcss v4.3.2") {
		t.Error("missing pinned Tailwind banner")
	}
	if missing := missingClasses(css, templates...); len(missing) > 0 {
		t.Errorf("classes missing from compiled CSS: %v", missing)
	}
	// Go builds the type-bar classes, so the template scan cannot see them.
	var bars strings.Builder
	for _, b := range typeBars(usagearchive.Group{}, "cost") {
		fmt.Fprintf(&bars, `<span class="%s"></span>`, b.Class)
	}
	if missing := missingClasses(css, bars.String()); len(missing) > 0 {
		t.Errorf("type-bar classes missing from compiled CSS: %v", missing)
	}
	bad := append(append([]string{}, templates...), `<div class="bogus-q"></div>`)
	found := false
	for _, c := range missingClasses(css, bad...) {
		if c == "bogus-q" {
			found = true
		}
	}
	if !found {
		t.Error("known-bad bogus-q class accepted")
	}
	if got := decodedSelector(`\32 xl\:grid-cols-4`); got != "2xl:grid-cols-4" {
		t.Errorf("CSS escape decode=%q", got)
	}
}
func TestUsageArchiveAssetsExcludeReveal(t *testing.T) {
	for _, name := range []string{"usage-archive.html", "usage-archive.js"} {
		b, e := usageAssets.ReadFile("usage_archive/" + name)
		if e != nil {
			t.Fatal(e)
		}
		for _, cut := range []string{"reveal", "@keyframes", "animation-delay"} {
			if strings.Contains(string(b), cut) {
				t.Errorf("cut change 8 remains in %s: %s", name, cut)
			}
		}
	}
}
