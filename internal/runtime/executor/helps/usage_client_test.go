package helps

import "testing"

func TestUsageClientClassifiesUserAgents(t *testing.T) {
	for _, tc := range []struct{ agent, want string }{
		{"claude-cli/2.1.310 (external, cli)", "claude-code-cli"},
		{"claude-cli/2.1.311 (external, shadow-code)", "claude-code-shadow"},
		{"claude-cli/2.1.311 (external, t3-code, agent-sdk/0.3.290)", "claude-code-t3"},
		{"claude-cli/2.1.310 (external, sdk-ts, agent-sdk/0.3.290)", "claude-code-sdk-ts"},
		{"claude-cli/2.1.310 (external, unknown)", "claude-other"},
		{"claude-cli/2.1.310", "claude-other"},
		{"codex_cli_rs/0.1.0", "codex"},
		{"curl/8.0", "other"},
		{"", ""},
		{"   ", ""},
	} {
		t.Run(tc.agent, func(t *testing.T) {
			if got := UsageClient(tc.agent); got != tc.want {
				t.Errorf("UsageClient(%q)=%q, want %q", tc.agent, got, tc.want)
			}
		})
	}
}
