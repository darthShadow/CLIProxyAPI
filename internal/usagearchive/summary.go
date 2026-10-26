package usagearchive

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	log "github.com/sirupsen/logrus"
)

// Group contains additive token and cost facts for one view row.
type Group struct {
	Day, Provider, Account, Model, Tier, Client string
	Requests, Sessions, WithoutSession          int64
	Input, Output, CacheRead, CacheWrite        int64
	CacheWrite5m, CacheWrite1h, NoTTLSplit      int64
	Unverified, Processed, Unpriced             int64
	TokenShare, CostShare                       float64
	Partial                                     bool
	Cost                                        Cost
}

// QuotaSample contains weekly used percent and reset at the header observation time.
type QuotaSample struct {
	At, Reset time.Time
	Used      float64
}

// Summary contains the 30 local days and their grouped usage facts.
type Summary struct {
	Quota                                                     map[string][]QuotaSample
	Start, End, Now                                           time.Time
	Unreadable                                                int64
	Groups, Daily, Providers, Days, Accounts, Tiers, AllTiers []Group
	Models, ModelClients, ModelDays                           []Group
	All                                                       Group
}

// Summarize reads complete JSONL lines for today and the preceding 29 local days.
// Missing files are empty; other read failures return no partial summary.
func Summarize(dir string, now time.Time) (Summary, error) {
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	s := Summary{Start: end.AddDate(0, 0, -29), End: end, Now: now}
	groups, daily, providers, days := groupSet{}, groupSet{}, groupSet{}, groupSet{}
	all := &accumulator{}
	accounts, tiers, allTiers := groupSet{}, groupSet{}, groupSet{}
	models, modelClients, modelDays := groupSet{}, groupSet{}, groupSet{}
	quotaBins := make(map[string]map[int64]QuotaSample)
	for day := s.Start; !day.After(s.End); day = day.AddDate(0, 0, 1) {
		path := filepath.Join(dir, "usage-"+day.Format("2006-01-02")+".jsonl")
		f, errOpen := os.Open(path)
		if errors.Is(errOpen, fs.ErrNotExist) {
			continue
		}
		if errOpen != nil {
			return Summary{}, fmt.Errorf("%s: %w", path, errOpen)
		}
		reader := bufio.NewReader(f)
		for {
			b, errRead := reader.ReadBytes('\n')
			if errRead == io.EOF {
				break
			}
			if errRead != nil {
				if errClose := f.Close(); errClose != nil {
					log.Warnf("usage archive: close %s: %v", path, errClose)
				}
				return Summary{}, fmt.Errorf("%s: %w", path, errRead)
			}
			if len(bytes.TrimSpace(b)) == 0 {
				continue
			}
			var l line
			if errUnmarshal := json.Unmarshal(b, &l); errUnmarshal != nil {
				s.Unreadable++
				continue
			}
			ts, errParse := time.Parse(time.RFC3339, l.TS)
			if errParse != nil {
				s.Unreadable++
				continue
			}
			date := ts.In(now.Location()).Format("2006-01-02")
			if date < s.Start.Format("2006-01-02") || date > s.End.Format("2006-01-02") {
				continue
			}
			if l.WeeklyUsed != nil && l.WeeklyReset != 0 && l.WeeklyAt != 0 {
				sample := QuotaSample{At: time.Unix(l.WeeklyAt, 0), Reset: time.Unix(l.WeeklyReset, 0), Used: *l.WeeklyUsed}
				bins := quotaBins[l.AuthIndex]
				if bins == nil {
					bins = make(map[int64]QuotaSample)
					quotaBins[l.AuthIndex] = bins
				}
				bin := sample.At.Truncate(10 * time.Minute).Unix()
				if previous, exists := bins[bin]; !exists || !sample.At.Before(previous.At) {
					bins[bin] = sample
				}
			}
			account := l.Account
			if account == "" {
				account = "credential " + l.AuthIndex
			}
			cost, priced := lineCost(l)
			groups.add(groupKey{Provider: l.Provider, Account: account, Model: l.Model}, l, cost, priced)
			daily.add(groupKey{Day: date, Provider: l.Provider, Account: account, Model: l.Model}, l, cost, priced)
			providers.add(groupKey{Provider: l.Provider}, l, cost, priced)
			days.add(groupKey{Day: date, Provider: l.Provider}, l, cost, priced)
			tier := priceTier(l.Tier)
			if !priced {
				tier = "no price"
			}
			accounts.add(groupKey{Provider: l.Provider, Account: account}, l, cost, priced)
			tiers.add(groupKey{Provider: l.Provider, Tier: tier}, l, cost, priced)
			allTiers.add(groupKey{Tier: tier}, l, cost, priced)
			models.add(groupKey{Provider: l.Provider, Model: l.Model}, l, cost, priced)
			modelClients.add(groupKey{Provider: l.Provider, Model: l.Model, Client: l.Client}, l, cost, priced)
			modelDays.add(groupKey{Day: date, Provider: l.Provider, Model: l.Model}, l, cost, priced)
			all.add(l, cost, priced)
		}
		if errClose := f.Close(); errClose != nil {
			log.Warnf("usage archive: close %s: %v", path, errClose)
			return Summary{}, fmt.Errorf("%s: %w", path, errClose)
		}
	}
	s.Quota = make(map[string][]QuotaSample, len(quotaBins))
	for authIndex, bins := range quotaBins {
		samples := make([]QuotaSample, 0, len(bins))
		for _, sample := range bins {
			samples = append(samples, sample)
		}
		sort.Slice(samples, func(i, j int) bool { return samples[i].At.Before(samples[j].At) })
		s.Quota[authIndex] = samples
	}
	s.All = all.Group
	if s.All.Processed > 0 {
		s.All.TokenShare = 100
	}
	if s.All.Cost.Total > 0 {
		s.All.CostShare = 100
	}
	s.Groups = groups.rows(s.All)
	s.Daily = daily.rows(s.All)
	s.Providers = providers.rows(s.All)
	s.Days = days.rows(s.All)
	s.Accounts = accounts.rows(s.All)
	s.Tiers = tiers.rows(s.All)
	s.AllTiers = allTiers.rows(s.All)
	s.Models = models.rows(s.All)
	s.ModelClients = modelClients.rows(s.All)
	s.ModelDays = modelDays.rows(s.All)
	for i := range s.ModelDays {
		s.ModelDays[i].Partial = s.ModelDays[i].Day == end.Format("2006-01-02")
	}
	for i := range s.Days {
		s.Days[i].Partial = s.Days[i].Day == end.Format("2006-01-02")
	}
	for i := range s.Daily {
		s.Daily[i].Partial = s.Daily[i].Day == end.Format("2006-01-02")
	}
	return s, nil
}

type groupKey struct{ Day, Provider, Account, Model, Tier, Client string }
type accumulator struct {
	Group
	sessions map[string]struct{}
}
type groupSet map[groupKey]*accumulator

func (gs groupSet) add(k groupKey, l line, cost Cost, priced bool) {
	a := gs[k]
	if a == nil {
		a = &accumulator{Group: Group{Day: k.Day, Provider: k.Provider, Account: k.Account, Model: k.Model, Tier: k.Tier, Client: k.Client}}
		gs[k] = a
	}
	a.add(l, cost, priced)
}
func (a *accumulator) add(l line, c Cost, priced bool) {
	if !priced {
		a.Unpriced++
	} else {
		a.Cost.Input += c.Input
		a.Cost.CacheRead += c.CacheRead
		a.Cost.CacheWrite += c.CacheWrite
		a.Cost.Output += c.Output
		a.Cost.Total += c.Total
		a.Cost.Savings += c.Savings
		a.Cost.Premium += c.Premium
	}
	a.Requests++
	a.Input += l.Input
	a.Output += l.Output
	a.CacheRead += l.CacheRead
	a.CacheWrite += l.CacheWrite
	a.CacheWrite5m += l.CacheWrite5m
	a.CacheWrite1h += l.CacheWrite1h
	a.Processed += l.Input + l.CacheRead + l.CacheWrite + l.Output
	valid := l.CacheWrite5m >= 0 && l.CacheWrite1h >= 0 && l.CacheWrite5m+l.CacheWrite1h <= l.CacheWrite
	if !valid || l.Quality == "inconsistent" {
		a.Unverified++
	}
	if valid {
		a.NoTTLSplit += l.CacheWrite - l.CacheWrite5m - l.CacheWrite1h
	}
	if l.Session == "" {
		a.WithoutSession++
	} else {
		if a.sessions == nil {
			a.sessions = map[string]struct{}{}
		}
		a.sessions[l.Provider+":"+l.Session] = struct{}{}
		a.Sessions = int64(len(a.sessions))
	}
}
func (gs groupSet) rows(all Group) []Group {
	rows := make([]Group, 0, len(gs))
	for _, a := range gs {
		g := a.Group
		if all.Cost.Total > 0 {
			g.CostShare = g.Cost.Total / all.Cost.Total * 100
		}
		if all.Processed != 0 {
			g.TokenShare = float64(g.Processed) / float64(all.Processed) * 100
		}
		rows = append(rows, g)
	}
	sort.Slice(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.Day != b.Day {
			return a.Day > b.Day
		}
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Account != b.Account {
			return a.Account < b.Account
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		if a.Tier != b.Tier {
			return a.Tier < b.Tier
		}
		return a.Client < b.Client
	})
	return rows
}
