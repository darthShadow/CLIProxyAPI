package helps

import "github.com/tidwall/gjson"

// The Agent SDK (sdk-ts) session-title helper of Claude Code 2.1.299, as
// test/release1-client/sdk_ts.sh recorded it (Agent SDK 0.3.290, the Haiku
// model of the shared settings, attribution off). It reaches the proxy with the
// proxy's own key, so its beta list has no OAuth beta. claudeCodeSDKTitleKeys is
// its top-level key order; claudeCodeSDKTitleFixed holds the raw JSON of every
// top-level value except messages, system, and metadata.
//
// The session title of interactive Claude Code 2.1.299 (cli) has the same keys
// and fixed values, and the CLI identity as its first system block. Its beta
// list is claudeCodeSDKTitleBetas when showThinkingSummaries is on, as in the
// shared settings, and claudeCodeCLITitleBetas, which adds
// redact-thinking-2026-02-12, when it is off, as an attended loopback session
// recorded it.
const (
	claudeCodeSDKTitleBetas = "context-1m-2025-08-07,interleaved-thinking-2025-05-14,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,effort-2025-11-24,structured-outputs-2025-12-15"
	claudeCodeSDKIdentity   = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	claudeCodeCLITitleBetas = "context-1m-2025-08-07,interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,thinking-token-count-2026-05-13,context-management-2025-06-27,prompt-caching-scope-2026-01-05,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,effort-2025-11-24,structured-outputs-2025-12-15"
	claudeCodeCLIIdentity   = "You are Claude Code, Anthropic's official CLI for Claude."
)

var (
	claudeCodeSDKTitleKeys  = []string{"model", "messages", "system", "tools", "metadata", "max_tokens", "output_config", "stream"}
	claudeCodeSDKTitleFixed = map[string]string{
		"model":         `"claude-haiku-5-5"`,
		"tools":         `[]`,
		"max_tokens":    `64000`,
		"output_config": `{"effort":"medium","format":{"type":"json_schema","schema":{"type":"object","properties":{"title":{"type":"string"}},"required":["title"],"additionalProperties":false}}}`,
		"stream":        `true`,
	}
)

// matchesClaudeCodeSDKTitleBody reports whether payload has the captured Agent
// SDK session-title shape: the same top-level keys in the same order, the same
// fixed values, one user message with one text block, and two text system
// blocks, the first of them the Agent SDK or the CLI identity. Each text value
// is a nonempty JSON string.
func matchesClaudeCodeSDKTitleBody(payload []byte) bool {
	if !claudeJSONObjectHasKeys(payload, claudeCodeSDKTitleKeys) {
		return false
	}
	for key, raw := range claudeCodeSDKTitleFixed {
		if gjson.GetBytes(payload, key).Raw != raw {
			return false
		}
	}
	messages := gjson.GetBytes(payload, "messages")
	message := messages.Get("0")
	content := message.Get("content")
	system := gjson.GetBytes(payload, "system")
	if !messages.IsArray() || len(messages.Array()) != 1 ||
		!claudeJSONObjectHasKeys([]byte(message.Raw), []string{"role", "content"}) ||
		message.Get("role").String() != "user" ||
		!content.IsArray() || len(content.Array()) != 1 ||
		!system.IsArray() || len(system.Array()) != 2 {
		return false
	}
	for _, block := range []gjson.Result{content.Get("0"), system.Get("0"), system.Get("1")} {
		text := block.Get("text")
		if !claudeJSONObjectHasKeys([]byte(block.Raw), []string{"type", "text"}) ||
			block.Get("type").String() != "text" || text.Type != gjson.String || text.String() == "" {
			return false
		}
	}
	identity := system.Get("0.text").String()
	return identity == claudeCodeSDKIdentity || identity == claudeCodeCLIIdentity
}

// claudeCodeTitleEntrypoint returns the entrypoint that sends a session title
// with this system identity and beta list, or "" when none does: sdk-ts sends
// the Agent SDK identity with claudeCodeSDKTitleBetas only; cli sends the CLI
// identity with either title beta list.
func claudeCodeTitleEntrypoint(payload []byte, betas string) string {
	switch gjson.GetBytes(payload, "system.0.text").String() {
	case claudeCodeSDKIdentity:
		if betas == claudeCodeSDKTitleBetas {
			return "sdk-ts"
		}
	case claudeCodeCLIIdentity:
		return "cli"
	}
	return ""
}
