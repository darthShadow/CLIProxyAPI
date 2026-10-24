//go:build wiregate

package helps

import (
	"context"
	"crypto/x509"
	"fmt"
	"net"

	tls "github.com/refraction-networking/utls"
)

// A wiregate build (go test -tags wiregate) replaces the Claude Code TLS
// handshake with a clear-text pass-through, so a loopback CONNECT gate set as
// proxy-url sees the bytes the real executor, header casing, and ordered writer
// produce. Production builds never include this file.
func init() {
	claudeCodeWrapConn = wireGatePassThrough
}

// wireGatePassThrough refuses any remote that is not loopback before a byte is
// written, so a wiregate build cannot send clear text to a real upstream.
func wireGatePassThrough(_ context.Context, conn net.Conn, _ string, _ tls.ClientSessionCache, _ *x509.CertPool) (net.Conn, error) {
	if tcpAddr, ok := conn.RemoteAddr().(*net.TCPAddr); !ok || !tcpAddr.IP.IsLoopback() {
		_ = conn.Close()
		return nil, fmt.Errorf("claude tls: wiregate refuses non-loopback remote %v", conn.RemoteAddr())
	}
	return conn, nil
}
