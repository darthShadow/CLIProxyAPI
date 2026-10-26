package claude

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestFetchOAuthPlanReadsPlanFields(t *testing.T) {
	for _, status := range []int{200, 401} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			calls := 0
			o := &ClaudeAuth{httpClient: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" || r.URL.String() != ProfileURL {
					t.Errorf("request=%s %s", r.Method, r.URL)
				}
				for key, want := range map[string]string{"Authorization": "Bearer fake-access-token", "User-Agent": "axios/1.15.2", "Accept": "application/json, text/plain, */*", "Content-Type": "application/json", "Accept-Encoding": "gzip, compress, deflate, br", "Cache-Control": "no-cache", "Connection": "close"} {
					if r.Header.Get(key) != want {
						t.Errorf("header %s=%q want=%q", key, r.Header.Get(key), want)
					}
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"account":{"has_claude_max":true,"has_claude_pro":false},"organization":{"organization_type":"claude_max","rate_limit_tier":"default_claude_max_20x","subscription_status":"active"},"access_token":"sk-fake-upstream-1111"}`)), Request: r}, nil
			})}}
			v, e := o.FetchOAuthPlan(context.Background(), "fake-access-token")
			if status == 200 {
				if e != nil || v == nil {
					t.Fatalf("plan missing: %v %v", v, e)
				}
				if v.Account.HasClaudeMax == nil || !*v.Account.HasClaudeMax || v.Account.HasClaudePro == nil || *v.Account.HasClaudePro || v.Organization.OrganizationType != "claude_max" || v.Organization.RateLimitTier != "default_claude_max_20x" || v.Organization.SubscriptionStatus != "active" {
					t.Errorf("plan=%+v", v)
				}
			} else if e == nil || e.Error() != "HTTP 401" {
				t.Errorf("error=%v want=HTTP 401", e)
			}
			if calls != 1 {
				t.Errorf("calls=%d want=1", calls)
			}
		})
	}
}
