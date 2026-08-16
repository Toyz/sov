package gateway_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/batch"
	"github.com/Toyz/sov/gateway/internal/gwtest"
	"github.com/Toyz/sov/rpc"
)

type PanicRouter struct{}

func (PanicRouter) Boom(_ *rpc.Context) (string, error) {
	var m map[string]int
	m["x"] = 1 // nil-map write → runtime panic
	return "", nil
}

// A panicking handler must be CONTAINED: a clean 500 on the direct /rpc path,
// and — critically — no process crash when the panic happens inside a batch
// entry's spawned goroutine (which runs outside the transport's own recover).
func TestPanicContainment_DirectAndBatch(t *testing.T) {
	gw := gwtest.New()
	gw.Register(&PanicRouter{})
	gw.MustUse(batch.New(batch.Config{}))

	// Direct: returns a 500, does NOT panic out of Handle.
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Panic/boom", Header: Header{}, Body: []byte("{}"),
	})
	if resp.Status != http.StatusInternalServerError {
		t.Fatalf("panicking handler should yield 500, got %d: %s", resp.Status, resp.Body)
	}

	// Batch: the panicking entry runs in a spawned goroutine. If it weren't
	// contained, this test binary would crash (exit 2) before reaching the
	// assertions below.
	bresp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/_batch", Header: Header{},
		Body: []byte(`{"calls":{"a":{"service":"Panic","method":"boom"}}}`),
	})
	if bresp.Status != http.StatusOK {
		t.Fatalf("batch envelope status = %d: %s", bresp.Status, bresp.Body)
	}
	if !strings.Contains(string(bresp.Body), "error") {
		t.Fatalf("panicking batch entry should carry an error result: %s", bresp.Body)
	}
}
