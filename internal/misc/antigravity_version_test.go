package misc

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sync/atomic"
	"testing"
	"time"
)

func overrideAntigravityVersionURLsForTest(t *testing.T, hubManifestURL string) func() {
	t.Helper()

	oldHubManifest := antigravityHubLatestManifestURL
	antigravityHubLatestManifestURL = hubManifestURL

	return func() {
		antigravityHubLatestManifestURL = oldHubManifest
	}
}

func overrideAntigravityVersionCacheForTest(t *testing.T, version string, expiry time.Time) func() {
	t.Helper()

	antigravityVersionMu.Lock()
	oldVersion := cachedAntigravityVersion
	oldExpiry := antigravityVersionExpiry
	cachedAntigravityVersion = version
	antigravityVersionExpiry = expiry
	antigravityVersionMu.Unlock()

	return func() {
		antigravityVersionMu.Lock()
		cachedAntigravityVersion = oldVersion
		antigravityVersionExpiry = oldExpiry
		antigravityVersionMu.Unlock()
	}
}

func TestAntigravityLatestVersionUsesCurrentHubFallback(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "", time.Time{})
	defer restore()

	version := AntigravityLatestVersion()
	if version != antigravityFallbackVersion {
		t.Fatalf("AntigravityLatestVersion() = %q, want %q", version, antigravityFallbackVersion)
	}
}

// Cloud Code resolves newer models only for clients reporting at least 2.23.0;
// older versions get 404 Requested entity was not found.
func TestAntigravityFallbackVersionMeetsBackendFloor(t *testing.T) {
	const floorMajor, floorMinor = 2, 23

	var major, minor, patch int
	if _, err := fmt.Sscanf(antigravityFallbackVersion, "%d.%d.%d", &major, &minor, &patch); err != nil {
		t.Fatalf("antigravityFallbackVersion = %q is not a dotted version: %v", antigravityFallbackVersion, err)
	}
	if major < floorMajor || (major == floorMajor && minor < floorMinor) {
		t.Fatalf("antigravityFallbackVersion = %q, want at least %d.%d.0", antigravityFallbackVersion, floorMajor, floorMinor)
	}
}

func TestAntigravityUserAgentUsesHubFamily(t *testing.T) {
	for _, tc := range []struct {
		version string
		want    string
	}{
		{"2.23.0", "antigravity/hub/2.23.0 (aidev_client; os_type=darwin; arch=arm64; cl=998057262)"},
		{"2.24.0", "antigravity/hub/2.24.0 (aidev_client; os_type=darwin; arch=arm64; cl=1000335682)"},
		{"2.25.0", "antigravity/hub/2.25.0 (aidev_client; os_type=darwin; arch=arm64)"},
	} {
		t.Run(tc.version, func(t *testing.T) {
			restore := overrideAntigravityVersionCacheForTest(t, tc.version, time.Now().Add(time.Hour))
			defer restore()
			if got := AntigravityUserAgent(); got != tc.want {
				t.Fatalf("AntigravityUserAgent() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAntigravityFallbackVersionHasMatchingChangelist(t *testing.T) {
	if antigravityHubChangelists[antigravityFallbackVersion] == "" {
		t.Fatal("fallback version must be updated together with its verified changelist")
	}
}

func TestAntigravityVersionFromUserAgentParsesHubFamily(t *testing.T) {
	for _, userAgent := range []string{
		"antigravity/hub/2.24.0 darwin/arm64",
		"antigravity/hub/2.24.0 (aidev_client; os_type=darwin; arch=arm64; cl=1000335682)",
		"antigravity/hub/2.24.0 (aidev_client; os_type=darwin; arch=arm64; cl=1000335682) google-api-nodejs-client/10.3.0",
	} {
		if got := AntigravityVersionFromUserAgent(userAgent); got != "2.24.0" {
			t.Fatalf("AntigravityVersionFromUserAgent(%q) = %q, want %q", userAgent, got, "2.24.0")
		}
	}
}

func TestAntigravityVersionFromUserAgentParsesLegacyFamily(t *testing.T) {
	if got := AntigravityVersionFromUserAgent("antigravity/1.23.2 windows/amd64"); got != "1.23.2" {
		t.Fatalf("AntigravityVersionFromUserAgent() = %q, want %q", got, "1.23.2")
	}
}

func TestAntigravityLoadCodeAssistUserAgentUsesShortUA(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "2.2.1", time.Now().Add(time.Hour))
	defer restore()

	want := "antigravity/hub/2.2.1 (aidev_client; os_type=darwin; arch=arm64)"
	if got := AntigravityLoadCodeAssistUserAgent(""); got != want {
		t.Fatalf("AntigravityLoadCodeAssistUserAgent() = %q, want %q", got, want)
	}
	if got := AntigravityLoadCodeAssistUserAgent(want); got != want {
		t.Fatalf("AntigravityLoadCodeAssistUserAgent(configured) = %q, want %q", got, want)
	}
}

func TestAntigravityOnboardUserUserAgentUsesLongUA(t *testing.T) {
	restore := overrideAntigravityVersionCacheForTest(t, "2.2.1", time.Now().Add(time.Hour))
	defer restore()

	want := "antigravity/hub/2.2.1 (aidev_client; os_type=darwin; arch=arm64) google-api-nodejs-client/10.3.0"
	if got := AntigravityOnboardUserUserAgent(""); got != want {
		t.Fatalf("AntigravityOnboardUserUserAgent() = %q, want %q", got, want)
	}
}

func TestFetchAntigravityLatestVersionUsesHubManifest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/hub/latest-arm64-mac.yml":
			if got := r.Header.Get("User-Agent"); got != "electron-builder" {
				t.Errorf("hub manifest User-Agent = %q, want %q", got, "electron-builder")
			}
			if got := r.Header.Get("Cache-Control"); got != "no-cache" {
				t.Errorf("hub manifest Cache-Control = %q, want %q", got, "no-cache")
			}
			w.Header().Set("Content-Type", "application/yaml")
			_, _ = w.Write([]byte("version: 2.2.1\npath: Antigravity-arm64-mac.zip\n"))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	restore := overrideAntigravityVersionURLsForTest(t, server.URL+"/hub/latest-arm64-mac.yml")
	defer restore()

	version, errFetch := fetchAntigravityLatestVersion(context.Background())
	if errFetch != nil {
		t.Fatalf("fetchAntigravityLatestVersion() error = %v", errFetch)
	}
	if version != "2.2.1" {
		t.Fatalf("fetchAntigravityLatestVersion() = %q, want %q", version, "2.2.1")
	}
}

func TestFetchAntigravityLatestVersionReturnsHubManifestError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "temporary outage", http.StatusInternalServerError)
	}))
	defer server.Close()

	restore := overrideAntigravityVersionURLsForTest(t, server.URL+"/hub/latest-arm64-mac.yml")
	defer restore()

	_, errFetch := fetchAntigravityLatestVersion(context.Background())
	if errFetch == nil {
		t.Fatal("fetchAntigravityLatestVersion() error = nil, want error")
	}
}

func TestAntigravityVersionUpdaterStartsOnFirstRead(t *testing.T) {
	var requests atomic.Int32
	requested := make(chan struct{}, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		select {
		case requested <- struct{}{}:
		default:
		}
		_, _ = w.Write([]byte("version: 2.9.2\n"))
	}))
	defer server.Close()
	restoreURL := overrideAntigravityVersionURLsForTest(t, server.URL)
	defer restoreURL()
	restoreVersion := overrideAntigravityVersionCacheForTest(t, "", time.Time{})
	defer restoreVersion()

	antigravityVersionMu.Lock()
	oldContext, oldStarted := antigravityUpdaterContext, antigravityUpdaterStarted
	antigravityUpdaterContext, antigravityUpdaterStarted = nil, false
	antigravityVersionMu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		antigravityVersionMu.Lock()
		antigravityUpdaterContext, antigravityUpdaterStarted = oldContext, oldStarted
		antigravityVersionMu.Unlock()
	}()

	ArmAntigravityVersionUpdater(ctx)
	antigravityVersionMu.RLock()
	started := antigravityUpdaterStarted
	antigravityVersionMu.RUnlock()
	if started {
		t.Fatalf("antigravityUpdaterStarted after arming = %t, want false", started)
	}
	select {
	case <-requested:
		t.Fatalf("requests after arming = %d, want 0", requests.Load())
	case <-time.After(50 * time.Millisecond):
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests after arming = %d, want 0", got)
	}

	AntigravityLatestVersion()
	select {
	case <-requested:
	case <-t.Context().Done():
		t.Fatal("requests after first read = 0, want 1")
	}
	// Wait for the cache write before restoring package state.
	for AntigravityLatestVersion() != "2.9.2" {
		select {
		case <-t.Context().Done():
			t.Fatalf("AntigravityLatestVersion() = %q, want %q", AntigravityLatestVersion(), "2.9.2")
		default:
			runtime.Gosched()
		}
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests after first read = %d, want 1", got)
	}
	ArmAntigravityVersionUpdater(ctx)
	antigravityVersionMu.RLock()
	started = antigravityUpdaterStarted
	antigravityVersionMu.RUnlock()
	if !started {
		t.Fatalf("antigravityUpdaterStarted after rearming = %t, want true", started)
	}
	AntigravityLatestVersion()
	select {
	case <-requested:
		t.Fatalf("requests after another arm and read = %d, want 1", requests.Load())
	case <-time.After(50 * time.Millisecond):
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("requests after another arm and read = %d, want 1", got)
	}
}

func TestAntigravityLatestVersionUnarmedReadMakesNoRequest(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		_, _ = w.Write([]byte("version: 2.9.2\n"))
	}))
	defer server.Close()
	restoreURL := overrideAntigravityVersionURLsForTest(t, server.URL)
	defer restoreURL()

	antigravityVersionMu.Lock()
	oldContext, oldStarted := antigravityUpdaterContext, antigravityUpdaterStarted
	antigravityUpdaterContext, antigravityUpdaterStarted = nil, false
	antigravityVersionMu.Unlock()
	defer func() {
		antigravityVersionMu.Lock()
		antigravityUpdaterContext, antigravityUpdaterStarted = oldContext, oldStarted
		antigravityVersionMu.Unlock()
	}()

	for _, tc := range []struct {
		name    string
		version string
		expiry  time.Time
		want    string
	}{
		{name: "fallback", want: antigravityFallbackVersion},
		{name: "cached", version: "2.9.0", expiry: time.Now().Add(time.Hour), want: "2.9.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			restoreVersion := overrideAntigravityVersionCacheForTest(t, tc.version, tc.expiry)
			defer restoreVersion()
			if got := AntigravityLatestVersion(); got != tc.want {
				t.Fatalf("unarmed AntigravityLatestVersion() = %q, want %q", got, tc.want)
			}
			antigravityVersionMu.RLock()
			started := antigravityUpdaterStarted
			antigravityVersionMu.RUnlock()
			if started {
				t.Fatalf("antigravityUpdaterStarted after unarmed read = %t, want false", started)
			}
		})
	}
	<-time.After(50 * time.Millisecond)
	if got := requests.Load(); got != 0 {
		t.Fatalf("requests after unarmed reads = %d, want 0", got)
	}
}
