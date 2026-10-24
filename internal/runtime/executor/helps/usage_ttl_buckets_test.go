package helps

import (
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
)

func TestParseClaudeUsageRecordsCacheWriteTTLBuckets(t *testing.T) {
	cases := []struct {
		name    string
		body    string
		want5m  int64
		want1h  int64
		wantAll int64
	}{
		{name: "split reported", body: `{"usage":{"input_tokens":3,"output_tokens":4,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}}}`, want5m: 100, want1h: 200, wantAll: 300},
		{name: "1-hour only", body: `{"usage":{"input_tokens":3,"output_tokens":4,"cache_creation_input_tokens":50,"cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":50}}}`, want5m: 0, want1h: 50, wantAll: 50},
		{name: "split absent", body: `{"usage":{"input_tokens":3,"output_tokens":4,"cache_creation_input_tokens":70}}`, want5m: 0, want1h: 0, wantAll: 70},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			detail := ParseClaudeUsage([]byte(tc.body))
			if detail.CacheCreation5mTokens != tc.want5m || detail.CacheCreation1hTokens != tc.want1h || detail.CacheCreationTokens != tc.wantAll {
				t.Fatalf("buckets = 5m %d, 1h %d, all %d; want 5m %d, 1h %d, all %d", detail.CacheCreation5mTokens, detail.CacheCreation1hTokens, detail.CacheCreationTokens, tc.want5m, tc.want1h, tc.wantAll)
			}
		})
	}
}

func TestMergeStreamUsageDetailKeepsCacheWriteTTLBuckets(t *testing.T) {
	start, ok := ParseClaudeStreamUsage([]byte(`data: {"type":"message_start","message":{"usage":{"input_tokens":3,"output_tokens":1,"cache_creation_input_tokens":300,"cache_creation":{"ephemeral_5m_input_tokens":100,"ephemeral_1h_input_tokens":200}}}}`))
	if !ok {
		t.Fatal("message_start usage not parsed")
	}
	delta, ok := ParseClaudeStreamUsage([]byte(`data: {"type":"message_delta","usage":{"output_tokens":9}}`))
	if !ok {
		t.Fatal("message_delta usage not parsed")
	}
	merged := MergeStreamUsageDetail(start, delta)
	want := usage.Detail{CacheCreation5mTokens: 100, CacheCreation1hTokens: 200}
	if merged.CacheCreation5mTokens != want.CacheCreation5mTokens || merged.CacheCreation1hTokens != want.CacheCreation1hTokens {
		t.Fatalf("merged buckets = 5m %d, 1h %d; want 5m %d, 1h %d", merged.CacheCreation5mTokens, merged.CacheCreation1hTokens, want.CacheCreation5mTokens, want.CacheCreation1hTokens)
	}
}
