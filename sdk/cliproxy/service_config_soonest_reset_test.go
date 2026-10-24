package cliproxy

import (
	"testing"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestRelease1RoutingStrategySoonestReset(t *testing.T) {
	cfg := &config.Config{}
	cfg.Routing.Strategy = "soonest-reset"
	state := normalizedRoutingRuntimeState(cfg)
	if state.strategy != "soonest-reset" {
		t.Fatalf("strategy = %q, want soonest-reset", state.strategy)
	}
	if _, ok := newRoutingSelector(state).(*coreauth.SoonestResetSelector); !ok {
		t.Fatalf("selector = %T, want *SoonestResetSelector", newRoutingSelector(state))
	}
}
