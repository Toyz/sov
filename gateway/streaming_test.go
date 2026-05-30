package gateway

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
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
