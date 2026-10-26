package management

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

type accountReader interface {
	Codex(context.Context, *config.Config, *coreauth.Auth) (helps.CodexAccountUsage, error)
	Claude(context.Context, *config.Config, *coreauth.Auth) (*claude.OAuthPlan, error)
}
type defaultAccountReader struct{}

var liveAccountReader accountReader = defaultAccountReader{}

func (defaultAccountReader) Codex(ctx context.Context, cfg *config.Config, a *coreauth.Auth) (helps.CodexAccountUsage, error) {
	return executor.ReadCodexAccountUsage(ctx, cfg, a)
}
func (defaultAccountReader) Claude(ctx context.Context, cfg *config.Config, a *coreauth.Auth) (*claude.OAuthPlan, error) {
	return claude.NewClaudeAuthWithProxyURL(cfg, a.ProxyURL).FetchOAuthPlan(ctx, tokenValueForAuth(a))
}

type accountRow struct {
	Provider, Account, Plan, Tier, Subscription, Renews, Error string
	Resets                                                     int
	Expiries                                                   []string
	DetailsError                                               string
}
type accountsView struct {
	Rows []accountRow
	Now  time.Time
}

func accountError(err error) string {
	if err == nil {
		return ""
	}
	var status interface{ StatusCode() int }
	if errors.As(err, &status) {
		return fmt.Sprintf("HTTP %d", status.StatusCode())
	}
	switch err.Error() {
	case "answered for a different account", "reset details unreadable":
		return err.Error()
	}
	return "request failed"
}
func signInError(err error) string {
	s := accountError(err)
	if s == "HTTP 401" || s == "HTTP 403" {
		return "The provider did not accept this account's sign-in (" + s + "). Sign in to this account again."
	}
	return s
}

// PostUsageAccounts reads eligible accounts sequentially only on a POST.
// Expired tokens are skipped. Results are escaped and never persisted; errors
// expose only approved status text, without any upstream header or body.
func (h *Handler) PostUsageAccounts(c *gin.Context) {
	if c.Request.Method != http.MethodPost {
		c.Status(http.StatusMethodNotAllowed)
		return
	}
	now := time.Now()
	view := accountsView{Now: now}
	if h.authManager == nil {
		renderUsage(c, "accounts.html", view)
		return
	}
	cfg := h.configSnapshot()
	for _, a := range usageAuths(h.authManager.List()) {
		row := accountRow{Provider: a.Provider, Account: usageAccount(a), Plan: "Not Reported", Tier: "Not Available", Renews: "Not Reported"}
		if expiry, ok := a.ExpirationTime(); ok && !expiry.After(now) {
			row.Error = "token expired at " + factTime(expiry.In(now.Location())) + "; the proxy refreshes it on the account's next request"
			view.Rows = append(view.Rows, row)
			continue
		}
		if a.Provider == "codex" {
			usage, errCodex := liveAccountReader.Codex(c.Request.Context(), cfg, a)
			row.Error = signInError(errCodex)
			if errCodex == nil {
				if usage.Plan != "" {
					row.Plan = usage.Plan
				}
				row.Resets = usage.ResetsAvailable
				row.DetailsError = accountError(usage.ResetDetailsErr)
				for i, expiry := range usage.ResetExpiries {
					prefix := fmt.Sprintf("Reset %d: ", i+1)
					switch {
					case expiry == nil:
						row.Expiries = append(row.Expiries, prefix+"does not expire")
					case !expiry.After(now):
						row.Expiries = append(row.Expiries, prefix+"expired at "+factTime(expiry.In(now.Location())))
					default:
						row.Expiries = append(row.Expiries, prefix+"expires "+factTime(expiry.In(now.Location()))+" ("+relativeTime(expiry.Sub(now))+")")
					}
				}
			}
			claim := extractCodexIDTokenClaims(a)["chatgpt_subscription_active_until"]
			var renewal time.Time
			switch value := claim.(type) {
			case string:
				renewal, _ = time.Parse(time.RFC3339, value)
			case float64:
				renewal = time.Unix(int64(value), 0)
			}
			if !renewal.IsZero() {
				row.Renews = factTime(renewal.In(now.Location()))
			}
		} else {
			plan, errClaude := liveAccountReader.Claude(c.Request.Context(), cfg, a)
			row.Error = signInError(errClaude)
			if errClaude == nil && plan != nil {
				switch {
				case plan.Account.HasClaudeMax != nil && *plan.Account.HasClaudeMax:
					row.Plan = "Max"
				case plan.Account.HasClaudePro != nil && *plan.Account.HasClaudePro:
					row.Plan = "Pro"
				case strings.EqualFold(strings.TrimSpace(plan.Organization.OrganizationType), "claude_team") && strings.EqualFold(strings.TrimSpace(plan.Organization.SubscriptionStatus), "active"):
					row.Plan = "Team"
				case plan.Account.HasClaudeMax != nil && plan.Account.HasClaudePro != nil:
					row.Plan = "Free"
				}
				if plan.Organization.RateLimitTier != "" {
					row.Tier = plan.Organization.RateLimitTier
				}
				row.Subscription = plan.Organization.SubscriptionStatus
			}
		}
		view.Rows = append(view.Rows, row)
	}
	renderUsage(c, "accounts.html", view)
}
