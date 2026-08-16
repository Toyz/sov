package gateway

import (
	"encoding/json"
	"io"
	"strings"

	"github.com/Toyz/sov/rpc"
)

// ndjsonContentType is the media type for newline-delimited JSON streams — one
// JSON value per line (W2.7 server streaming).
const ndjsonContentType = "application/x-ndjson"

// ndjsonStream adapts a push-style rpc.StreamProducer to the pull-style
// io.Reader the server writes incrementally. Each yielded value is encoded as
// one line (json.Encoder appends '\n') and flushed to the pipe as produced, so
// memory stays constant regardless of stream length. If the reader end closes
// (client disconnect), the next Encode errors and iteration stops. The server
// closes the returned reader when the response is done.
func ndjsonStream(p rpc.StreamProducer) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		enc := json.NewEncoder(pw)
		var encErr error
		p.ForEach(func(v any) bool {
			if err := enc.Encode(v); err != nil {
				encErr = err
				return false // reader gone or encode failure — stop producing
			}
			return true
		})
		pw.CloseWithError(encErr) // nil on clean completion
	}()
	return pr
}

// isNDJSON reports whether a Content-Type names the NDJSON stream media type,
// tolerating parameters (e.g. "application/x-ndjson; charset=utf-8") and the
// common "application/ndjson" spelling.
func isNDJSON(contentType string) bool {
	ct := strings.ToLower(strings.TrimSpace(contentType))
	if i := strings.IndexByte(ct, ';'); i >= 0 {
		ct = strings.TrimSpace(ct[:i])
	}
	return ct == ndjsonContentType || ct == "application/ndjson"
}
