package helps

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
)

func claudeGzipTestRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "https://api.anthropic.com/v1/messages", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return req
}

func TestApplyClaudeRequestGzipFramesLikeClaudeCode(t *testing.T) {
	ResetClaudeGzipLatchForTest()
	t.Cleanup(ResetClaudeGzipLatchForTest)
	body := `{"system":[{"text":"x-anthropic-billing-header: cch=1a2b3;"}],"prompt":"` + strings.Repeat("x", 5000) + `"}`
	reset := strings.Replace(body, "cch=1a2b3", "cch=00000", 1)
	for _, pinned := range []bool{false, true} {
		t.Run(map[bool]string{false: "random", true: "pinned"}[pinned], func(t *testing.T) {
			if pinned {
				previous := claudeGzipPadding
				t.Cleanup(func() { claudeGzipPadding = previous })
				claudeGzipPadding = func() []byte { return []byte("\n\t ") }
			}
			req := claudeGzipTestRequest(t, body)
			ApplyClaudeRequestGzip(req, func(b []byte) []byte {
				return bytes.Replace(b, []byte("cch=1a2b3"), []byte("cch=00000"), 1)
			})
			if got := req.Header.Get("Content-Encoding"); got != "gzip" {
				t.Fatalf("Content-Encoding = %q, want gzip; body remains plain", got)
			}
			encoded, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(encoded, []byte{0x1f, 0x8b, 8, 0, 0, 0, 0, 0, 0, 0xff}) {
				t.Fatalf("gzip header = %x, want 1f8b08000000000000ff", encoded[:min(10, len(encoded))])
			}
			if req.ContentLength != int64(len(encoded)) || len(req.TransferEncoding) != 0 {
				t.Fatalf("framing = length %d, transfer %v; want %d, no chunking", req.ContentLength, req.TransferEncoding, len(encoded))
			}
			for range 2 {
				replay, err := req.GetBody()
				if err != nil {
					t.Fatal(err)
				}
				got, err := io.ReadAll(replay)
				if errClose := replay.Close(); errClose != nil {
					t.Fatal(errClose)
				}
				if err != nil || !bytes.Equal(got, encoded) {
					t.Fatalf("GetBody replay differs: err %v, equal %v", err, bytes.Equal(got, encoded))
				}
			}
			reader, err := gzip.NewReader(bytes.NewReader(encoded))
			if err != nil {
				t.Fatal(err)
			}
			decoded, err := io.ReadAll(reader)
			if errClose := reader.Close(); errClose != nil {
				t.Fatal(errClose)
			}
			if err != nil || !bytes.HasPrefix(decoded, []byte(reset)) {
				t.Fatalf("decoded body does not preserve reset bytes: %v", err)
			}
			suffix := string(decoded[len(reset):])
			if !regexp.MustCompile(`^\n[ \t]{0,256}$`).MatchString(suffix) || (pinned && suffix != "\n\t ") {
				t.Fatalf("padding = %q, want newline and 0-256 spaces/tabs (pinned %v)", suffix, pinned)
			}
		})
	}
}

func TestApplyClaudeRequestGzipFloorCountsUTF16Units(t *testing.T) {
	ResetClaudeGzipLatchForTest()
	t.Cleanup(ResetClaudeGzipLatchForTest)
	for _, tc := range []struct {
		name, body string
		gzip       bool
	}{
		{"ASCII4095", strings.Repeat("a", 4095), false},
		{"accent4095", strings.Repeat("é", 2000) + strings.Repeat("a", 2095), false},
		{"astral4096", strings.Repeat("😀", 1024) + strings.Repeat("a", 2048), true},
		{"invalidUTF8", strings.Repeat("\xff", 4096), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := claudeGzipTestRequest(t, tc.body)
			ApplyClaudeRequestGzip(req, func(b []byte) []byte { return b })
			if got := req.Header.Get("Content-Encoding") == "gzip"; got != tc.gzip {
				t.Fatalf("gzip = %v, want %v for UTF-16 floor", got, tc.gzip)
			}
		})
	}
}

func TestNoteClaudeGzipRejectionTripsOnlyOnOriginInvalidJSON400(t *testing.T) {
	invalid := `{"error":{"message":"The request body is not valid JSON: gzip"}}`
	for _, tc := range []struct {
		name, body, marker, value, origin, encoding string
		status                                      int
		trip                                        bool
	}{
		{name: "firstPrefix", body: invalid, trip: true},
		{name: "secondPrefix", body: `{"error":{"message":"Failed to parse request: could not parse request body as JSON: gzip"}}`, trip: true},
		{name: "rawTrailingWhitespace", body: "bad json: invalid character 'x' looking for beginning of value \n\t", trip: true},
		{name: "reason", body: `{"error":"bad json: unexpected EOF","reason":"bad_json"}`, trip: true},
		{name: "cfRayOnly", body: invalid, marker: "Cf-Ray", value: "offline", trip: true},
		{name: "emptyCfRay", body: invalid, marker: "Cf-Ray", trip: true},
		{name: "noMarker", body: invalid, marker: "none"},
		{name: "403", body: invalid, status: 403},
		{name: "415", body: invalid, status: 415},
		{name: "ordinary400", body: `{"error":{"message":"invalid max_tokens"}}`},
		{name: "unrecognizedReason", body: `{"error":"x","reason":"bad_json"}`},
		{name: "leadingSpace", body: " bad json: invalid character 'x' looking for beginning of value"},
		{name: "plain", body: invalid, encoding: "plain"},
		{name: "boundedRead", body: strings.Repeat(" ", 9000) + invalid},
		{name: "customOrigin", body: invalid, origin: "gw.example.com"},
		{name: "rawEOF", body: "bad json: EOF\ufeff", trip: true},
		{name: "rawUnexpectedEnd", body: "bad json: unexpected end of JSON input", trip: true},
		{name: "reasonExtraText", body: `{"error":"bad json: unexpected EOF extra","reason":"bad_json"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ResetClaudeGzipLatchForTest()
			t.Cleanup(ResetClaudeGzipLatchForTest)
			req := claudeGzipTestRequest(t, "body")
			if tc.origin != "" {
				req.URL.Host = tc.origin
			}
			if tc.encoding != "plain" {
				req.Header.Set("Content-Encoding", "gzip")
			}
			header := http.Header{}
			if tc.marker == "" {
				header.Set("request-id", "req_offline")
			} else if tc.marker != "none" {
				header.Set(tc.marker, tc.value)
			}
			status := tc.status
			if status == 0 {
				status = 400
			}
			NoteClaudeGzipRejection(req, status, header, []byte(tc.body))
			if got := claudeGzipRejected.Load(); got != tc.trip {
				t.Fatalf("latch = %v, want %v", got, tc.trip)
			}
			if tc.origin != "" {
				control := claudeGzipTestRequest(t, strings.Repeat("a", 4096))
				ApplyClaudeRequestGzip(control, func(b []byte) []byte { return b })
				if got := control.Header.Get("Content-Encoding"); got != "gzip" {
					t.Fatalf("official request after custom rejection encoding = %q, want gzip", got)
				}
			}
		})
	}
}

func TestApplyClaudeRequestGzipScope(t *testing.T) {
	for _, name := range []string{"official", "custom", "GET", "noGetBody", "encoded", "latched"} {
		t.Run(name, func(t *testing.T) {
			ResetClaudeGzipLatchForTest()
			t.Cleanup(ResetClaudeGzipLatchForTest)
			body := strings.Repeat("x", 4096)
			req := claudeGzipTestRequest(t, body)
			switch name {
			case "custom":
				req.URL.Host = "gw.example.com"
			case "GET":
				req.Method = http.MethodGet
			case "noGetBody":
				req.GetBody = nil
			case "encoded":
				req.Header.Set("Content-Encoding", "existing")
			case "latched":
				claudeGzipRejected.Store(true)
			}
			calls := 0
			ApplyClaudeRequestGzip(req, func(b []byte) []byte { calls++; return b })
			want := ""
			if name == "official" {
				want = "gzip"
			} else if name == "encoded" {
				want = "existing"
			}
			if got := req.Header.Get("Content-Encoding"); got != want {
				t.Fatalf("Content-Encoding = %q, want %q", got, want)
			}
			if name != "official" {
				got, err := io.ReadAll(req.Body)
				if err != nil || string(got) != body || calls != 0 || req.ContentLength != int64(len(body)) {
					t.Fatalf("out-of-scope request changed: err %v, reset calls %d, length %d", err, calls, req.ContentLength)
				}
			}
		})
	}
}
