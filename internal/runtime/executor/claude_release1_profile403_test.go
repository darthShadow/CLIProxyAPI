package executor

import (
	"context"
	"fmt"
	"testing"

	claudeauth "github.com/router-for-me/CLIProxyAPI/v8/internal/auth/claude"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRelease1ProfileUUIDOnlyOnProfileHTTP403(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		synthesis bool
	}{
		{name: "profile http 403", err: fmt.Errorf("fetch Claude OAuth profile failed with status 403"), synthesis: true},
		{name: "proxy connect forbidden", err: fmt.Errorf("fetch Claude OAuth profile: proxyconnect tcp: 403 Forbidden")},
		{name: "transport text with 403", err: fmt.Errorf("fetch Claude OAuth profile: dial tcp 10.0.0.403:443: i/o timeout")},
		{name: "permission text without status", err: fmt.Errorf("fetch Claude OAuth profile: permission_error")},
		{name: "profile http 402", err: fmt.Errorf("fetch Claude OAuth profile failed with status 402")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			executor := NewClaudeExecutor(&config.Config{})
			executor.oauthProfileFetcher = func(context.Context, *cliproxyauth.Auth, string) (*claudeauth.OAuthProfile, error) {
				return nil, tc.err
			}
			auth := &cliproxyauth.Auth{
				ID:         "claude-profile-403-" + tc.name,
				Attributes: map[string]string{"api_key": "sk-ant-oat-profile-403"},
				Metadata:   map[string]any{"type": "claude"},
			}
			prepared, errPrepare := executor.PrepareRequestAuth(context.Background(), auth)
			if tc.synthesis {
				if errPrepare != nil || prepared == nil || prepared.Metadata["account_uuid"] == nil {
					t.Fatalf("PrepareRequestAuth = %v, %v; want synthesized account_uuid", prepared, errPrepare)
				}
				return
			}
			if errPrepare == nil {
				t.Fatal("PrepareRequestAuth error = nil, want the profile error")
			}
			if _, ok := auth.Metadata["account_uuid"]; ok {
				t.Fatalf("account_uuid persisted after %q", tc.err)
			}
		})
	}
}
