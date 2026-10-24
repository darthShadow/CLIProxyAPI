package helps

import (
	"bytes"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/klauspost/compress/zstd"
)

var codexZstdEncoders sync.Pool

// ApplyCodexRequestZstd frames replayable HTTPS chatgpt.com POST /responses
// bodies as zstd. Other requests and failures leave the request unchanged.
// Body, GetBody, and ContentLength describe the same encoded bytes on success.
func ApplyCodexRequestZstd(req *http.Request) {
	if req == nil || req.URL == nil || req.Method != http.MethodPost ||
		req.URL.Scheme != "https" || !strings.EqualFold(req.URL.Hostname(), "chatgpt.com") ||
		!strings.HasSuffix(req.URL.Path, "/responses") || req.GetBody == nil || req.Header.Get("Content-Encoding") != "" {
		return
	}
	reader, err := req.GetBody()
	if err != nil {
		return
	}
	plain, errRead := io.ReadAll(reader)
	errClose := reader.Close()
	if errRead != nil || errClose != nil {
		return
	}
	writer, _ := codexZstdEncoders.Get().(*zstd.Encoder)
	if writer == nil {
		writer, err = zstd.NewWriter(nil,
			zstd.WithEncoderLevel(zstd.EncoderLevelFromZstd(3)),
			zstd.WithEncoderCRC(false),
			zstd.WithWindowSize(1<<21),
			// One block encoder per pooled Encoder: the default builds GOMAXPROCS of
			// them on the first one-block body, and each request encodes one body.
			zstd.WithEncoderConcurrency(1))
		if err != nil {
			return
		}
	}
	var encoded bytes.Buffer
	writer.Reset(&encoded)
	_, errWrite := writer.Write(plain)
	errClose = writer.Close()
	writer.Reset(nil)
	codexZstdEncoders.Put(writer)
	if errWrite != nil || errClose != nil {
		return
	}
	compressed := encoded.Bytes()
	req.GetBody = func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(compressed)), nil
	}
	req.Body = io.NopCloser(bytes.NewReader(compressed))
	req.ContentLength = int64(len(compressed))
	req.Header.Set("Content-Encoding", "zstd")
}
