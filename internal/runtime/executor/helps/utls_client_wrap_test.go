//go:build !wiregate

package helps

import (
	"context"
	"io"
	"net"
	"testing"
	"time"

	tls "github.com/refraction-networking/utls"
)

// TestClaudeCodeWrapConnDefaultPerformsTLSHandshake shows that a build without
// the wiregate tag still opens every Claude upstream connection with a TLS
// handshake record.
func TestClaudeCodeWrapConnDefaultPerformsTLSHandshake(t *testing.T) {
	client, server := net.Pipe()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, errWrap := claudeCodeWrapConn(ctx, client, "api.anthropic.com", tls.NewLRUClientSessionCache(1))
		done <- errWrap
	}()
	if errDeadline := server.SetReadDeadline(time.Now().Add(5 * time.Second)); errDeadline != nil {
		t.Fatalf("SetReadDeadline() error = %v", errDeadline)
	}
	first := make([]byte, 1)
	if _, errRead := io.ReadFull(server, first); errRead != nil {
		t.Fatalf("read first byte: %v", errRead)
	}
	if first[0] != 0x16 {
		t.Fatalf("first byte = %#x, want 0x16 (TLS handshake record)", first[0])
	}
	_ = server.Close()
	if errWrap := <-done; errWrap == nil {
		t.Fatal("handshake against a closed peer succeeded, want an error")
	}
}
