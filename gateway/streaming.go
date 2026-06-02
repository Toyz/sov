package gateway

import (
	"fmt"
	"io"
)

// PipeStream adapts a writer-callback into an io.ReadCloser for
// Response.Stream — the ergonomic form when you have code that writes to an
// io.Writer (a zip.Writer, csv.Writer, a template, an SSE loop) rather than
// a ready-made reader.
//
// fn runs in its own goroutine writing to the pipe; whatever it writes is
// streamed to the client as it is produced, in constant memory. fn's
// return value closes the read end: nil → clean EOF; an error → the
// adapter's io.Copy stops and the transfer truncates (the client sees a
// short/aborted body — there is no way to send a 500 after the status line
// is already on the wire, so validate before you start streaming).
//
// The producer never blocks forever: if the client disconnects, the
// adapter closes the reader, the next pw.Write fails with
// io.ErrClosedPipe, and fn returns.
//
// A panic inside fn is recovered and surfaced to the reader as an error
// (the transfer truncates) instead of crashing the process — fn runs in
// its own goroutine, so the gateway's request-scoped recovery middleware
// cannot see it. The panic value is not swallowed silently: it becomes
// the pipe's close error.
//
//	return &gateway.Response{
//	    Status: 200,
//	    Header: gateway.Header{
//	        "Content-Type":        "application/zip",
//	        "Content-Disposition": `attachment; filename="export.zip"`,
//	    },
//	    Stream: gateway.PipeStream(func(w io.Writer) error {
//	        zw := zip.NewWriter(w)
//	        for _, f := range files {
//	            fw, err := zw.Create(f.Name)
//	            if err != nil {
//	                return err
//	            }
//	            if _, err := fw.Write(f.Data); err != nil {
//	                return err
//	            }
//	        }
//	        return zw.Close()
//	    }),
//	}
func PipeStream(fn func(w io.Writer) error) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		// CloseWithError(nil) is a clean EOF; a non-nil err is surfaced to
		// the reader (the adapter's io.Copy returns it). A panic in fn is
		// recovered here — this goroutine is outside the gateway's recovery
		// middleware, so an unrecovered panic would kill the process.
		var err error
		defer func() {
			if r := recover(); r != nil {
				err = fmt.Errorf("gateway: stream producer panic: %v", r)
			}
			_ = pw.CloseWithError(err)
		}()
		err = fn(pw)
	}()
	return pr
}
