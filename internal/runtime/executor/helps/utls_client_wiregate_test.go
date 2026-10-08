//go:build wiregate

package helps

import (
	"context"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/wiregate"
)

// TestWireGateObservesClaudeTransportBytes sends one request through the real
// Claude transport and checks the order and casing classes on the observed
// wire. Builds with wirebad_order or wirebad_casing must fail only their own
// class (step A13 verify).
func TestWireGateObservesClaudeTransportBytes(t *testing.T) {
	gate := wiregate.Start(t, `{"ok":true}`)
	cfg := &config.Config{}
	cfg.ProxyURL = gate.ProxyURL()
	request, errRequest := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages?beta=true", strings.NewReader(`{"model":"m"}`))
	if errRequest != nil {
		t.Fatalf("NewRequest() error = %v", errRequest)
	}
	request.Header.Set("Anthropic-Version", "2023-06-01")
	request.Header.Set("Anthropic-Beta", "claude-code-20250219")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("User-Agent", "claude-cli/2.1.285 (external, cli)")
	response, errDo := NewUtlsHTTPClient(context.Background(), cfg, nil, 10*time.Second).Do(request)
	if errDo != nil {
		t.Fatalf("Do() error = %v", errDo)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()

	record := gate.Next(t)
	if record.Target != "api.anthropic.com:443" {
		t.Fatalf("CONNECT target = %q, want api.anthropic.com:443", record.Target)
	}
	if string(record.Body) != `{"model":"m"}` {
		t.Fatalf("body = %q, want the request body unchanged", record.Body)
	}
	names := record.Names()
	position := map[string]int{}
	for index, name := range names {
		position[strings.ToLower(name)] = index
	}
	for _, name := range []string{"user-agent", "anthropic-beta", "anthropic-version"} {
		if _, ok := position[name]; !ok {
			t.Fatalf("header %s missing from the wire: %v", name, names)
		}
	}
	if position["user-agent"] > position["anthropic-beta"] || position["anthropic-beta"] > position["anthropic-version"] {
		t.Errorf("order class: observed header names %v, want User-Agent, anthropic-beta, anthropic-version in that order", names)
	}
	spelled := false
	for _, name := range names {
		if name == "anthropic-beta" {
			spelled = true
		}
	}
	if !spelled {
		t.Errorf("casing class: observed header names %v, want the spelling anthropic-beta", names)
	}
}

// wireGateCountingConn is a non-loopback connection that counts written bytes.
type wireGateCountingConn struct {
	net.Conn
	written int
	closed  bool
}

func (c *wireGateCountingConn) Write(p []byte) (int, error) {
	c.written += len(p)
	return len(p), nil
}

func (c *wireGateCountingConn) Close() error {
	c.closed = true
	return nil
}

func (c *wireGateCountingConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("203.0.113.7"), Port: 443}
}

func TestWireGateRefusesNonLoopbackRemote(t *testing.T) {
	conn := &wireGateCountingConn{}
	if _, errWrap := claudeCodeWrapConn(context.Background(), conn, "api.anthropic.com", nil); errWrap == nil {
		t.Fatal("claudeCodeWrapConn() error = nil, want a refusal for a non-loopback remote")
	}
	if conn.written != 0 || !conn.closed {
		t.Fatalf("written %d bytes, closed %v; want 0 bytes and a closed connection", conn.written, conn.closed)
	}
}
