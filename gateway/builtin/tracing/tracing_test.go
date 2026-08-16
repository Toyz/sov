package tracing

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Toyz/sov/gateway"
)

func TestTracing_MintsRootWhenAbsent(t *testing.T) {
	req := &gateway.Request{Header: gateway.Header{}}
	New().ParseHeaders(req)
	tid, flags, ok := parseTraceparent(req.Header.Get(traceparentHeader))
	if !ok {
		t.Fatalf("minted traceparent is invalid: %q", req.Header.Get(traceparentHeader))
	}
	if len(tid) != 32 || flags != "01" {
		t.Fatalf("tid=%s flags=%s", tid, flags)
	}
}

func TestTracing_KeepsValidInbound(t *testing.T) {
	in := "00-" + strings.Repeat("a", 32) + "-" + strings.Repeat("b", 16) + "-01"
	req := &gateway.Request{Header: gateway.Header{traceparentHeader: in}}
	New().ParseHeaders(req)
	if req.Header.Get(traceparentHeader) != in {
		t.Fatalf("a valid inbound traceparent must be kept, got %q", req.Header.Get(traceparentHeader))
	}
}

func TestTracing_InjectMintsChildSpan(t *testing.T) {
	tid := strings.Repeat("a", 32)
	in := "00-" + tid + "-" + strings.Repeat("b", 16) + "-01"
	req := &gateway.Request{Header: gateway.Header{traceparentHeader: in}}
	hreq, _ := http.NewRequest("POST", "http://x/y", nil)
	New().InjectHeaders(context.Background(), req, hreq)
	out := hreq.Header.Get(traceparentHeader)
	otid, _, ok := parseTraceparent(out)
	if !ok || otid != tid {
		t.Fatalf("child hop must keep the trace-id, got %q", out)
	}
	if out == in {
		t.Fatal("child span-id must differ from the parent's")
	}
}

func TestParseTraceparent_Rejects(t *testing.T) {
	for _, b := range []string{
		"", "garbage", "00-x-y-z",
		"00-" + strings.Repeat("0", 32) + "-" + strings.Repeat("b", 16) + "-01", // all-zero trace
		"00-" + strings.Repeat("g", 32) + "-" + strings.Repeat("b", 16) + "-01", // non-hex
	} {
		if _, _, ok := parseTraceparent(b); ok {
			t.Fatalf("should reject %q", b)
		}
	}
}
