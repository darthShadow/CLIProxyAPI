package helps

import (
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestRelease1RestUsesRejectedWindowReset(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	unix := func(d time.Duration) string { return strconv.FormatInt(now.Add(d).Unix(), 10) }
	cases := []struct {
		name string
		hdrs [][2]string
		want time.Duration
	}{
		{
			name: "5h rejected, unified names the weekly reset",
			hdrs: [][2]string{
				{"Anthropic-Ratelimit-Unified-Status", "rejected"},
				{"Anthropic-Ratelimit-Unified-5h-Status", "rejected"},
				{"Anthropic-Ratelimit-Unified-5h-Reset", unix(time.Hour)},
				{"Anthropic-Ratelimit-Unified-7d-Status", "allowed"},
				{"Anthropic-Ratelimit-Unified-7d-Reset", unix(6 * 24 * time.Hour)},
				{"Anthropic-Ratelimit-Unified-Reset", unix(6 * 24 * time.Hour)},
			},
			want: time.Hour,
		},
		{
			name: "7d rejected",
			hdrs: [][2]string{
				{"Anthropic-Ratelimit-Unified-Status", "rejected"},
				{"Anthropic-Ratelimit-Unified-7d-Status", "rejected"},
				{"Anthropic-Ratelimit-Unified-7d-Reset", unix(30 * time.Hour)},
				{"Anthropic-Ratelimit-Unified-Reset", unix(30 * time.Hour)},
			},
			want: 30 * time.Hour,
		},
		{
			name: "no window identified, unified reset used",
			hdrs: [][2]string{
				{"Anthropic-Ratelimit-Unified-Status", "rejected"},
				{"Anthropic-Ratelimit-Unified-Reset", unix(2 * time.Hour)},
			},
			want: 2 * time.Hour,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			headers := http.Header{}
			for _, pair := range tc.hdrs {
				headers.Set(pair[0], pair[1])
			}
			got := parseClaudeRateLimitResetWithFuzz(headers, now, 0, 0)
			if got == nil {
				t.Fatal("reset = nil")
			}
			if diff := *got - tc.want; diff < -time.Second || diff > time.Second {
				t.Fatalf("reset = %s, want %s", *got, tc.want)
			}
		})
	}
}
