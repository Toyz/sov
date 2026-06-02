package gateway

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A RouteHandler-style streaming Response is copied to the wire verbatim,
// the plugin's Content-Type is kept (not defaulted to JSON), and no
// Content-Length is set (chunked).
func TestNetHTTP_StreamsResponse(t *testing.T) {
	s := NewNetHTTPServer(NetHTTPOptions{})
	s.Handle(func(ctx context.Context, req *Request) *Response {
		return &Response{
			Status: 200,
			Header: Header{"Content-Type": "application/zip"},
			Stream: PipeStream(func(w io.Writer) error {
				for i := 0; i < 3; i++ {
					if _, err := io.WriteString(w, "chunk"); err != nil {
						return err
					}
				}
				return nil
			}),
		}
	})

	rec := httptest.NewRecorder()
	s.serve(rec, httptest.NewRequest("GET", "/dl", nil))

	if rec.Code != 200 {
		t.Fatalf("status=%d", rec.Code)
	}
	if got := rec.Body.String(); got != "chunkchunkchunk" {
		t.Fatalf("body=%q", got)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/zip" {
		t.Fatalf("content-type=%q, want application/zip (must not default to JSON for a stream)", ct)
	}
	if cl := rec.Header().Get("Content-Length"); cl != "" {
		t.Errorf("Content-Length=%q set on a stream; want chunked (empty)", cl)
	}
}

// Stream takes precedence over Body when both are set.
func TestNetHTTP_StreamBeatsBody(t *testing.T) {
	s := NewNetHTTPServer(NetHTTPOptions{})
	s.Handle(func(ctx context.Context, req *Request) *Response {
		return &Response{
			Status: 200,
			Body:   []byte("BUFFERED"),
			Stream: strings.NewReader("STREAMED"),
		}
	})
	rec := httptest.NewRecorder()
	s.serve(rec, httptest.NewRequest("GET", "/x", nil))
	if got := rec.Body.String(); got != "STREAMED" {
		t.Fatalf("body=%q, want STREAMED (Stream must win over Body)", got)
	}
}

// PipeStream surfaces the producer's error to the reader (so the adapter's
// io.Copy returns it) and delivers everything written before the error.
func TestPipeStream_ErrorPropagates(t *testing.T) {
	boom := errors.New("boom")
	r := PipeStream(func(w io.Writer) error {
		_, _ = io.WriteString(w, "partial")
		return boom
	})
	got, err := io.ReadAll(r)
	if string(got) != "partial" {
		t.Fatalf("read %q, want partial", got)
	}
	if !errors.Is(err, boom) {
		t.Fatalf("err=%v, want boom", err)
	}
}

// A panic in the producer is contained: it does not crash the process
// (the goroutine is outside the gateway's recovery middleware), it
// surfaces to the reader as an error, and bytes written before the panic
// are still delivered.
func TestPipeStream_PanicContained(t *testing.T) {
	r := PipeStream(func(w io.Writer) error {
		_, _ = io.WriteString(w, "partial")
		panic("producer exploded")
	})
	got, err := io.ReadAll(r)
	if string(got) != "partial" {
		t.Fatalf("read %q, want partial", got)
	}
	if err == nil {
		t.Fatal("err=nil, want stream-producer-panic error")
	}
	if !strings.Contains(err.Error(), "producer exploded") {
		t.Fatalf("err=%v, want panic value surfaced", err)
	}
}

// Streamed bytes are flushed to the client as produced, not buffered until
// the stream ends — the property MCP SSE depends on. Uses a real server +
// chunked client read: the first frame must arrive while the producer is
// still blocked waiting to send the second.
func TestNetHTTP_StreamFlushesPerFrame(t *testing.T) {
	gate := make(chan struct{})
	s := NewNetHTTPServer(NetHTTPOptions{})
	s.Handle(func(ctx context.Context, req *Request) *Response {
		return &Response{
			Status: 200,
			Header: Header{"Content-Type": "text/event-stream"},
			Stream: PipeStream(func(w io.Writer) error {
				if _, err := io.WriteString(w, "data: one\n\n"); err != nil {
					return err
				}
				<-gate // block until the client confirms it got frame one
				_, err := io.WriteString(w, "data: two\n\n")
				return err
			}),
		}
	})

	srv := httptest.NewServer(http.HandlerFunc(s.serve))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/sse")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	buf := make([]byte, len("data: one\n\n"))
	if _, err := io.ReadFull(resp.Body, buf); err != nil {
		t.Fatalf("read frame one: %v", err)
	}
	if string(buf) != "data: one\n\n" {
		t.Fatalf("frame one=%q", buf)
	}
	// We received frame one before unblocking frame two → it was flushed,
	// not buffered behind the rest of the response.
	close(gate)
	rest, _ := io.ReadAll(resp.Body)
	if string(rest) != "data: two\n\n" {
		t.Fatalf("frame two=%q", rest)
	}
}

// End-to-end: a zip streamed through the adapter is a valid archive — the
// real mininote-export shape.
func TestPipeStream_ZipRoundTrips(t *testing.T) {
	s := NewNetHTTPServer(NetHTTPOptions{})
	s.Handle(func(ctx context.Context, req *Request) *Response {
		return &Response{
			Status: 200,
			Header: Header{"Content-Type": "application/zip"},
			Stream: PipeStream(func(w io.Writer) error {
				zw := zip.NewWriter(w)
				f, err := zw.Create("note.md")
				if err != nil {
					return err
				}
				if _, err := io.WriteString(f, "# hello"); err != nil {
					return err
				}
				return zw.Close()
			}),
		}
	})
	rec := httptest.NewRecorder()
	s.serve(rec, httptest.NewRequest("GET", "/export.zip", nil))

	zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
	if err != nil {
		t.Fatalf("not a valid zip: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].Name != "note.md" {
		t.Fatalf("zip entries=%v", zr.File)
	}
	rc, _ := zr.File[0].Open()
	defer rc.Close()
	content, _ := io.ReadAll(rc)
	if string(content) != "# hello" {
		t.Fatalf("entry content=%q", content)
	}
}
