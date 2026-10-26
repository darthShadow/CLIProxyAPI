package management

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type quotaAccount struct {
	Provider, Account, Window, State string
	Used                             float64
	Reset, Observed                  time.Time
	Weight                           int64
	Index                            string
	Counted                          bool
	RunsOut                          time.Time
	Lasts                            bool
}
type quotaPool struct {
	Provider, Window       string
	Accounts, Fresh, Stale int
	Used, Total            float64
	Gain                   float64
	Reset, Observed        time.Time
	Segments               []quotaSegment
}
type quotaSegment struct {
	Account, State, Severity string
	Used                     float64
	Weight                   int64
}
type quotaGroup struct {
	Disabled []string
	Provider string
	Accounts int
	Pools    []quotaPool
}
type quotaView struct {
	Choices  []quotaAccount
	Counted  int
	Counts   map[string]int
	Groups   []quotaGroup
	Pools    []quotaPool
	Accounts []quotaAccount
}

func usageInventory(auths []*coreauth.Auth) []*coreauth.Auth {
	var rows []*coreauth.Auth
	for _, a := range auths {
		if a != nil && (a.Provider == "claude" || a.Provider == "codex") && a.AuthKind() == "oauth" {
			rows = append(rows, a)
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if usageAccount(a) != usageAccount(b) {
			return usageAccount(a) < usageAccount(b)
		}
		return a.Index < b.Index
	})
	return rows
}
func usageAuths(auths []*coreauth.Auth) []*coreauth.Auth {
	var enabled []*coreauth.Auth
	for _, a := range usageInventory(auths) {
		if !a.Disabled && a.Status != coreauth.StatusDisabled {
			enabled = append(enabled, a)
		}
	}
	return enabled
}
func usageAccount(a *coreauth.Auth) string {
	if email := authEmail(a); email != "" {
		return email
	}
	return "credential " + a.Index
}
func poolQuota(auths []*coreauth.Auth, now time.Time, exclude map[string]bool) quotaView {
	inventory := usageInventory(auths)
	auths = usageAuths(inventory)
	view := quotaView{Counts: map[string]int{"all": len(inventory)}}
	for _, a := range auths {
		weight := coreauth.AuthWeight(a)
		counted := weight > 0 && !exclude[a.Index]
		view.Choices = append(view.Choices, quotaAccount{Provider: a.Provider, Account: usageAccount(a), Index: a.Index, Weight: weight, Counted: counted})
		if counted {
			view.Counted++
		}
	}
	windows := map[string]map[string]map[string]quotaAccount{}
	for _, a := range auths {
		if windows[a.Provider] == nil {
			windows[a.Provider] = map[string]map[string]quotaAccount{}
		}
		keys := map[string]string{}
		for key := range a.Quota.Signals {
			keys[strings.ToLower(key)] = key
		}
		for lower, key := range keys {
			var name, prefix string
			var used float64
			var ok bool
			var reset time.Time
			if a.Provider == "claude" && strings.HasPrefix(lower, "anthropic-ratelimit-unified-") && strings.HasSuffix(lower, "-utilization") {
				prefix = strings.TrimSuffix(lower, "-utilization")
				name = strings.TrimPrefix(prefix, "anthropic-ratelimit-unified-")
				used, ok = a.Quota.SignalPercent(key)
				used *= 100
				reset = a.Quota.SignalUnixTime(keys[prefix+"-reset"])
				switch name {
				case "5h":
					name = "5-Hour"
				case "7d":
					name = "7-Day"
				}
			} else if a.Provider == "codex" && strings.HasPrefix(lower, "x-codex-") && strings.HasSuffix(lower, "-used-percent") {
				prefix = strings.TrimSuffix(lower, "-used-percent")
				used, ok = a.Quota.SignalPercent(key)
				minutes, err := strconv.Atoi(a.Quota.Signals[keys[prefix+"-window-minutes"]])
				if err != nil {
					continue
				}
				name = fmt.Sprintf("%d-Minute", minutes)
				switch minutes {
				case 300:
					name = "5-Hour"
				case 10080:
					name = "Weekly"
				}
				part := strings.TrimPrefix(prefix, "x-codex-")
				if part != "primary" && part != "secondary" {
					name = part + " " + name
				}
				reset = a.Quota.SignalUnixTime(keys[prefix+"-reset-at"])
				if reset.IsZero() {
					reset = a.Quota.SignalResetAfter(keys[prefix+"-reset-after-seconds"])
				}
			} else {
				continue
			}
			if !ok || math.IsNaN(used) || math.IsInf(used, 0) {
				continue
			}
			row := quotaAccount{Weight: coreauth.AuthWeight(a), Provider: a.Provider, Account: usageAccount(a), Window: name, State: "fresh", Used: used, Reset: reset.In(now.Location()), Observed: a.Quota.ObservedAt.In(now.Location())}
			if !reset.IsZero() && !reset.After(now) {
				row.State = "stale"
				row.Used = 0
			}
			if row.State == "fresh" && (name == "7-Day" || name == "Weekly") && !reset.IsZero() {
				row.RunsOut, row.Lasts = runsOut(used, row.Reset, row.Observed)
			}
			if windows[a.Provider][name] == nil {
				windows[a.Provider][name] = map[string]quotaAccount{}
			}
			windows[a.Provider][name][a.ID] = row
		}
	}
	for provider, names := range windows {
		for name, accounts := range names {
			pool := quotaPool{Provider: provider, Window: name}
			for _, a := range auths {
				if a.Provider != provider {
					continue
				}
				row, ok := accounts[a.ID]
				if !ok {
					row = quotaAccount{Weight: coreauth.AuthWeight(a), Provider: provider, Account: usageAccount(a), Window: name, State: "absent"}
				}
				view.Accounts = append(view.Accounts, row)
				if exclude[a.Index] {
					continue
				}
				segment := quotaSegment{Account: row.Account, State: row.State, Used: row.Used, Weight: row.Weight}
				if row.Weight == 0 {
					pool.Segments = append(pool.Segments, segment)
					continue
				}
				pool.Accounts++
				if row.State == "stale" {
					pool.Stale++
				}
				if row.State == "fresh" {
					pool.Fresh++
					pool.Used += float64(row.Weight) * row.Used
					pool.Total += float64(row.Weight) * 100
					if !row.Reset.IsZero() && (pool.Reset.IsZero() || row.Reset.Before(pool.Reset)) {
						pool.Reset = row.Reset
					}
					if pool.Observed.IsZero() || row.Observed.Before(pool.Observed) {
						pool.Observed = row.Observed
					}
					if row.Used >= 90 {
						segment.Severity = "bad"
					} else if row.Used >= 75 {
						segment.Severity = "warn"
					}
				}
				pool.Segments = append(pool.Segments, segment)
			}
			for _, a := range auths {
				if a.Provider != provider || coreauth.AuthWeight(a) == 0 || exclude[a.Index] {
					continue
				}
				row, ok := accounts[a.ID]
				if ok && row.State == "fresh" && !row.Reset.IsZero() && row.Reset.Truncate(time.Minute).Equal(pool.Reset.Truncate(time.Minute)) {
					pool.Gain += float64(row.Weight) * row.Used
				}
			}
			view.Pools = append(view.Pools, pool)
		}
		if len(names) == 0 {
			for _, a := range auths {
				if a.Provider == provider {
					view.Accounts = append(view.Accounts, quotaAccount{Provider: provider, Account: usageAccount(a), State: "absent"})
				}
			}
		}
	}
	sort.Slice(view.Pools, func(i, j int) bool {
		a, b := view.Pools[i], view.Pools[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		return a.Window < b.Window
	})
	sort.Slice(view.Accounts, func(i, j int) bool {
		a, b := view.Accounts[i], view.Accounts[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Account != b.Account {
			return a.Account < b.Account
		}
		return a.Window < b.Window
	})
	for _, a := range inventory {
		if len(view.Groups) == 0 || view.Groups[len(view.Groups)-1].Provider != a.Provider {
			view.Groups = append(view.Groups, quotaGroup{Provider: a.Provider})
		}
		view.Counts[providerLabel(a.Provider)]++
		view.Groups[len(view.Groups)-1].Accounts++
		if a.Disabled || a.Status == coreauth.StatusDisabled {
			view.Groups[len(view.Groups)-1].Disabled = append(view.Groups[len(view.Groups)-1].Disabled, usageAccount(a))
		}
	}
	for i := range view.Groups {
		for _, p := range view.Pools {
			if p.Provider == view.Groups[i].Provider {
				view.Groups[i].Pools = append(view.Groups[i].Pools, p)
			}
		}
	}
	return view
}

func runsOut(used float64, reset, observed time.Time) (at time.Time, lasts bool) {
	const window = 168 * time.Hour
	remaining := reset.Sub(observed)
	elapsed := window - remaining
	if observed.IsZero() || remaining <= 0 || remaining > window {
		return time.Time{}, false
	}
	if used >= 100 {
		return observed, false
	}
	if elapsed <= 0 {
		return time.Time{}, false
	}
	if used <= 0 {
		return time.Time{}, true
	}
	duration := float64(elapsed) * (100 - used) / used
	if duration >= float64(remaining) {
		return time.Time{}, true
	}
	return observed.Add(time.Duration(duration)), false
}
