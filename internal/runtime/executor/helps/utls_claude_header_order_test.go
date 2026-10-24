package helps

import (
	"bufio"
	"net"
	"net/http"
	"reflect"
	"strings"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/httpwire"
)

func TestClaudeCodeHeaderOrderPlacesContentEncoding(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		t.Run(path, func(t *testing.T) {
			want := []string{"x-app", "x-client-request-id", "Content-Encoding", "Connection", "Host", "Accept-Encoding", "Content-Length"}
			if got := claudeHeaderTailOnPipe(t, path, false); !reflect.DeepEqual(got, want) {
				t.Fatalf("header tail = %v, want %v", got, want)
			}
		})
	}
}

func TestClaudeCodeHeaderOrderLowercasesHintHeaders(t *testing.T) {
	for _, path := range []string{"/v1/messages", "/v1/messages/count_tokens"} {
		t.Run(path, func(t *testing.T) {
			want := []string{"x-app", "x-claude-code-prompt-id", "x-claude-code-request-class", "x-client-request-id", "Content-Encoding", "Connection", "Host", "Accept-Encoding", "Content-Length"}
			if got := claudeHeaderTailOnPipe(t, path, true); !reflect.DeepEqual(got, want) {
				t.Fatalf("header tail = %v, want %v", got, want)
			}
		})
	}
}

func claudeHeaderTailOnPipe(t *testing.T, path string, hints bool) []string {
	t.Helper()
	client, server := net.Pipe()
	t.Cleanup(func() { _ = client.Close(); _ = server.Close() })
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com"+path, strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"x-app": "cli", "x-client-request-id": "offline", "Content-Encoding": "gzip", "Connection": "keep-alive", "Accept-Encoding": "gzip"} {
		req.Header.Set(name, value)
	}
	if hints {
		req.Header.Set("X-Claude-Code-Prompt-Id", "offline")
		req.Header.Set("X-Claude-Code-Request-Class", "main")
	}
	done := make(chan error, 1)
	go func() {
		done <- req.Write(httpwire.NewOrderedRequestConn(client, claudeCodeRequestHeaderOrder))
		_ = client.Close()
	}()
	scanner := bufio.NewScanner(server)
	var tail []string
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			break
		}
		name, _, found := strings.Cut(line, ":")
		if found && (len(tail) > 0 || name == "x-app") {
			tail = append(tail, name)
		}
	}
	// Drain the body before joining the writer, so net.Pipe cannot block on it.
	for scanner.Scan() {
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	return tail
}
