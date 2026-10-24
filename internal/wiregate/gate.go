// Package wiregate is a test-only loopback CONNECT gate. The wiregate build of
// the Claude transport (internal/runtime/executor/helps/utls_client_wiregate.go)
// sends its request bytes in clear text through the gate, which records the raw
// header block and body and answers with a fixed JSON response. Only tests
// built with -tags wiregate import it.
package wiregate

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Record is one request seen inside a CONNECT tunnel.
type Record struct {
	Target    string
	RawHeader []byte
	Body      []byte
}

func (r Record) lines() []string {
	return strings.Split(string(r.RawHeader), "\r\n")
}

// RequestLine returns the request line, for example "POST /v1/messages HTTP/1.1".
func (r Record) RequestLine() string {
	return r.lines()[0]
}

// Names returns the header names in wire order, spelled as sent.
func (r Record) Names() []string {
	lines := r.lines()
	names := make([]string, 0, len(lines))
	for _, line := range lines[1:] {
		if colon := strings.IndexByte(line, ':'); colon > 0 {
			names = append(names, line[:colon])
		}
	}
	return names
}

// Get returns the first value of the named header, matched case-insensitively.
func (r Record) Get(name string) string {
	for _, line := range r.lines()[1:] {
		if colon := strings.IndexByte(line, ':'); colon > 0 && strings.EqualFold(line[:colon], name) {
			return strings.TrimSpace(line[colon+1:])
		}
	}
	return ""
}

// Gate is a loopback CONNECT recorder.
type Gate struct {
	listener net.Listener
	records  chan Record
	response string
}

// Start listens on 127.0.0.1 and answers every tunneled request with status
// 200 and responseBody. The listener closes when the test ends.
func Start(t testing.TB, responseBody string) *Gate {
	t.Helper()
	listener, errListen := net.Listen("tcp", "127.0.0.1:0")
	if errListen != nil {
		t.Fatalf("wiregate: listen: %v", errListen)
	}
	gate := &Gate{listener: listener, records: make(chan Record, 16), response: responseBody}
	t.Cleanup(func() { _ = listener.Close() })
	go gate.serve()
	return gate
}

// ProxyURL is the value for proxy-url.
func (g *Gate) ProxyURL() string {
	return "http://" + g.listener.Addr().String()
}

// Next waits up to five seconds for the next recorded request.
func (g *Gate) Next(t testing.TB) Record {
	t.Helper()
	select {
	case record := <-g.records:
		return record
	case <-time.After(5 * time.Second):
		t.Fatal("wiregate: no request reached the gate")
		return Record{}
	}
}

func (g *Gate) serve() {
	for {
		conn, errAccept := g.listener.Accept()
		if errAccept != nil {
			return
		}
		go g.handle(conn)
	}
}

func (g *Gate) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	reader := bufio.NewReader(conn)
	connect, errConnect := http.ReadRequest(reader)
	if errConnect != nil || connect.Method != http.MethodConnect {
		return
	}
	if _, errWrite := io.WriteString(conn, "HTTP/1.1 200 Connection Established\r\n\r\n"); errWrite != nil {
		return
	}
	var header bytes.Buffer
	for !bytes.HasSuffix(header.Bytes(), []byte("\r\n\r\n")) {
		line, errLine := reader.ReadBytes('\n')
		header.Write(line)
		if errLine != nil {
			return
		}
	}
	record := Record{Target: connect.Host, RawHeader: bytes.TrimSuffix(header.Bytes(), []byte("\r\n\r\n"))}
	if length, errLength := strconv.Atoi(record.Get("Content-Length")); errLength == nil && length > 0 {
		record.Body = make([]byte, length)
		if _, errBody := io.ReadFull(reader, record.Body); errBody != nil {
			return
		}
	}
	g.records <- record
	_, _ = fmt.Fprintf(conn, "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: %d\r\nConnection: close\r\n\r\n%s", len(g.response), g.response)
}
