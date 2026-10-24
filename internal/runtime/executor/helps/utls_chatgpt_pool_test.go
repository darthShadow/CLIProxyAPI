package helps

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

func newChatGPTPoolFixture(t *testing.T, serve func(net.Conn, int)) (*utlsRoundTripper, *atomic.Int32) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"example.com"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(leaf)
	listener, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}, NextProtos: []string{"h2"}})
	if err != nil {
		t.Fatal(err)
	}
	var dials atomic.Int32
	var mu sync.Mutex
	var conns []net.Conn
	acceptDone := make(chan struct{})
	var workers sync.WaitGroup
	workers.Add(1)
	go func() {
		defer workers.Done()
		defer close(acceptDone)
		for index := 1; ; index++ {
			conn, errAccept := listener.Accept()
			if errAccept != nil {
				return
			}
			mu.Lock()
			conns = append(conns, conn)
			mu.Unlock()
			workers.Add(1)
			go func(index int) {
				defer workers.Done()
				defer conn.Close()
				if errHandshake := conn.(*tls.Conn).Handshake(); errHandshake != nil {
					t.Error(errHandshake)
					return
				}
				serve(conn, index)
			}(index)
		}
	}()
	rt := &utlsRoundTripper{roots: roots, dialer: contextDialerFunc(func(ctx context.Context, _, _ string) (net.Conn, error) {
		dials.Add(1)
		return (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
	})}
	t.Cleanup(func() {
		rt.CloseIdleConnections()
		_ = listener.Close()
		<-acceptDone
		mu.Lock()
		for _, conn := range conns {
			_ = conn.Close()
		}
		mu.Unlock()
		workers.Wait()
	})
	return rt, &dials
}

type chatGPTPoolResult struct {
	status int
	body   string
	err    error
}

func chatGPTPoolRequest(rt *utlsRoundTripper, ctx context.Context, body string) chatGPTPoolResult {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://example.com/backend-api/codex/responses", bytes.NewReader([]byte(body)))
	if err != nil {
		return chatGPTPoolResult{err: err}
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		return chatGPTPoolResult{err: err}
	}
	data, errRead := io.ReadAll(resp.Body)
	return chatGPTPoolResult{status: resp.StatusCode, body: string(data), err: errors.Join(errRead, resp.Body.Close())}
}

func requireChatGPTPoolOK(t *testing.T, result chatGPTPoolResult) {
	t.Helper()
	if result.err != nil || result.status != http.StatusOK || result.body != "ok" {
		t.Fatalf("status = %d, body = %q, error = %v, want 200, ok, nil", result.status, result.body, result.err)
	}
}

func TestUtlsRoundTripperReusesOneHTTP2Connection(t *testing.T) {
	rt, dials := newChatGPTPoolFixture(t, func(conn net.Conn, _ int) {
		(&http2.Server{}).ServeConn(conn, &http2.ServeConnOpts{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = io.Copy(io.Discard, r.Body)
			_, _ = io.WriteString(w, "ok")
		})})
	})
	for range 5 {
		requireChatGPTPoolOK(t, chatGPTPoolRequest(rt, t.Context(), "request"))
	}
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials = %d, want 1 for 5 sequential requests", got)
	}
}

type chatGPTFrameScript struct {
	mode     string
	third    atomic.Int32
	canceled atomic.Bool
	bodies   chan string
}

// newChatGPTFrameScript shares the frame protocol for refusal, GOAWAY and reset tests.
func newChatGPTFrameScript(t *testing.T, mode string) (*utlsRoundTripper, *atomic.Int32, *chatGPTFrameScript) {
	t.Helper()
	script := &chatGPTFrameScript{mode: mode, bodies: make(chan string, 8)}
	rt, dials := newChatGPTPoolFixture(t, func(conn net.Conn, index int) {
		preface := make([]byte, len(http2.ClientPreface))
		if _, err := io.ReadFull(conn, preface); err != nil {
			return
		}
		if string(preface) != http2.ClientPreface {
			t.Error("invalid HTTP/2 preface")
			return
		}
		framer := http2.NewFramer(conn, conn)
		if err := framer.WriteSettings(); err != nil {
			t.Error(err)
			return
		}
		bodies := make(map[uint32][]byte)
		for {
			frame, err := framer.ReadFrame()
			if err != nil {
				return
			}
			var stream uint32
			switch f := frame.(type) {
			case *http2.SettingsFrame:
				if !f.IsAck() {
					err = framer.WriteSettingsAck()
				}
			case *http2.PingFrame:
				if !f.IsAck() {
					err = framer.WritePing(true, f.Data)
				}
			case *http2.HeadersFrame:
				if index == 1 && f.StreamID == 3 {
					script.third.Add(1)
					if script.mode == "goaway" {
						err = framer.WriteGoAway(1, http2.ErrCodeNo, nil)
					} else if script.mode == "canceled" {
						script.canceled.Store(true)
						err = framer.WriteRSTStream(3, http2.ErrCodeRefusedStream)
					}
				}
			case *http2.DataFrame:
				bodies[f.StreamID] = append(bodies[f.StreamID], f.Data()...)
				if f.StreamEnded() {
					stream = f.StreamID
				}
			}
			if err != nil {
				t.Error(err)
				return
			}
			if stream == 0 {
				continue
			}
			script.bodies <- string(bodies[stream])
			if index == 1 && stream == 3 {
				switch script.mode {
				case "refused", "processed":
					code := http2.ErrCodeRefusedStream
					if script.mode == "processed" {
						code = http2.ErrCodeInternal
					}
					err = framer.WriteRSTStream(stream, code)
				case "goaway", "canceled":
					continue
				}
			} else {
				var block bytes.Buffer
				err = hpack.NewEncoder(&block).WriteField(hpack.HeaderField{Name: ":status", Value: "200"})
				if err == nil {
					err = framer.WriteHeaders(http2.HeadersFrameParam{StreamID: stream, BlockFragment: block.Bytes(), EndHeaders: true})
				}
				if err == nil {
					err = framer.WriteData(stream, true, []byte("ok"))
				}
			}
			if err != nil {
				t.Error(err)
				return
			}
		}
	})
	return rt, dials, script
}

func TestUtlsRoundTripperResendsRefusedStreamOnce(t *testing.T) {
	rt, dials, script := newChatGPTFrameScript(t, "refused")
	requireChatGPTPoolOK(t, chatGPTPoolRequest(rt, t.Context(), "first"))
	requireChatGPTPoolOK(t, chatGPTPoolRequest(rt, t.Context(), "second"))
	var secondBodies []string
	for len(script.bodies) > 0 {
		if body := <-script.bodies; body == "second" {
			secondBodies = append(secondBodies, body)
		}
	}
	if script.third.Load() != 1 || dials.Load() != 2 || len(secondBodies) != 2 || secondBodies[0] != secondBodies[1] {
		t.Fatalf("connection 1 stream 3 headers = %d, dials = %d, request 2 bodies = %q, want 1, 2, [second second]", script.third.Load(), dials.Load(), secondBodies)
	}
}

func TestUtlsRoundTripperResendsAfterGracefulGoAway(t *testing.T) {
	rt, dials, script := newChatGPTFrameScript(t, "goaway")
	requireChatGPTPoolOK(t, chatGPTPoolRequest(rt, t.Context(), "first"))
	requireChatGPTPoolOK(t, chatGPTPoolRequest(rt, t.Context(), "second"))
	if script.third.Load() != 1 || dials.Load() != 2 {
		t.Fatalf("connection 1 stream 3 headers = %d, dials = %d, want 1, 2", script.third.Load(), dials.Load())
	}
}

func TestUtlsRoundTripperDoesNotResendProcessedStream(t *testing.T) {
	rt, dials, script := newChatGPTFrameScript(t, "processed")
	requireChatGPTPoolOK(t, chatGPTPoolRequest(rt, t.Context(), "first"))
	result := chatGPTPoolRequest(rt, t.Context(), "second")
	var streamErr http2.StreamError
	if !errors.As(result.err, &streamErr) || streamErr.Code != http2.ErrCodeInternal || dials.Load() != 1 {
		t.Fatalf("request 2 status = %d, error = %v, dials = %d, want INTERNAL_ERROR, 1 dial", result.status, result.err, dials.Load())
	}
	count := 0
	for len(script.bodies) > 0 {
		if <-script.bodies == "second" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("request 2 body count = %d, want 1", count)
	}
}

type chatGPTCanceledContext struct {
	context.Context
	canceled *atomic.Bool
}

func (ctx chatGPTCanceledContext) Err() error {
	if ctx.canceled.Load() {
		return context.Canceled
	}
	return nil
}

func TestUtlsRoundTripperCanceledRequestIsNotResent(t *testing.T) {
	rt, dials, script := newChatGPTFrameScript(t, "canceled")
	requireChatGPTPoolOK(t, chatGPTPoolRequest(rt, t.Context(), "first"))
	ctx := chatGPTCanceledContext{Context: context.Background(), canceled: &script.canceled}
	result := chatGPTPoolRequest(rt, ctx, "second")
	var streamErr http2.StreamError
	if !errors.As(result.err, &streamErr) || streamErr.Code != http2.ErrCodeRefusedStream || dials.Load() != 1 || script.third.Load() != 1 {
		t.Fatalf("request 2 status = %d, error = %v, dials = %d, stream 3 headers = %d, want REFUSED_STREAM, 1 dial, 1 headers", result.status, result.err, dials.Load(), script.third.Load())
	}
}

func TestUtlsRoundTripperCloseIdleKeepsStreamsInFlight(t *testing.T) {
	held, release := make(chan struct{}), make(chan struct{})
	rt, dials := newChatGPTPoolFixture(t, func(conn net.Conn, _ int) {
		(&http2.Server{}).ServeConn(conn, &http2.ServeConnOpts{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			if string(body) == "first" {
				close(held)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			_, _ = io.WriteString(w, "ok")
		})})
	})
	first := make(chan chatGPTPoolResult, 1)
	go func() { first <- chatGPTPoolRequest(rt, t.Context(), "first") }()
	<-held
	requireChatGPTPoolOK(t, chatGPTPoolRequest(rt, t.Context(), "second"))
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials while first stream held = %d, want 1", got)
	}
	rt.CloseIdleConnections()
	close(release)
	requireChatGPTPoolOK(t, <-first)
	requireChatGPTPoolOK(t, chatGPTPoolRequest(rt, t.Context(), "third"))
	if got := dials.Load(); got != 2 {
		t.Fatalf("dials after CloseIdleConnections = %d, want 2", got)
	}
}

func TestNewUtlsHTTPClientCachesChatGPTRoundTripper(t *testing.T) {
	chrome := func(proxyURL string) http.RoundTripper {
		client := NewUtlsHTTPClient(context.Background(), nil, &cliproxyauth.Auth{ProxyURL: proxyURL}, 0)
		rt, ok := client.Transport.(*fallbackRoundTripper)
		if !ok {
			t.Fatalf("transport = %T, want *fallbackRoundTripper", client.Transport)
		}
		return rt.chrome
	}
	first, second, third := chrome("http://127.0.0.1:9"), chrome("http://127.0.0.1:9"), chrome("http://127.0.0.1:10")
	if first != second {
		t.Fatalf("same proxy chrome round trippers = %p and %p, want identical", first, second)
	}
	if first == third {
		t.Fatal("different proxies share a chrome round tripper")
	}
}

func TestUtlsRoundTripperCanceledFirstRequestKeepsSiblingStream(t *testing.T) {
	firstHeld, secondHeld, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	rt, dials := newChatGPTPoolFixture(t, func(conn net.Conn, _ int) {
		(&http2.Server{}).ServeConn(conn, &http2.ServeConnOpts{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			switch string(body) {
			case "first":
				close(firstHeld)
				<-r.Context().Done()
				return
			case "second":
				close(secondHeld)
				select {
				case <-release:
				case <-r.Context().Done():
					return
				}
			}
			_, _ = io.WriteString(w, "ok")
		})})
	})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	first, second := make(chan chatGPTPoolResult, 1), make(chan chatGPTPoolResult, 1)
	go func() { first <- chatGPTPoolRequest(rt, ctx, "first") }()
	<-firstHeld
	go func() { second <- chatGPTPoolRequest(rt, t.Context(), "second") }()
	<-secondHeld
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials while sibling stream held = %d, want 1", got)
	}
	cancel()
	if result := <-first; !errors.Is(result.err, context.Canceled) {
		t.Fatalf("first request error = %v, want context.Canceled", result.err)
	}
	close(release)
	requireChatGPTPoolOK(t, <-second)
	if got := dials.Load(); got != 1 {
		t.Fatalf("dials after sibling completed = %d, want 1", got)
	}
	requireChatGPTPoolOK(t, chatGPTPoolRequest(rt, t.Context(), "third"))
	if got := dials.Load(); got != 2 {
		t.Fatalf("dials after first connection retired = %d, want 2", got)
	}
}
