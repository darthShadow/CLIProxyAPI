package registry

import "testing"

// Under the shared settings, Claude Code 2.1.295 sends its Haiku requests, among
// them the Agent SDK session title, with model claude-haiku-5-5. A proxy started
// with -local-model routes only the models of its embedded catalog.
func TestEmbeddedClaudeCatalogHasHaiku55(t *testing.T) {
	if LookupStaticModelInfoByChannel("claude-haiku-5-5", "claude") == nil {
		t.Fatal("embedded claude catalog has no claude-haiku-5-5")
	}
}
