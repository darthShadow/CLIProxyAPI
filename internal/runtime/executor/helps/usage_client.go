package helps

import (
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// UsageClient returns a closed archive client name from userAgent, or an empty
// string when no user agent is recorded (empty or blank). It never returns the
// raw user agent.
func UsageClient(userAgent string) string {
	if strings.TrimSpace(userAgent) == "" {
		return ""
	}
	if coreauth.IsCodexClientUserAgent(userAgent) {
		return "codex"
	}
	if claudeCodeUserAgentPattern.MatchString(userAgent) {
		entrypoint, _ := parseClaudeCodeUserAgentDetails(userAgent)
		if client := claudeCodeSubclientByEntrypoint[entrypoint]; client != "" {
			return client
		}
		return "claude-other"
	}
	return "other"
}
