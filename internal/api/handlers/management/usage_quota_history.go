package management

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/usagearchive"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type quotaHistoryAccount struct {
	Account string
	Weight  int64
	Samples []usagearchive.QuotaSample
	Now     usagearchive.QuotaSample
	Known   bool
}
type quotaPoint struct {
	At    time.Time
	Value float64
}

// quotaState carries the last observation forward and clears use after its reset.
func quotaState(samples []usagearchive.QuotaSample, at time.Time) (usagearchive.QuotaSample, bool) {
	var state usagearchive.QuotaSample
	known := false
	for _, sample := range samples {
		if !sample.At.After(at) && (!known || !sample.At.Before(state.At)) {
			state, known = sample, true
		}
	}
	if known && !state.Reset.IsZero() && !state.Reset.After(at) {
		state.Used = 0
	}
	return state, known
}

// quotaBurn sums the archived rises after the state at from, up to to, over
// the window. The accounts are also used outside this proxy, so a rise that
// starts before from counts only its share inside the window; a rise across a
// reset starts at that reset.
func quotaBurn(samples []usagearchive.QuotaSample, from, to time.Time) (float64, bool) {
	previous, known := quotaState(samples, from)
	count, rise := 0, 0.0
	if known {
		count = 1
	}
	for _, sample := range samples {
		if !sample.At.After(from) || sample.At.After(to) {
			continue
		}
		if count > 0 {
			step, start := math.Max(0, sample.Used-previous.Used), previous.At
			if math.Abs(sample.Reset.Sub(previous.Reset).Seconds()) > 60 {
				step = sample.Used
				if !previous.Reset.IsZero() {
					start = previous.Reset
				}
			}
			if start.Before(from) {
				step *= sample.At.Sub(from).Seconds() / sample.At.Sub(start).Seconds()
			}
			rise += step
		}
		count++
		previous = sample
	}
	if count < 2 || !to.After(from) {
		return 0, false
	}
	return rise / to.Sub(from).Hours(), true
}

func quotaDrain(accounts []quotaHistoryAccount, from, to time.Time, window int) []quotaPoint {
	var points []quotaPoint
	for at := from; !at.After(to); at = at.Add(10 * time.Minute) {
		value, observed := 0.0, false
		for _, a := range accounts {
			if rate, ok := quotaBurn(a.Samples, at.Add(-time.Duration(window)*time.Hour), at); ok {
				value += float64(a.Weight) * rate
				observed = true
			}
		}
		if observed {
			points = append(points, quotaPoint{at, value})
		}
	}
	return points
}

func quotaPage(now time.Time, k int) (time.Time, time.Time) {
	from := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).AddDate(0, 0, -2+7*k)
	return from, from.AddDate(0, 0, 7)
}

// averageRate is the pooled use per hour since each account's window began.
func averageRate(accounts []quotaHistoryAccount) (float64, bool) {
	rate, available := 0.0, false
	for _, a := range accounts {
		elapsed := a.Now.At.Sub(a.Now.Reset.Add(-168 * time.Hour)).Hours()
		if !a.Known || a.Now.Reset.IsZero() || elapsed <= 0 || elapsed > 168 {
			return 0, false
		}
		rate += float64(a.Weight) * a.Now.Used / elapsed
		available = true
	}
	return rate, available
}

type quotaReset struct {
	At   time.Time
	Gain float64
}
type quotaProjection struct {
	Available      bool
	Missing        []string
	Empty, Horizon time.Time
	Points         []quotaPoint
	Resets         []quotaReset
}

// quotaProject shares the selected pool demand across non-empty accounts by weight.
// Resets split a bin so each refill has two points at the same time.
func quotaProject(accounts []quotaHistoryAccount, rate float64, available bool, now, horizon time.Time) quotaProjection {
	p := quotaProjection{Horizon: horizon}
	for _, a := range accounts {
		if !a.Known || a.Now.Reset.IsZero() {
			p.Missing = append(p.Missing, a.Account)
		}
	}
	if len(p.Missing) > 0 || !available || len(accounts) == 0 {
		return p
	}
	p.Available = true
	states := make([]usagearchive.QuotaSample, len(accounts))
	left := 0.0
	for i, a := range accounts {
		states[i] = a.Now
		if !states[i].Reset.After(now) {
			states[i].Used = 0
			n := now.Sub(states[i].Reset)/(168*time.Hour) + 1
			states[i].Reset = states[i].Reset.Add(n * 168 * time.Hour)
		}
		left += float64(a.Weight) * (100 - states[i].Used)
	}
	p.Points = append(p.Points, quotaPoint{now, left})
	if left == 0 {
		p.Empty = now
	}
	for at := now; at.Before(horizon); {
		next := now.Add((at.Sub(now)/(10*time.Minute) + 1) * 10 * time.Minute)
		if next.After(horizon) {
			next = horizon
		}
		weight := 0.0
		for i, a := range accounts {
			if states[i].Used < 100 {
				weight += float64(a.Weight)
			}
			if states[i].Reset.Before(next) {
				next = states[i].Reset
			}
		}
		left = 0
		for i, a := range accounts {
			if states[i].Used < 100 && weight > 0 {
				states[i].Used = math.Min(100, states[i].Used+rate*next.Sub(at).Hours()/weight)
				// Repeated fractional bin increments can leave a few ulps below the cap.
				if 100-states[i].Used < 1e-9 {
					states[i].Used = 100
				}
			}
			left += float64(a.Weight) * (100 - states[i].Used)
		}
		p.Points = append(p.Points, quotaPoint{next, left})
		if left == 0 && p.Empty.IsZero() {
			p.Empty = next
		}
		gain := 0.0
		reset := false
		for i, a := range accounts {
			if states[i].Reset.Equal(next) {
				gain += float64(a.Weight) * states[i].Used
				states[i].Used = 0
				states[i].Reset = states[i].Reset.Add(168 * time.Hour)
				reset = true
			}
		}
		if reset {
			p.Resets = append(p.Resets, quotaReset{next, gain})
			p.Points = append(p.Points, quotaPoint{next, left + gain})
		}
		at = next
	}
	return p
}

type quotaTooltip struct {
	Text string
	Left bool
}
type quotaChartLabel struct {
	X, Y, Text string
	At         time.Time
	Gain       float64
}
type quotaFigure struct {
	Provider, Kind, NowX, EmptyX string
	Resets                       []quotaChartLabel
	Recorded                     []string
	Projected                    string
	Gridlines                    []chartTick
	DateTicks                    []chartTick
	Columns                      []quotaTooltip
	Top                          float64
}
type quotaChartView struct {
	NoRecorded                     bool
	Provider                       string
	From, To                       time.Time
	BurnWindow                     int
	Top, Burn                      float64
	BurnAvailable                  bool
	Observed, NotObserved, LeftOut []string
	Recorded, Projected            []quotaPoint
	Pool, Drain                    quotaFigure
	Projection                     quotaProjection
	NowX, EmptyX, RateLabel        string
	Average                        bool
	Resets                         []quotaChartLabel
}

func quotaChart(auths []*coreauth.Auth, s usagearchive.Summary, q quotaQuery) []quotaChartView {
	if q.Burn == 0 {
		q.Burn = 3
	}
	if q.Future == "" {
		q.Future = "burn"
	}
	from, to := quotaPage(s.Now, q.Page)
	var charts []quotaChartView
	for _, provider := range []string{"claude", "codex"} {
		c := quotaChartView{Provider: provider, From: from, To: to, BurnWindow: q.Burn, NoRecorded: true}
		var accounts []quotaHistoryAccount
		present := false
		for _, a := range usageInventory(auths) {
			if a.Provider != provider {
				continue
			}
			present = true
			weight := coreauth.AuthWeight(a)
			if a.Disabled || a.Status == coreauth.StatusDisabled || weight <= 0 || q.Exclude[a.Index] {
				c.LeftOut = append(c.LeftOut, usageAccount(a))
				continue
			}
			state, known := quotaState(s.Quota[a.Index], s.Now)
			used, reset, _, live := coreauth.WeeklyQuota(a.Quota)
			if live && !a.Quota.ObservedAt.After(s.Now) && (!known || a.Quota.ObservedAt.After(state.At)) {
				state = usagearchive.QuotaSample{At: a.Quota.ObservedAt, Reset: reset, Used: used}
				known = true
				if !reset.After(s.Now) {
					state.Used = 0
				}
			}
			account := quotaHistoryAccount{Account: usageAccount(a), Weight: weight, Samples: s.Quota[a.Index], Now: state, Known: known}
			for _, sample := range account.Samples {
				if !sample.At.Before(from) && sample.At.Before(to) && !sample.At.After(s.Now) {
					c.NoRecorded = false
					break
				}
			}
			accounts = append(accounts, account)
			c.Top += float64(weight) * 100
			if burn, ok := quotaBurn(account.Samples, s.Now.Add(-time.Duration(q.Burn)*time.Hour), s.Now); ok {
				c.Burn += float64(weight) * burn
				c.BurnAvailable = true
				c.Observed = append(c.Observed, account.Account)
			} else {
				c.NotObserved = append(c.NotObserved, account.Account)
			}
		}
		if !present {
			continue
		}
		rate, available := c.Burn, c.BurnAvailable
		c.RateLabel = fmt.Sprintf("Same as last %dh", q.Burn)
		if q.Future == "average" {
			c.RateLabel, c.Average = "Window average", true
			rate, available = averageRate(accounts)
		}
		c.Projection = quotaProject(accounts, rate, available, s.Now, s.Now.Add(7*24*time.Hour))
		// History uses archive state only; the live snapshot contributes solely at Now.
		end := s.Now
		if to.Before(end) {
			end = to
		}
		for at := from; !at.After(end); {
			value, known := 0.0, len(accounts) > 0
			for _, a := range accounts {
				state, ok := quotaState(a.Samples, at)
				if at.Equal(s.Now) {
					state, ok = a.Now, a.Known
				}
				if !ok {
					known = false
					break
				}
				value += float64(a.Weight) * (100 - state.Used)
			}
			if known {
				c.Recorded = append(c.Recorded, quotaPoint{at, value})
			}
			if at.Equal(end) {
				break
			}
			at = at.Add(10 * time.Minute)
			if at.After(end) {
				at = end
			}
		}
		x := func(at time.Time) float64 { return at.Sub(from).Seconds() / to.Sub(from).Seconds() * 100 }
		// Space local-date ticks over days 0-6; midnight positions follow daylight-saving changes.
		dayTop, dayTicks := chartTicks(6, "req")
		for i := range dayTicks {
			at := from.AddDate(0, 0, int(float64(i)*dayTop/4))
			if at.Before(to) {
				tick := chartTick{Position: fmt.Sprintf("%.3f", x(at)), Label: at.Format("Jan 02")}
				c.Pool.DateTicks = append(c.Pool.DateTicks, tick)
				c.Drain.DateTicks = append(c.Drain.DateTicks, tick)
			}
		}
		if q.Page == 0 && !s.Now.Before(from) && !s.Now.After(to) {
			c.NowX = fmt.Sprintf("%.3f", x(s.Now))
			if c.Projection.Available {
				for _, point := range c.Projection.Points {
					if !point.At.After(to) {
						c.Projected = append(c.Projected, point)
					}
				}
				if !c.Projection.Empty.IsZero() && !c.Projection.Empty.After(to) {
					c.EmptyX = fmt.Sprintf("%.3f", x(c.Projection.Empty))
				}
				for _, reset := range c.Projection.Resets {
					if reset.At.After(to) {
						continue
					}
					left := 0.0
					for _, point := range c.Projected {
						if point.At.Equal(reset.At) {
							left = point.Value
						}
					}
					c.Resets = append(c.Resets, quotaChartLabel{X: fmt.Sprintf("%.3f", x(reset.At)), Y: fmt.Sprintf("%.3f", left/c.Top*100), Text: fmt.Sprintf("+%.0f%%", reset.Gain), At: reset.At, Gain: reset.Gain})
				}
			}
		}
		c.Pool.Top = c.Top
		for i := 0; i <= 2; i++ {
			c.Pool.Gridlines = append(c.Pool.Gridlines, chartTick{fmt.Sprint(i * 50), fmt.Sprintf("%.0f%%", float64(i)*c.Top/2)})
		}
		drain := quotaDrain(accounts, from, end, q.Burn)
		max := 0.0
		for _, point := range drain {
			if point.Value > max {
				max = point.Value
			}
		}
		var futureDrain []quotaPoint
		if len(c.Projected) > 0 {
			futureDrain = []quotaPoint{{s.Now, rate}, {to, rate}}
			if rate > max {
				max = rate
			}
		}
		c.Drain.Top, c.Drain.Gridlines = chartTicks(max, "req")
		for i := range c.Drain.Gridlines {
			c.Drain.Gridlines[i].Label = fmt.Sprintf("%.3g%% per hour", float64(i)*c.Drain.Top/4)
		}
		for _, series := range []struct {
			figure              *quotaFigure
			recorded, projected []quotaPoint
		}{{&c.Pool, c.Recorded, c.Projected}, {&c.Drain, drain, futureDrain}} {
			geometry := func(points []quotaPoint) string {
				var vertices []string
				if series.figure.Top <= 0 {
					return ""
				}
				for _, point := range points {
					vertices = append(vertices, fmt.Sprintf("%.3f,%.3f", x(point.At)*10, 200-point.Value/series.figure.Top*200))
				}
				return strings.Join(vertices, " ")
			}
			// Recorded points are 10 minutes apart; a longer step is an
			// unobserved gap, so the recorded line breaks there.
			start := 0
			for i := range series.recorded {
				if i+1 == len(series.recorded) || series.recorded[i+1].At.Sub(series.recorded[i].At) > 10*time.Minute {
					if line := geometry(series.recorded[start : i+1]); line != "" {
						series.figure.Recorded = append(series.figure.Recorded, line)
					}
					start = i + 1
				}
			}
			series.figure.Projected = geometry(series.projected)
			// Equal-width columns cover the local-day page, including daylight-saving changes.
			for i := 0; i < 42; i++ {
				at := from.Add(time.Duration(float64(to.Sub(from)) * (float64(i) + .5) / 42))
				points, kind := series.recorded, "recorded"
				if at.After(s.Now) {
					points, kind = series.projected, "projected"
				}
				value, known, last := 0.0, false, time.Time{}
				for _, point := range points {
					if !point.At.After(at) {
						value, known, last = point.Value, true, point.At
					}
				}
				// A recorded point covers its own 10-minute bin, not a later gap.
				if kind == "recorded" && at.Sub(last) >= 10*time.Minute {
					known = false
				}
				text := at.In(s.Now.Location()).Format("Mon 15:04 UTC-07:00") + " - "
				if known {
					if series.figure == &c.Pool {
						text += fmt.Sprintf("%.0f%% left of %.0f%% - %s", value, c.Top, kind)
					} else {
						text += fmt.Sprintf("%.1f%% per hour - %s", value, kind)
					}
				} else if at.After(s.Now) {
					text += "Not Available"
				} else {
					text += "No recorded quota"
				}
				series.figure.Columns = append(series.figure.Columns, quotaTooltip{Text: text, Left: i >= 21})
			}
		}
		c.Pool.Provider, c.Pool.Kind, c.Pool.NowX, c.Pool.EmptyX, c.Pool.Resets = provider, "pool", c.NowX, c.EmptyX, c.Resets
		c.Drain.Provider, c.Drain.Kind, c.Drain.NowX = provider, "drain", c.NowX
		charts = append(charts, c)
	}
	return charts
}
