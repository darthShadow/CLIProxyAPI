package helps

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/klauspost/compress/zstd"
)

func readCodexZstdBody(t *testing.T, body io.ReadCloser) []byte {
	t.Helper()
	data, err := io.ReadAll(body)
	errClose := body.Close()
	if err != nil || errClose != nil {
		t.Fatalf("read/close body: %v / %v", err, errClose)
	}
	return data
}

func TestApplyCodexRequestZstdFrameHeaders(t *testing.T) {
	for _, tc := range []struct {
		name, input, header string
	}{
		{"small", strings.Repeat("x", 2000), "28b52ffd60f5"},
		{"large", strings.Repeat("y", 600000), "28b52ffd0058"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			plain := []byte(`{"model": "gpt-6.1-sol", "input": "` + tc.input + `"}`)
			decoder, err := zstd.NewReader(nil)
			if err != nil {
				t.Fatal(err)
			}
			defer decoder.Close()
			for range 2 {
				req, err := http.NewRequest(http.MethodPost, "https://chatgpt.com/backend-api/codex/responses", bytes.NewReader(plain))
				if err != nil {
					t.Fatal(err)
				}
				ApplyCodexRequestZstd(req)
				encoded := readCodexZstdBody(t, req.Body)
				if got := req.Header.Get("Content-Encoding"); got != "zstd" {
					t.Errorf("Content-Encoding = %q, want zstd (body plain = %t)", got, bytes.Equal(encoded, plain))
					continue
				}
				if len(encoded) < 6 || fmt.Sprintf("%x", encoded[:6]) != tc.header {
					t.Fatalf("frame = %x, want prefix %s", encoded, tc.header)
				}
				if encoded[4]&4 != 0 {
					t.Error("frame checksum flag is set")
				}
				decoded, err := decoder.DecodeAll(encoded, nil)
				if err != nil || !bytes.Equal(decoded, plain) {
					t.Fatalf("decoded body matches input = %t, error = %v", bytes.Equal(decoded, plain), err)
				}
				if req.ContentLength != int64(len(encoded)) {
					t.Errorf("ContentLength = %d, want %d", req.ContentLength, len(encoded))
				}
				for range 2 {
					replay, err := req.GetBody()
					if err != nil {
						t.Fatal(err)
					}
					if !bytes.Equal(readCodexZstdBody(t, replay), encoded) {
						t.Error("GetBody differs from encoded body")
					}
				}
			}
		})
	}
}

func TestApplyCodexRequestZstdScope(t *testing.T) {
	for _, tc := range []struct {
		name, method, url, encoding string
		wantZstd, noReplay          bool
	}{
		{"control", "POST", "https://chatgpt.com/backend-api/codex/responses", "", true, false},
		{"mixed-case host", "POST", "https://ChAtGpT.CoM:443/responses", "", true, false},
		{"http", "POST", "http://chatgpt.com/responses", "", false, false},
		{"other host", "POST", "https://api.openai.com/v1/responses", "", false, false},
		{"compact", "POST", "https://chatgpt.com/responses/compact", "", false, false},
		{"get", "GET", "https://chatgpt.com/responses", "", false, false},
		{"existing encoding", "POST", "https://chatgpt.com/responses", "gzip", false, false},
		{"no replay", "POST", "https://chatgpt.com/responses", "", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const plain = "0123456789"
			req, err := http.NewRequest(tc.method, tc.url, strings.NewReader(plain))
			if err != nil {
				t.Fatal(err)
			}
			if tc.noReplay {
				req.GetBody = nil
			}
			req.Header.Set("Content-Encoding", tc.encoding)
			req.Header.Set("X-Test-Preserved", "fake-value")
			ApplyCodexRequestZstd(req)
			body := readCodexZstdBody(t, req.Body)
			wantEncoding := tc.encoding
			if tc.wantZstd {
				wantEncoding = "zstd"
				if len(body) <= len(plain) || string(body) == plain {
					t.Errorf("10-byte control stays plain or does not grow: length = %d, body plain = %t", len(body), string(body) == plain)
				}
			} else if string(body) != plain || req.ContentLength != int64(len(plain)) {
				t.Errorf("out-of-scope request changed: body = %q, ContentLength = %d", body, req.ContentLength)
			}
			if got := req.Header.Get("Content-Encoding"); got != wantEncoding {
				t.Errorf("Content-Encoding = %q, want %q", got, wantEncoding)
			}
			if req.Header.Get("X-Test-Preserved") != "fake-value" {
				t.Error("unrelated header changed")
			}
		})
	}
}
