package claude

import (
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/interfaces"
	"github.com/tidwall/gjson"
)

// anthropicErrorEnvelope returns the upstream error text when it is a valid
// Anthropic error envelope: top-level "type":"error" and an "error" object with
// string "type" and "message". A proxy refusal built in that shape, such as
// the unconfirmed-client refusal, is written unchanged too.
func anthropicErrorEnvelope(msg *interfaces.ErrorMessage) ([]byte, bool) {
	if msg == nil || msg.Error == nil {
		return nil, false
	}
	text := strings.TrimSpace(msg.Error.Error())
	if !gjson.Valid(text) {
		return nil, false
	}
	root := gjson.Parse(text)
	if root.Get("type").String() != "error" || !root.Get("error").IsObject() {
		return nil, false
	}
	if root.Get("error.type").Type != gjson.String || root.Get("error.message").Type != gjson.String {
		return nil, false
	}
	return []byte(text), true
}
