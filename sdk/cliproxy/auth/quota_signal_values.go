package auth

import (
	"strconv"
	"strings"
	"time"
)

// SignalUnixTime returns the time named by a Unix-seconds quota signal. It is
// zero when the signal is absent, does not parse, or is not positive.
func (q QuotaState) SignalUnixTime(name string) time.Time {
	seconds, errParse := strconv.ParseInt(strings.TrimSpace(q.Signals[name]), 10, 64)
	if errParse != nil || seconds <= 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0)
}

// SignalResetAfter returns ObservedAt plus a seconds-from-observation quota
// signal. It is zero when ObservedAt is zero or the signal is absent, does not
// parse, or is negative.
func (q QuotaState) SignalResetAfter(name string) time.Time {
	if q.ObservedAt.IsZero() {
		return time.Time{}
	}
	seconds, errParse := strconv.ParseInt(strings.TrimSpace(q.Signals[name]), 10, 64)
	if errParse != nil || seconds < 0 {
		return time.Time{}
	}
	return q.ObservedAt.Add(time.Duration(seconds) * time.Second)
}

// SignalPercent returns a used-percent quota signal and whether it parses.
func (q QuotaState) SignalPercent(name string) (float64, bool) {
	value, errParse := strconv.ParseFloat(strings.TrimSpace(q.Signals[name]), 64)
	return value, errParse == nil
}
