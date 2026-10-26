package claude

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
)

// OAuthPlan contains only the five allowlisted profile fields.
type OAuthPlan struct {
	Account struct {
		HasClaudeMax *bool `json:"has_claude_max"`
		HasClaudePro *bool `json:"has_claude_pro"`
	} `json:"account"`
	Organization struct {
		OrganizationType   string `json:"organization_type"`
		RateLimitTier      string `json:"rate_limit_tier"`
		SubscriptionStatus string `json:"subscription_status"`
	} `json:"organization"`
}

// OAuthPlanStatusError exposes only the profile response status.
type OAuthPlanStatusError struct{ Status int }

func (e OAuthPlanStatusError) Error() string { return fmt.Sprintf("HTTP %d", e.Status) }

// StatusCode returns the upstream status without response content.
func (e OAuthPlanStatusError) StatusCode() int { return e.Status }

var planStatus = regexp.MustCompile(`failed with status ([0-9]{3})$`)

// FetchOAuthPlan reads five plan fields using the existing Axios profile GET.
// It returns only a status or a generic error; it neither refreshes nor persists.
func (o *ClaudeAuth) FetchOAuthPlan(ctx context.Context, token string) (*OAuthPlan, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	body, err := o.fetchOAuthControlPlaneJSON(ctx, ProfileURL, token, "profile")
	if err != nil {
		if match := planStatus.FindStringSubmatch(err.Error()); len(match) == 2 {
			status, _ := strconv.Atoi(match[1])
			return nil, OAuthPlanStatusError{status}
		}
		return nil, fmt.Errorf("request failed")
	}
	var plan OAuthPlan
	if json.Unmarshal(body, &plan) != nil {
		return nil, fmt.Errorf("request failed")
	}
	return &plan, nil
}
