package usagearchive

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"
)

// Cost holds USD estimates by token class, savings, and fast premium.
type Cost struct{ Input, CacheRead, CacheWrite, Output, Total, Savings, Premium float64 }
type priceEntry struct {
	Provider         string                        `json:"provider"`
	LongContextAbove int64                         `json:"long_context_above,omitempty"`
	NoPrice          string                        `json:"no_price,omitempty"`
	Tiers            map[string]map[string]float64 `json:"tiers,omitempty"`
}
type priceTable struct {
	Checked string                `json:"checked"`
	Sources map[string]string     `json:"sources"`
	Models  map[string]priceEntry `json:"models"`
}

//go:embed prices.json
var priceData []byte
var prices = loadPrices()

func loadPrices() priceTable {
	var p priceTable
	if json.Unmarshal(priceData, &p) != nil || validatePrices(p) != nil {
		return priceTable{}
	}
	return p
}
func validatePrices(p priceTable) error {
	if _, errParse := time.Parse("2006-01-02", p.Checked); errParse != nil {
		return fmt.Errorf("missing checked date")
	}
	for _, provider := range []string{"claude", "codex"} {
		if !strings.HasPrefix(p.Sources[provider], "https://") {
			return fmt.Errorf("missing %s source URL", provider)
		}
	}
	if len(p.Models) == 0 {
		return fmt.Errorf("empty price table")
	}
	for model, e := range p.Models {
		if e.Provider != "claude" && e.Provider != "codex" {
			return fmt.Errorf("%s: unknown provider", model)
		}
		if e.NoPrice != "" {
			if strings.TrimSpace(e.NoPrice) == "" || len(e.Tiers) > 0 {
				return fmt.Errorf("%s: invalid no_price", model)
			}
			continue
		}
		if len(e.Tiers["standard"]) == 0 {
			return fmt.Errorf("%s: missing standard rates", model)
		}
		for tier, rates := range e.Tiers {
			if strings.HasSuffix(tier, "_long") && e.LongContextAbove <= 0 {
				return fmt.Errorf("%s: missing long context threshold", model)
			}
			required := []string{"input", "cache_read", "output"}
			if e.Provider == "claude" {
				required = append(required, "cache_write", "cache_write_1h")
			}
			for _, key := range required {
				if rates[key] <= 0 {
					return fmt.Errorf("%s %s: missing positive %s", model, tier, key)
				}
			}
			for _, v := range rates {
				if v <= 0 || math.IsNaN(v) || math.IsInf(v, 0) {
					return fmt.Errorf("%s: invalid rate", model)
				}
			}
		}
	}
	return nil
}

var datedModel = regexp.MustCompile(`-\d{8}$`)

func lookupPrice(model string) (priceEntry, bool) {
	model = strings.ToLower(model)
	e, ok := prices.Models[model]
	if !ok {
		e, ok = prices.Models[datedModel.ReplaceAllString(model, "")]
	}
	return e, ok
}
func priceTier(tier string) string {
	switch tier {
	case "", "standard", "default", "auto":
		return "standard"
	case "fast", "priority":
		return "fast"
	}
	return "no price"
}
func lineCost(l line) (Cost, bool) {
	if l.CacheWrite5m < 0 || l.CacheWrite1h < 0 || l.CacheWrite5m+l.CacheWrite1h > l.CacheWrite {
		return Cost{}, false
	}
	e, ok := lookupPrice(l.Model)
	if !ok || e.NoPrice != "" || l.Quality == "inconsistent" {
		return Cost{}, false
	}
	tier := priceTier(l.Tier)
	band := ""
	if e.LongContextAbove > 0 && l.Input+l.CacheRead+l.CacheWrite > e.LongContextAbove {
		band = "_long"
	}
	r, ok := e.Tiers[tier+band]
	if !ok {
		return Cost{}, false
	}
	tokens := map[string]int64{"input": l.Input, "cache_read": l.CacheRead, "cache_write": l.CacheWrite - l.CacheWrite1h, "cache_write_1h": l.CacheWrite1h, "output": l.Output}
	for key, n := range tokens {
		if n != 0 && r[key] <= 0 {
			return Cost{}, false
		}
	}
	c := costAt(tokens, r)
	c.Savings = float64(l.CacheRead) * (r["input"] - r["cache_read"]) / 1e6
	if tier == "fast" {
		c.Premium = c.Total - costAt(tokens, e.Tiers["standard"+band]).Total
	}
	return c, true
}
func costAt(n map[string]int64, r map[string]float64) Cost {
	c := Cost{Input: float64(n["input"]) * r["input"] / 1e6, CacheRead: float64(n["cache_read"]) * r["cache_read"] / 1e6, CacheWrite: (float64(n["cache_write"])*r["cache_write"] + float64(n["cache_write_1h"])*r["cache_write_1h"]) / 1e6, Output: float64(n["output"]) * r["output"] / 1e6}
	c.Total = c.Input + c.CacheRead + c.CacheWrite + c.Output
	return c
}

// PricesChecked returns the checked date from the validated embedded price file.
// Invalid price metadata returns the zero time, displayed as Not Reported.
func PricesChecked() time.Time {
	checked, _ := time.Parse("2006-01-02", prices.Checked)
	return checked
}
