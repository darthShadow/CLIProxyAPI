package usagearchive

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestPriceFileCoversRoutedModels(t *testing.T) {
	models := append(registry.GetClaudeModels(), registry.GetCodexProModels()...)
	if len(models) != 33 {
		t.Fatalf("routed count=%d want=33", len(models))
	}
	for _, m := range models {
		p, ok := lookupPrice(m.ID)
		if !ok || p.NoPrice == "" && len(p.Tiers) == 0 {
			t.Errorf("missing routed model %s", m.ID)
		}
	}
}
func validPriceFixture() priceTable {
	return priceTable{Checked: "2026-10-25", Sources: map[string]string{"claude": "https://platform.claude.com/docs/en/about-claude/pricing", "codex": "https://developers.openai.com/api/docs/pricing"}, Models: map[string]priceEntry{"m": {Provider: "claude", Tiers: map[string]map[string]float64{"standard": {"input": 4, "cache_read": 0.2, "cache_write": 5, "cache_write_1h": 8, "output": 20}}}}}
}
func TestValidatePricesRejectsIncompleteEntries(t *testing.T) {
	for _, name := range []string{"missing output", "negative input", "zero cache read", "missing long write", "long without threshold", "no price with rates", "empty reason", "missing date", "missing source"} {
		t.Run(name, func(t *testing.T) {
			p := validPriceFixture()
			m := p.Models["m"]
			switch name {
			case "missing output":
				delete(m.Tiers["standard"], "output")
			case "negative input":
				m.Tiers["standard"]["input"] = -1
			case "zero cache read":
				m.Tiers["standard"]["cache_read"] = 0
			case "missing long write":
				delete(m.Tiers["standard"], "cache_write_1h")
			case "long without threshold":
				m.Tiers["standard_long"] = m.Tiers["standard"]
			case "no price with rates":
				m.NoPrice = "reason"
			case "empty reason":
				m.Tiers = nil
			case "missing date":
				p.Checked = ""
			case "missing source":
				delete(p.Sources, "codex")
			}
			p.Models["m"] = m
			if e := validatePrices(p); e == nil {
				t.Errorf("accepted %s", name)
			}
		})
	}
	if len(prices.Models) > 0 {
		if e := validatePrices(prices); e != nil {
			t.Error(e)
		}
	}
}
func priced(t *testing.T, l line) Cost {
	t.Helper()
	c, ok := lineCost(l)
	if !ok {
		t.Fatalf("control is unpriced: %+v", l)
	}
	return c
}
func TestLineCostUsesTierBandAndTTL(t *testing.T) {
	l := fixtureLine(0, "claude", "a", "claude-opus-5-5", "")
	standard := priced(t, l)
	closeFloat(t, standard.Total, (10*4+30*0.2+15*5+25*8+20*20)/1e6)
	l.Tier = "fast"
	fast := priced(t, l)
	for _, pair := range [][2]float64{{fast.Input, 2 * standard.Input}, {fast.Output, 2 * standard.Output}, {fast.CacheRead, 2 * standard.CacheRead}, {fast.CacheWrite, 2 * standard.CacheWrite}, {fast.Total, 2 * standard.Total}, {fast.Premium, standard.Total}, {fast.Savings, 30 * (8 - 0.4) / 1e6}} {
		closeFloat(t, pair[0], pair[1])
	}
	l.Tier = "priority"
	closeFloat(t, priced(t, l).Total, fast.Total)
	for _, tc := range []struct {
		model string
		input int64
		want  float64
	}{{"claude-haiku-5-5", 100000, 0.01}, {"claude-haiku-5-5", 100001, 0.0500005}, {"gpt-6.1-sol", 272000, 0.544}, {"gpt-6.1-sol", 272001, 1.088004}} {
		l = line{Model: tc.model, Input: tc.input}
		closeFloat(t, priced(t, l).Input, tc.want)
	}
	l = line{Model: "gpt-6.1-sol", CacheWrite: 100}
	closeFloat(t, priced(t, l).CacheWrite, 100*2.5/1e6)
	l = line{Model: "claude-opus-5-5", CacheWrite: 100, CacheWrite1h: 100}
	closeFloat(t, priced(t, l).CacheWrite, 100*8.0/1e6)
	if _, ok := lookupPrice("CLAUDE-OPUS-4-5-20251101"); !ok {
		t.Error("dated case-insensitive model lookup missing")
	}
}
func TestLineCostUnpricedNeverZero(t *testing.T) {
	l := fixtureLine(0, "claude", "a", "claude-opus-5-5", "")
	if priced(t, l).Total <= 0 {
		t.Fatal("priced control is zero")
	}
	for _, tc := range []struct {
		model, tier, quality string
		write                int64
	}{{"unknown", "", "", 0}, {"claude-3-7-sonnet", "", "", 0}, {"claude-opus-5-5", "ultrafast", "", 0}, {"gpt-5.5", "", "", 1}, {"claude-opus-5-5", "", "inconsistent", 0}} {
		l = line{Model: tc.model, Tier: tc.tier, Quality: tc.quality, Input: 10, CacheWrite: tc.write}
		if _, ok := lineCost(l); ok {
			t.Errorf("unpriced line accepted: %+v", l)
		}
	}
}
func TestLineCostUnpricesInvalidTTLSplit(t *testing.T) {
	for _, split := range [][2]int64{{0, 150}, {-1, 0}, {0, -1}} {
		l := line{Model: "claude-opus-5-5", CacheWrite: 100, CacheWrite5m: split[0], CacheWrite1h: split[1]}
		c, ok := lineCost(l)
		if ok {
			t.Errorf("invalid split %v priced at %.8f", split, c.Total)
		}
	}
	for _, tc := range []struct {
		five, hour int64
		want       float64
	}{{0, 0, 100 * 5.0 / 1e6}, {40, 60, (40*5.0 + 60*8) / 1e6}} {
		l := line{Model: "claude-opus-5-5", CacheWrite: 100, CacheWrite5m: tc.five, CacheWrite1h: tc.hour}
		closeFloat(t, priced(t, l).Total, tc.want)
	}
}
