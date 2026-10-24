package redisqueue

import (
	"context"
	"net/http"
	"testing"

	internallogging "github.com/router-for-me/CLIProxyAPI/v8/internal/logging"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestUsageQueuePluginPublishesCacheWriteTTLBuckets(t *testing.T) {
	withEnabledQueue(t, func() {
		ctx := internallogging.WithResponseStatusHolder(context.Background())
		internallogging.SetResponseStatus(ctx, http.StatusOK)

		(&usageQueuePlugin{}).HandleUsage(ctx, coreusage.Record{
			Provider: "claude",
			Model:    "claude-opus-5-5",
			Detail: coreusage.Detail{
				InputTokens:           10,
				OutputTokens:          5,
				CacheCreationTokens:   300,
				CacheCreation5mTokens: 100,
				CacheCreation1hTokens: 200,
			},
		})

		tokens := requireTokensPayload(t, popSinglePayload(t))
		requireIntField(t, tokens, "cache_creation_5m_tokens", 100)
		requireIntField(t, tokens, "cache_creation_1h_tokens", 200)
		requireIntField(t, tokens, "cache_creation_tokens", 300)
	})
}
