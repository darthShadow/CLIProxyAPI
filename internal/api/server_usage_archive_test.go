package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
)

func usageServer(t *testing.T, enabled, home bool) *Server {
	t.Helper()
	gin.SetMode(gin.TestMode)
	t.Setenv("CPA_USAGE_DIR", t.TempDir())
	cfg := &config.Config{}
	cfg.Home.Enabled = home
	cfg.RemoteManagement.SecretKey = "sk-fake-proxy-0000"
	cfg.RemoteManagement.DisableControlPanel = true
	h := management.NewHandler(cfg, "", nil)
	h.SetLocalPassword("sk-fake-proxy-0000")
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	s.managementRoutesEnabled.Store(enabled)
	s.registerManagementRoutes()
	return s
}
func usageRequest(s *Server, method, path string, key bool) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, path, nil)
	r.RemoteAddr = "127.0.0.1:1234"
	if key {
		r.Header.Set("Authorization", "Bearer sk-fake-proxy-0000")
	}
	w := httptest.NewRecorder()
	s.engine.ServeHTTP(w, r)
	return w
}
func TestUsageArchiveRoutesShareManagementAccess(t *testing.T) {
	for _, tc := range []struct {
		name               string
		enabled, home, key bool
		want               int
	}{{"authorized", true, false, true, 200}, {"missing key", true, false, false, 401}, {"disabled", false, false, true, 404}, {"home", true, true, true, 404}} {
		t.Run(tc.name, func(t *testing.T) {
			s := usageServer(t, tc.enabled, tc.home)
			for _, route := range []struct{ method, path string }{{"GET", "/v8/management/observability/usage/archive"}, {"POST", "/v8/management/observability/usage/accounts"}} {
				w := usageRequest(s, route.method, route.path, tc.key)
				if w.Code != tc.want {
					t.Errorf("%s %s status=%d want=%d", route.method, route.path, w.Code, tc.want)
				}
			}
			w := usageRequest(s, "GET", "/v8/management/observability/usage/accounts", true)
			if w.Code != 404 {
				t.Errorf("accounts GET routed: %d", w.Code)
			}
		})
	}
}

const archiveCSP = "default-src 'none'; script-src 'self'; style-src 'unsafe-inline'; connect-src 'self'; base-uri 'none'; form-action 'none'; frame-ancestors 'none'"

func checkUsageHeaders(t *testing.T, w *httptest.ResponseRecorder, content string) {
	t.Helper()
	for key, want := range map[string]string{"Content-Security-Policy": archiveCSP, "X-Content-Type-Options": "nosniff", "Referrer-Policy": "no-referrer", "Cache-Control": "no-store", "Content-Type": content} {
		if got := w.Header().Get(key); got != want {
			t.Errorf("%s=%q want=%q", key, got, want)
		}
	}
}
func TestUsageArchivePageHeadersAndNoData(t *testing.T) {
	s := usageServer(t, true, false)
	w := usageRequest(s, "GET", "/usage-archive.html", false)
	if w.Code != 200 {
		t.Fatalf("page status=%d want=200", w.Code)
	}
	checkUsageHeaders(t, w, "text/html; charset=utf-8")
	body := w.Body.String()
	for _, text := range []string{"Check Plans and Codex Resets", "Open the Management Page", "type=\"password\"", `id="panel-overview"`, `id="panel-quota"`, `id="panel-accounts"`, `id="panel-daily"`} {
		if !strings.Contains(body, text) {
			t.Errorf("missing %q", text)
		}
	}
	for _, text := range []string{"sk-fake-proxy-0000", "a@example.test", "fake-access-token"} {
		if strings.Contains(body, text) {
			t.Errorf("data in static page: %q", text)
		}
	}
	for _, state := range []struct{ enabled, home bool }{{false, false}, {true, true}} {
		s = usageServer(t, state.enabled, state.home)
		for _, path := range []string{"/usage-archive.html", "/usage-archive.js"} {
			if w := usageRequest(s, "GET", path, false); w.Code != 404 {
				t.Errorf("disabled/home asset %s status=%d", path, w.Code)
			}
		}
	}
}
func TestUsageArchiveScriptServed(t *testing.T) {
	w := usageRequest(usageServer(t, true, false), http.MethodGet, "/usage-archive.js", false)
	if w.Code != 200 {
		t.Fatalf("script status=%d want=200", w.Code)
	}
	checkUsageHeaders(t, w, "text/javascript; charset=utf-8")
	want, err := os.ReadFile("handlers/management/usage_archive/usage-archive.js")
	if err != nil {
		t.Fatalf("read script: %v", err)
	}
	if len(want) == 0 || w.Body.String() != string(want) {
		t.Errorf("served script differs from usage-archive.js: got %d bytes, want %d", w.Body.Len(), len(want))
	}
}
