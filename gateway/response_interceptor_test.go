package gateway_test

import (
	. "github.com/Toyz/sov/gateway"

	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/Toyz/sov/rpc"
)

// stamper appends its name to a response header on every intercept.
type stamper struct {
	name  string
	calls atomic.Int32
	fail  bool
}

func (s *stamper) PluginName() string { return "stamper-" + s.name }
func (s *stamper) InterceptResponse(_ *Request, resp *Response) error {
	s.calls.Add(1)
	if resp.Header == nil {
		resp.Header = Header{}
	}
	prev := resp.Header["X-Stampers"]
	if prev != "" {
		prev += ","
	}
	resp.Header["X-Stampers"] = prev + s.name
	if s.fail {
		return rpcErr("stamper-fail")
	}
	return nil
}

func rpcErr(msg string) error { return &rpc.Error{Status: 500, Code: "TEST", Message: msg} }

type RespPingRouter struct{}

func (RespPingRouter) Hello(_ *rpc.Context) (string, error) { return "hi", nil }

func TestResponseInterceptor_RegistrationOrder(t *testing.T) {
	gw := New()
	a := &stamper{name: "a"}
	b := &stamper{name: "b"}
	c := &stamper{name: "c"}
	if err := gw.Use(a); err != nil {
		t.Fatal(err)
	}
	if err := gw.Use(b); err != nil {
		t.Fatal(err)
	}
	if err := gw.Use(c); err != nil {
		t.Fatal(err)
	}
	gw.Register(&RespPingRouter{})

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/RespPing/hello",
		Header: Header{}, Body: []byte(`{"args":[]}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	if got := resp.Header["X-Stampers"]; got != "a,b,c" {
		t.Errorf("X-Stampers=%q want a,b,c", got)
	}
	for _, s := range []*stamper{a, b, c} {
		if s.calls.Load() != 1 {
			t.Errorf("stamper %q calls=%d want 1", s.name, s.calls.Load())
		}
	}
}

func TestResponseInterceptor_FailingOneDoesntStopOthers(t *testing.T) {
	gw := New()
	a := &stamper{name: "a", fail: true}
	b := &stamper{name: "b"}
	if err := gw.Use(a); err != nil {
		t.Fatal(err)
	}
	if err := gw.Use(b); err != nil {
		t.Fatal(err)
	}
	gw.Register(&RespPingRouter{})

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/RespPing/hello",
		Header: Header{}, Body: []byte(`{"args":[]}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	// Soft severity: a's failure is logged but b still runs.
	if b.calls.Load() != 1 {
		t.Errorf("second interceptor didn't run after first errored")
	}
}
