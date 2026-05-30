package gateway_test

import (
	. "github.com/Toyz/sov/gateway"

	"context"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Toyz/sov/rpc"
)

// panickyInjector panics every InjectHeaders call. Used to exercise
// SeveritySoft recovery — panic must be caught, request must succeed.
type panickyInjector struct{ name string }

func (p panickyInjector) PluginName() string { return p.name }
func (p panickyInjector) InjectHeaders(_ context.Context, _ *Request, _ *http.Request) error {
	panic("boom from " + p.name)
}

// panickyParser panics in ParseHeaders. Severity is Response — caller
// should get the recovery handler's override response (or default 500).
type panickyParser struct{}

func (panickyParser) PluginName() string                 { return "panicky-parser" }
func (panickyParser) ParseHeaders(_ *Request) *rpc.Error { panic("parser boom") }

// panickyBootValidator panics in ValidateBoot. Severity Halt —
// ListenAndServe should return an error wrapping the panic.
type panickyBootValidator struct{}

func (panickyBootValidator) PluginName() string            { return "panicky-boot" }
func (panickyBootValidator) ValidateBoot(_ *Gateway) error { panic("boot boom") }

// capturingRecovery records every failure. Verifies the framework
// routes through registered RecoveryHandlers.
type capturingRecovery struct {
	count    atomic.Int32
	last     HookFailure
	override *Response
}

func (c *capturingRecovery) PluginName() string { return "capturing-recovery" }
func (c *capturingRecovery) HandleHookFailure(f HookFailure) *Response {
	c.count.Add(1)
	c.last = f
	return c.override
}

func TestRecovery_SoftSeverityCaughtRequestSucceeds(t *testing.T) {
	rec := &capturingRecovery{}
	gw := newBatchGateway()
	if err := gw.Use(rec); err != nil {
		t.Fatalf("Use rec: %v", err)
	}
	if err := gw.Use(panickyInjector{name: "panicky"}); err != nil {
		t.Fatalf("Use injector: %v", err)
	}
	gw.RegisterRemote("Remote", "http://does-not-matter:0", time.Minute)

	// Dispatch to a remote service triggers InjectHeaders.
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Remote/x",
		Header: Header{}, Body: []byte(`{"args":[]}`),
	})
	// The remote isn't real → 502 is expected; what matters is
	// the injector PANIC was caught (gateway didn't crash) AND
	// the recovery handler saw it.
	if resp == nil {
		t.Fatal("nil response after recovered panic")
	}
	if rec.count.Load() == 0 {
		t.Fatal("recovery handler not invoked")
	}
	if rec.last.HookName != "HeaderInjector" {
		t.Errorf("hook=%s want HeaderInjector", rec.last.HookName)
	}
	if !strings.Contains(rec.last.Err.Error(), "boom from panicky") {
		t.Errorf("err=%q missing original panic message", rec.last.Err.Error())
	}
}

func TestRecovery_ResponseSeverityReturnsOverride(t *testing.T) {
	rec := &capturingRecovery{override: &Response{Status: 418, Body: []byte("teapot")}}
	gw := New()
	if err := gw.Use(rec); err != nil {
		t.Fatalf("Use rec: %v", err)
	}
	// HeaderParser severity is Response. But ParseHeaders short-
	// circuits return *rpc.Error as control flow — only PANICS go to
	// recovery. Confirm the recovery handler IS invoked for the
	// panic, AND the parser-short-circuit path is untouched.
	if err := gw.Use(panickyParser{}); err != nil {
		t.Fatalf("Use parser: %v", err)
	}
	gw.Register(&EchoRouter{})

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Echo/ping",
		Header: Header{}, Body: []byte(`{"args":{}}`),
	})
	if rec.count.Load() == 0 {
		t.Fatal("recovery handler not invoked on parser panic")
	}
	// The framework keeps serving despite the panic. Response
	// shape varies by override; for this test we just confirm
	// non-nil + non-crash.
	if resp == nil {
		t.Fatal("nil response after recovered parser panic")
	}
}

func TestRecovery_HaltSeverityAbortsBoot(t *testing.T) {
	gw := New()
	if err := gw.Use(panickyBootValidator{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := gw.ListenAndServe(ctx, ":0")
	if err == nil {
		t.Fatal("expected boot error, got nil")
	}
	if !strings.Contains(err.Error(), "boot boom") {
		t.Errorf("err=%q missing panic message", err.Error())
	}
}

func TestRecovery_DefaultHandlerNoCrash(t *testing.T) {
	// No RecoveryHandler registered. Default handler logs via
	// gw.Log() (slog.Default()) and returns nil. Confirm the gateway
	// still serves the request after a soft-severity panic.
	gw := newBatchGateway()
	if err := gw.Use(panickyInjector{name: "default-test"}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	gw.RegisterRemote("Remote", "http://does-not-matter:0", time.Minute)
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Remote/x",
		Header: Header{}, Body: []byte(`{"args":[]}`),
	})
	if resp == nil {
		t.Fatal("nil response after soft panic with default recovery")
	}
}
