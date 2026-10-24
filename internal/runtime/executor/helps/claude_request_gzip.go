package helps

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"math/rand/v2"
	"net/http"
	"regexp"
	"strings"
	"sync/atomic"
	"unicode"
)

var claudeGzipRejected atomic.Bool

var claudeGzipPadding = func() []byte {
	padding := make([]byte, 1+rand.IntN(257))
	padding[0] = '\n'
	for i := 1; i < len(padding); i++ {
		padding[i] = " \t"[rand.IntN(2)]
	}
	return padding
}

var claudeGzipBadJSON = regexp.MustCompile(`^bad json: (?:invalid character |unexpected EOF$|unexpected end of JSON input$|EOF$)`)

// ApplyClaudeRequestGzip frames eligible official-origin POST bodies as gzip.
// Bodies that cannot be replayed or read remain unchanged.
func ApplyClaudeRequestGzip(req *http.Request, resetCCH func([]byte) []byte) {
	if req == nil || req.Method != http.MethodPost || !IsAnthropicUpstreamURL(req.URL) ||
		req.GetBody == nil || req.Header.Get("Content-Encoding") != "" || claudeGzipRejected.Load() {
		return
	}
	reader, err := req.GetBody()
	if err != nil {
		return
	}
	body, errRead := io.ReadAll(reader)
	errClose := reader.Close()
	if errRead != nil || errClose != nil {
		return
	}
	units := 0
	for _, r := range string(body) {
		units++
		if r >= 0x10000 {
			units++
		}
	}
	if units < 4096 {
		return
	}
	padded := append(resetCCH(body), claudeGzipPadding()...)
	var encoded bytes.Buffer
	writer := gzip.NewWriter(&encoded)
	_, errWrite := writer.Write(padded)
	errClose = writer.Close()
	if errWrite != nil || errClose != nil {
		return
	}
	compressed := encoded.Bytes()
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(compressed)), nil
	}
	req.Body = io.NopCloser(bytes.NewReader(compressed))
	req.ContentLength = int64(len(compressed))
	req.Header.Set("Content-Encoding", "gzip")
}

// NoteClaudeGzipRejection disables later gzip preparation after an origin-marked
// invalid-JSON 400 on a gzip request. It never resends or changes the response.
func NoteClaudeGzipRejection(req *http.Request, status int, header http.Header, body []byte) {
	if req == nil || !IsAnthropicUpstreamURL(req.URL) || req.Header.Get("Content-Encoding") != "gzip" || status != 400 {
		return
	}
	if !strings.HasPrefix(header.Get("request-id"), "req_") && len(header.Values("cf-ray")) == 0 {
		return
	}
	body = body[:min(len(body), 8192)]
	var envelope map[string]json.RawMessage
	var rejected bool
	if !json.Valid(body) {
		text := strings.TrimRightFunc(string(body), func(r rune) bool { return unicode.IsSpace(r) || r == 0xFEFF })
		rejected = claudeGzipBadJSON.MatchString(text)
	} else if json.Unmarshal(body, &envelope) == nil {
		var detail map[string]json.RawMessage
		if json.Unmarshal(envelope["error"], &detail) == nil {
			var message string
			rejected = json.Unmarshal(detail["message"], &message) == nil &&
				(strings.HasPrefix(message, "The request body is not valid JSON") ||
					strings.HasPrefix(message, "Failed to parse request: could not parse request body as JSON"))
		} else {
			var text, reason string
			rejected = json.Unmarshal(envelope["reason"], &reason) == nil && reason == "bad_json" &&
				json.Unmarshal(envelope["error"], &text) == nil && claudeGzipBadJSON.MatchString(text)
		}
	}
	if rejected {
		claudeGzipRejected.Store(true)
	}
}

// ResetClaudeGzipLatchForTest resets process state for isolated tests.
func ResetClaudeGzipLatchForTest() {
	claudeGzipRejected.Store(false)
}
