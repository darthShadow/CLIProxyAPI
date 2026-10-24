package helps

import "github.com/tidwall/gjson"

// The Agent SDK (sdk-ts) session-title helper of Claude Code 2.1.311, as
// testdata/sdk_ts_title.json records it (Agent SDK 0.3.290, the Haiku model
// of the shared settings, attribution off). It reaches the proxy with the
// proxy's own key, so its beta list has no OAuth beta. claudeCodeSDKTitleKeys is
// its top-level key order; claudeCodeSDKTitleFixed holds the raw JSON of every
// top-level value except messages, system, and metadata.
//
// The session title of interactive Claude Code 2.1.311 (cli) has the same keys
// and fixed values, and the CLI identity as its first system block. Its beta
// list is claudeCodeSDKTitleBetas when showThinkingSummaries is on, as in the
// shared settings, and claudeCodeCLITitleBetas, which adds
// redact-thinking-2026-02-12, when it is off, as the attended loopback capture
// in testdata/cli_title.json records it.
const (
	claudeCodeSDKTitleBetas = "interleaved-thinking-2025-05-14,context-management-2025-06-27,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,thinking-token-count-2026-05-13,effort-2025-11-24,structured-outputs-2025-12-15"
	claudeCodeSDKIdentity   = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	claudeCodeCLITitleBetas = "interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,thinking-token-count-2026-05-13,effort-2025-11-24,structured-outputs-2025-12-15"
	claudeCodeCLIIdentity   = "You are Claude Code, Anthropic's official CLI for Claude."
)

// The Claude Code 2.1.311 WebFetch page-summary helper lists, pinned apart from
// the title lists. The first-try lists are those of
// testdata/sdk_ts_web_fetch.json and testdata/cli_web_fetch.json; each retry
// list is its first-try list without thinking-token-count-2026-05-13.
const (
	claudeCodeSDKWebFetchBetas      = "interleaved-thinking-2025-05-14,context-management-2025-06-27,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,thinking-token-count-2026-05-13,effort-2025-11-24"
	claudeCodeSDKWebFetchRetryBetas = "interleaved-thinking-2025-05-14,context-management-2025-06-27,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,effort-2025-11-24"
	claudeCodeCLIWebFetchBetas      = "interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,thinking-token-count-2026-05-13,effort-2025-11-24"
	claudeCodeCLIWebFetchRetryBetas = "interleaved-thinking-2025-05-14,redact-thinking-2026-02-12,context-management-2025-06-27,per-turn-control-2026-07-01,mid-conversation-tool-changes-2026-07-01,effort-2025-11-24"
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
	return matchesClaudeCodeTextHelperBody(payload, 2, claudeCodeSDKTitleFixed["output_config"])
}

// matchesClaudeCodeWebFetchBody recognizes the captured WebFetch envelope:
// one identity system block and medium effort without an output format.
func matchesClaudeCodeWebFetchBody(payload []byte) bool {
	return matchesClaudeCodeTextHelperBody(payload, 1, `{"effort":"medium"}`)
}

// matchesClaudeCodeTextHelperBody checks the key order, fixed values, and
// nonempty text blocks shared by the title and WebFetch helper envelopes.
func matchesClaudeCodeTextHelperBody(payload []byte, systemBlocks int, outputConfig string) bool {
	if !claudeJSONObjectHasKeys(payload, claudeCodeSDKTitleKeys) {
		return false
	}
	for key, raw := range claudeCodeSDKTitleFixed {
		if key == "output_config" {
			raw = outputConfig
		}
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
		!system.IsArray() || len(system.Array()) != systemBlocks {
		return false
	}
	for _, block := range append([]gjson.Result{content.Get("0")}, system.Array()...) {
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

// claudeCodeWebFetchEntrypoint selects the measured entrypoint by identity
// and beta list. CLI identity also uses SDK lists with showThinkingSummaries.
// SDK identity with CLI lists has no entrypoint, including for custom labels.
func claudeCodeWebFetchEntrypoint(payload []byte, betas, entrypoint string) string {
	switch gjson.GetBytes(payload, "system.0.text").String() {
	case claudeCodeSDKIdentity:
		if betas == claudeCodeSDKWebFetchBetas || betas == claudeCodeSDKWebFetchRetryBetas {
			if entrypoint == "sdk-cli" {
				return "sdk-cli"
			}
			return "sdk-ts"
		}
	case claudeCodeCLIIdentity:
		return "cli"
	}
	return ""
}
