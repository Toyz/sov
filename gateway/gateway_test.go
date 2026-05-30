package gateway_test

import (
	. "github.com/Toyz/sov/gateway"

	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Toyz/sov/rpc"
)

// ---- Test routers ----------------------------------------------------------

type EchoRouter struct{}
type EchoSayParams struct {
	Msg string `json:"msg"`
}

func (r *EchoRouter) Say(ctx *rpc.Context, p *EchoSayParams) (map[string]string, error) {
	return map[string]string{"echoed": p.Msg}, nil
}
func (r *EchoRouter) Ping(ctx *rpc.Context) (map[string]bool, error) {
	return map[string]bool{"ok": true}, nil
}

// ---- Helpers ---------------------------------------------------------------

func newGateway(t *testing.T) *Gateway {
	t.Helper()
	gw := newRegistryGateway()
	gw.Register(&EchoRouter{})
	return gw
}

func do(t *testing.T, gw *Gateway, method, path string, body []byte) *Response {
	t.Helper()
	req := &Request{Method: method, Path: path, Header: Header{}, Body: body}
	return gw.HandleRaw(context.Background(), req)
}

// ---- Tests -----------------------------------------------------------------

func TestGateway_LocalDispatch(t *testing.T) {
	resp := do(t, newGateway(t), http.MethodPost, "/rpc/Echo/say", []byte(`{"args":[{"msg":"hi"}]}`))
	if resp.Status != 200 {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), `"echoed":"hi"`) {
		t.Fatalf("body = %s", resp.Body)
	}
}

func TestGateway_UnknownService(t *testing.T) {
	resp := do(t, newGateway(t), http.MethodPost, "/rpc/Nope/x", nil)
	if resp.Status != 404 {
		t.Fatalf("status = %d", resp.Status)
	}
	if !strings.Contains(string(resp.Body), `"NOT_FOUND"`) || !strings.Contains(string(resp.Body), "Nope") {
		t.Fatalf("body = %s", resp.Body)
	}
}

func TestGateway_RefusesServiceLevelUnderscore(t *testing.T) {
	resp := do(t, newGateway(t), http.MethodPost, "/rpc/Echo/_debug", nil)
	if resp.Status != 404 {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), "internal-network only") {
		t.Fatalf("body = %s", resp.Body)
	}
}

func TestGateway_RefusesReservedRouter(t *testing.T) {
	resp := do(t, newGateway(t), http.MethodPost, "/rpc/_admin/whatever", nil)
	if resp.Status != 404 {
		t.Fatalf("status = %d", resp.Status)
	}
}

func TestGateway_HealthLocalOnly(t *testing.T) {
	resp := do(t, newGateway(t), http.MethodGet, "/rpc/_health", nil)
	if resp.Status != 200 {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
	var hr HealthReport
	if err := json.Unmarshal(resp.Body, &hr); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if hr.Status != "healthy" {
		t.Fatalf("status = %q", hr.Status)
	}
	if _, ok := hr.Services["Echo"]; !ok {
		t.Fatalf("Echo missing: %#v", hr.Services)
	}
}

func TestGateway_IntrospectLocalOnly(t *testing.T) {
	resp := do(t, newGateway(t), http.MethodGet, "/rpc/_introspect", nil)
	if resp.Status != 200 {
		t.Fatalf("status = %d", resp.Status)
	}
	var ir IntrospectReport
	if err := json.Unmarshal(resp.Body, &ir); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := ir.Services["Echo"]; !ok {
		t.Fatalf("Echo missing: %#v", ir.Services)
	}
}

func TestGateway_BatchFanIn(t *testing.T) {
	body := []byte(`{
		"calls": {
			"hello": {"service": "Echo", "method": "say", "args": [{"msg": "world"}]},
			"alive": {"service": "Echo", "method": "ping"}
		}
	}`)
	resp := do(t, newGateway(t), http.MethodPost, "/rpc/_batch", body)
	if resp.Status != 200 {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
	var br BatchResponse
	if err := json.Unmarshal(resp.Body, &br); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(br.Results) != 2 {
		t.Fatalf("results = %#v", br.Results)
	}
	if !strings.Contains(string(br.Results["hello"]), `"world"`) {
		t.Fatalf("hello = %s", br.Results["hello"])
	}
	if !strings.Contains(string(br.Results["alive"]), `"ok":true`) {
		t.Fatalf("alive = %s", br.Results["alive"])
	}
}

func TestGateway_RegisterAddsRemote(t *testing.T) {
	gw := newGateway(t)
	body := []byte(`{"name":"Widgets","address":"http://widgets-pod:8080","heartbeat_interval_seconds":10}`)
	resp := do(t, gw, http.MethodPost, "/rpc/_register", body)
	if resp.Status != 200 {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
	if !contains(gw.RegisterField().Services(), "Widgets") {
		t.Fatalf("Widgets not registered: %v", gw.RegisterField().Services())
	}
}

func TestGateway_RegisterRefusesUnderscoreNames(t *testing.T) {
	resp := do(t, newGateway(t), http.MethodPost, "/rpc/_register", []byte(`{"name":"_admin","address":"x"}`))
	if resp.Status != 400 {
		t.Fatalf("status = %d", resp.Status)
	}
}

func TestGateway_RemoteProxy(t *testing.T) {
	// Upstream "remote service" that echoes path + body + the
	// gateway-injected X-Sov-Subject header.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"path":"` + r.URL.Path + `","body":` + string(b) + `,"sov_subject":"` + r.Header.Get("X-Sov-Subject") + `"}}`))
	}))
	defer upstream.Close()

	gw := New()
	gw.RegisterRemote("Widgets", upstream.URL, time.Minute)

	req := &Request{
		Method: http.MethodPost,
		Path:   "/rpc/Widgets/create",
		Header: Header{},
		Body:   []byte(`{"args":[{"name":"foo"}]}`),
		User:   &Claims{Subject: "alice"},
	}
	resp := gw.Handle(context.Background(), req)
	if resp.Status != 200 {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), `"sov_subject":"alice"`) {
		t.Fatalf("missing X-Sov-Subject injection: %s", resp.Body)
	}
	if !strings.Contains(string(resp.Body), `"path":"/rpc/Widgets/create"`) {
		t.Fatalf("path mismatch: %s", resp.Body)
	}
}

func TestGateway_PluggableServerInterface(t *testing.T) {
	// Trivial fake Server proves the interface is sufficient without net/http.
	fake := &fakeServer{}
	gw := New(WithServer(fake))
	gw.Register(&EchoRouter{})
	if fake.h == nil {
		t.Fatal("Server.Handle was not called")
	}
	resp := fake.h(context.Background(), &Request{Method: http.MethodPost, Path: "/rpc/Echo/ping", Header: Header{}})
	if resp.Status != 200 {
		t.Fatalf("status = %d", resp.Status)
	}
}

type fakeServer struct {
	h RequestHandler
}

func (f *fakeServer) Handle(h RequestHandler)                          { f.h = h }
func (f *fakeServer) ListenAndServe(_ context.Context, _ string) error { return nil }

func contains(ss []string, s string) bool {
	for _, x := range ss {
		if x == s {
			return true
		}
	}
	return false
}
