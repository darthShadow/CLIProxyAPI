//go:build wiregate && wirebad_order

package helps

// Known-bad control for the wire gate: anthropic-beta and anthropic-version
// trade places (spelling kept), so only the order class may fail.
func init() {
	for _, order := range [][]string{claudeCodeMessagesHeaderOrder, claudeCodeCountTokensHeaderOrder} {
		beta, version := -1, -1
		for index, name := range order {
			switch name {
			case "anthropic-beta":
				beta = index
			case "anthropic-version":
				version = index
			}
		}
		if beta >= 0 && version >= 0 {
			order[beta], order[version] = order[version], order[beta]
		}
	}
}
