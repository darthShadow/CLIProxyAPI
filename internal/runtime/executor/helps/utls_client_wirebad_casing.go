//go:build wiregate && wirebad_casing

package helps

// Known-bad control for the wire gate: anthropic-beta is respelled
// Anthropic-Beta (position kept), so only the casing class may fail.
func init() {
	for _, order := range [][]string{claudeCodeMessagesHeaderOrder, claudeCodeCountTokensHeaderOrder} {
		for index, name := range order {
			if name == "anthropic-beta" {
				order[index] = "Anthropic-Beta"
			}
		}
	}
}
