package test

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

const release1ClaudeCodeUA = "claude-cli/2.1.285 (external, cli)"

func release1StopStatuses(rules []config.RequestScopedErrorRule) map[int]bool {
	statuses := make(map[int]bool, len(rules))
	for _, rule := range rules {
		if rule.Action == "stop" && len(rule.MatchRegexr) == 1 && rule.MatchRegexr[0] == "^" {
			statuses[rule.Status] = true
		}
	}
	return statuses
}

func release1RequireStatuses(t *testing.T, name string, got map[int]bool, want []int) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s stop statuses = %v, want exactly %v", name, got, want)
	}
	for _, status := range want {
		if !got[status] {
			t.Fatalf("%s stop statuses = %v, missing %d", name, got, status)
		}
	}
}

var (
	release1ClaudeStops = []int{408, 429, 500, 501, 502, 503, 504, 520, 521, 522, 523, 524, 525, 526, 529}
	release1CodexStops  = []int{402, 403, 408, 429, 500, 501, 502, 503, 504, 520, 521, 522, 523, 524, 525, 526, 529}
)

// release1Require429Rows checks the 429 rows: per-minute throttles stop, and a
// plan limit takes credentialAction ("" means no credential row: stock rest and
// move). No unscoped 429 row may remain beside scoped ones.
func release1Require429Rows(t *testing.T, name string, rules []config.RequestScopedErrorRule, credentialAction string) {
	t.Helper()
	got := map[string]string{}
	for _, rule := range rules {
		if rule.Status == 429 {
			got[rule.Scope] = rule.Action
		}
	}
	want := map[string]string{"model": "stop"}
	if credentialAction != "" {
		want["credential"] = credentialAction
	}
	if len(got) != len(want) {
		t.Fatalf("%s 429 rows = %v, want %v", name, got, want)
	}
	for scope, action := range want {
		if got[scope] != action {
			t.Fatalf("%s 429 rows = %v, want %v", name, got, want)
		}
	}
}

func release1RequireSharedPolicy(t *testing.T, cfg *config.Config) {
	t.Helper()
	if cfg.Host != "127.0.0.1" {
		t.Fatalf("host = %q, want 127.0.0.1", cfg.Host)
	}
	if !cfg.DisableClaudeCloakMode || !cfg.ClaudeCode.RefuseUnconfirmedClients || !cfg.ClaudeCode.DisableCloakingModelList {
		t.Fatalf("cloak policy = disable %v, refuse %v, model list %v; want all true", cfg.DisableClaudeCloakMode, cfg.ClaudeCode.RefuseUnconfirmedClients, cfg.ClaudeCode.DisableCloakingModelList)
	}
	if cfg.ClaudeHeaderDefaults.UserAgent != release1ClaudeCodeUA || cfg.ClaudeHeaderDefaults.StabilizeDeviceProfile != nil {
		t.Fatalf("claude-header-defaults = %+v, want only user-agent %q", cfg.ClaudeHeaderDefaults, release1ClaudeCodeUA)
	}
	if scoped := cfg.ForAPIKey(); scoped.ClaudeHeaderDefaults.UserAgent != release1ClaudeCodeUA || !scoped.ClaudeCode.RefuseUnconfirmedClients {
		t.Fatalf("API-key view lost the version tuple or refusal switch: %+v", scoped.ClaudeHeaderDefaults)
	}
	if !cfg.Codex.RefuseExhaustedIncludedUsage {
		t.Fatal("codex.refuse-exhausted-included-usage = false, want true")
	}
	if !cfg.PassthroughHeaders || cfg.CommercialMode || cfg.RequestRetry != 0 || cfg.TransientErrorCooldownSeconds != -1 {
		t.Fatalf("passthrough %v, commercial %v, request-retry %d, transient cooldown %d; want true, false, 0, -1", cfg.PassthroughHeaders, cfg.CommercialMode, cfg.RequestRetry, cfg.TransientErrorCooldownSeconds)
	}
	if !cfg.Routing.SessionAffinity || cfg.Routing.SessionAffinityTTL != "1h" {
		t.Fatalf("routing = %+v, want session affinity with 1h TTL", cfg.Routing)
	}
	if cfg.RemoteManagement.AllowRemote || cfg.RemoteManagement.SecretKey != "" || !cfg.RemoteManagement.DisableAutoUpdatePanel {
		t.Fatalf("remote-management allow-remote %v, secret-key set %v, disable-auto-update-panel %v; want false, false (only the live copy holds a bcrypt hash), true", cfg.RemoteManagement.AllowRemote, cfg.RemoteManagement.SecretKey != "", cfg.RemoteManagement.DisableAutoUpdatePanel)
	}
	if cfg.DisableImageGeneration != config.DisableImageGenerationChat {
		t.Fatalf("disable-image-generation = %v, want chat", cfg.DisableImageGeneration)
	}
	if len(cfg.Payload.Override) != 1 || cfg.Payload.Override[0].Params["text.verbosity"] != "low" {
		t.Fatalf("payload override = %+v, want text.verbosity low", cfg.Payload.Override)
	}
	if len(cfg.Payload.Filter) != 1 || len(cfg.Payload.Filter[0].Params) != 2 || cfg.Payload.Filter[0].Params[0] != "service_tier" {
		t.Fatalf("payload filter = %+v, want service_tier and the image tool", cfg.Payload.Filter)
	}
}

func TestRelease1PersonalProxyConfig(t *testing.T) {
	cfg, errLoad := config.LoadConfig("../deploy/P.config.yaml")
	if errLoad != nil {
		t.Fatalf("LoadConfig(P) error = %v", errLoad)
	}
	release1RequireSharedPolicy(t, cfg)
	if cfg.RemoteManagement.DisableControlPanel {
		t.Fatal("P disable-control-panel = true, want the pinned management page served")
	}
	if cfg.Routing.Strategy != "soonest-reset" {
		t.Fatalf("P routing strategy = %q, want soonest-reset", cfg.Routing.Strategy)
	}
	if cfg.Port != 8317 || cfg.MaxRetryCredentials != 1 || !cfg.Routing.SessionAffinityHold {
		t.Fatalf("port %d, max-retry-credentials %d, hold %v; want 8317, 1, true", cfg.Port, cfg.MaxRetryCredentials, cfg.Routing.SessionAffinityHold)
	}
	if len(cfg.ClaudeKey) != 0 || len(cfg.CodexKey) != 0 {
		t.Fatalf("P has %d claude and %d codex API keys, want none (login files only)", len(cfg.ClaudeKey), len(cfg.CodexKey))
	}
	if len(cfg.APIKeys) != 2 {
		t.Fatalf("P access keys = %d, want 2 (personal-proxy, W delegate)", len(cfg.APIKeys))
	}
	release1RequireStatuses(t, "P claude", release1StopStatuses(cfg.OAuthRequestScopedErrors["claude"]), release1ClaudeStops)
	release1RequireStatuses(t, "P codex", release1StopStatuses(cfg.OAuthRequestScopedErrors["codex"]), release1CodexStops)
	release1Require429Rows(t, "P claude", cfg.OAuthRequestScopedErrors["claude"], "stop-and-cooldown")
	release1Require429Rows(t, "P codex", cfg.OAuthRequestScopedErrors["codex"], "stop-and-cooldown")
}

func TestRelease1WorkProxyConfig(t *testing.T) {
	cfg, errLoad := config.LoadConfig("../deploy/W.config.yaml")
	if errLoad != nil {
		t.Fatalf("LoadConfig(W) error = %v", errLoad)
	}
	release1RequireSharedPolicy(t, cfg)
	if !cfg.RemoteManagement.DisableControlPanel {
		t.Fatal("W disable-control-panel = false, want no management page on W")
	}
	if cfg.Port != 8318 || cfg.MaxRetryCredentials != 2 || cfg.Routing.SessionAffinityHold {
		t.Fatalf("port %d, max-retry-credentials %d, hold %v; want 8318, 2, false", cfg.Port, cfg.MaxRetryCredentials, cfg.Routing.SessionAffinityHold)
	}
	if len(cfg.ClaudeKey) != 1 || cfg.ClaudeKey[0].BaseURL != "http://127.0.0.1:8317" || cfg.ClaudeKey[0].Priority != -1 {
		t.Fatalf("W claude delegate = %+v, want one key to P at priority -1", cfg.ClaudeKey)
	}
	if len(cfg.CodexKey) != 1 || cfg.CodexKey[0].BaseURL != "http://127.0.0.1:8317/v1" {
		t.Fatalf("W codex delegate = %+v, want one key to P /v1", cfg.CodexKey)
	}
	if cfg.ClaudeKey[0].APIKey != cfg.CodexKey[0].APIKey {
		t.Fatal("W delegate keys differ; both must equal the W delegate key in P")
	}
	release1RequireStatuses(t, "W claude", release1StopStatuses(cfg.OAuthRequestScopedErrors["claude"]), release1ClaudeStops)
	release1RequireStatuses(t, "W claude delegate", release1StopStatuses(cfg.ClaudeKey[0].RequestScopedErrors), release1ClaudeStops)
	release1RequireStatuses(t, "W codex delegate", release1StopStatuses(cfg.CodexKey[0].RequestScopedErrors), release1CodexStops)
	release1Require429Rows(t, "W claude", cfg.OAuthRequestScopedErrors["claude"], "")
	for _, rule := range cfg.ClaudeKey[0].RequestScopedErrors {
		if rule.Status == 429 && (rule.Scope != "" || rule.Action != "stop") {
			t.Fatalf("W claude delegate 429 row = %+v, want unscoped stop", rule)
		}
	}
}
