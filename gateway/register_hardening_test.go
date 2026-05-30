package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	. "github.com/Toyz/sov/gateway"
	meshsecretproto "github.com/Toyz/sov/gateway/builtin/meshsecret/proto"
	"github.com/Toyz/sov/gateway/builtin/registry"
	"github.com/Toyz/sov/rpc"
)

// testMeshSecretPlugin mirrors what builtin.MeshSecret does — kept
// inline here to avoid an import cycle (the gateway package can't
// import gateway/builtin). Verifies X-Sov-Register-Sig against the
// supplied key on /rpc/_register POSTs; passes all other paths.
type testMeshSecretPlugin struct{ secret []byte }

func (p testMeshSecretPlugin) PluginName() string { return "test-mesh-secret" }
func (p testMeshSecretPlugin) ParseHeaders(req *Request) *rpc.Error {
	if req.Path != "/rpc/_register" {
		return nil
	}
	sig := req.Header.Get(meshsecretproto.RegisterSigHeader)
	ts := req.Header.Get(meshsecretproto.RegisterTsHeader)
	if err := meshsecretproto.Verify(p.secret, sig, ts, req.Body, time.Now()); err != nil {
		return rpc.Unauthorized("_register: %v", err)
	}
	return nil
}

// useMeshSecret returns a gateway pre-wired with the test registry
// plugin AND the mesh-secret plugin and the supplied Options. Tests
// use this in place of the dropped WithMeshSecret Option.
func useMeshSecret(t *testing.T, secret []byte, opts ...Option) *Gateway {
	t.Helper()
	gw := New(opts...)
	if err := gw.Use(registry.New(registry.Config{})); err != nil {
		t.Fatalf("Use registry: %v", err)
	}
	if err := gw.Use(testMeshSecretPlugin{secret: secret}); err != nil {
		t.Fatalf("Use mesh-secret: %v", err)
	}
	return gw
}

// testAllowedServicesPlugin mirrors builtin.AllowedServices — parses
// the register body and rejects any name not on the allowlist.
type testAllowedServicesPlugin struct{ allow map[string]struct{} }

func newTestAllowedServices(names ...string) testAllowedServicesPlugin {
	m := make(map[string]struct{}, len(names))
	for _, n := range names {
		m[n] = struct{}{}
	}
	return testAllowedServicesPlugin{allow: m}
}
func (p testAllowedServicesPlugin) PluginName() string { return "test-allowed-services" }
func (p testAllowedServicesPlugin) ParseHeaders(req *Request) *rpc.Error {
	if req.Path != "/rpc/_register" || len(p.allow) == 0 {
		return nil
	}
	var rr RegisterRequest
	if err := json.Unmarshal(req.Body, &rr); err != nil {
		return nil
	}
	check := func(name string) *rpc.Error {
		if name == "" || strings.HasPrefix(name, "_") {
			return nil
		}
		if _, ok := p.allow[name]; !ok {
			return rpc.Forbidden("_register: service %q not on allow list", name)
		}
		return nil
	}
	if rr.Federate {
		for _, svc := range rr.Services {
			if err := check(svc); err != nil {
				return err
			}
		}
		return nil
	}
	return check(rr.Name)
}

func registerBody(name string, role string) []byte {
	body := map[string]any{
		"name":                       name,
		"address":                    "http://" + name + ":9000",
		"heartbeat_interval_seconds": 10,
	}
	switch role {
	case "auth":
		body["auth"] = true
		body["verify"] = "verify"
	case "authz":
		body["authz"] = true
		body["check"] = "check"
	}
	out, _ := json.Marshal(body)
	return out
}

func TestRegister_RejectsUnsignedWhenSecretSet(t *testing.T) {
	gw := useMeshSecret(t, []byte("topsecret"))
	body := registerBody("Auth", "")
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/_register",
		Header: Header{}, Body: body,
	})
	if resp.Status != 401 {
		t.Fatalf("unsigned register on hardened gateway should be 401, got %d body=%s", resp.Status, resp.Body)
	}
}

func TestRegister_AcceptsValidSig(t *testing.T) {
	secret := []byte("topsecret")
	gw := useMeshSecret(t, secret)
	body := registerBody("Feed", "")
	sig, ts := meshsecretproto.Sign(secret, body, time.Now())
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/_register",
		Header: Header{meshsecretproto.RegisterSigHeader: sig, meshsecretproto.RegisterTsHeader: ts},
		Body:   body,
	})
	if resp.Status != 200 {
		t.Fatalf("valid sig should pass, got %d body=%s", resp.Status, resp.Body)
	}
}

func TestRegister_RejectsExpiredTs(t *testing.T) {
	secret := []byte("topsecret")
	gw := useMeshSecret(t, secret)
	body := registerBody("Feed", "")
	sig, ts := meshsecretproto.Sign(secret, body, time.Now().Add(-10*time.Minute))
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/_register",
		Header: Header{meshsecretproto.RegisterSigHeader: sig, meshsecretproto.RegisterTsHeader: ts},
		Body:   body,
	})
	if resp.Status != 401 || !strings.Contains(string(resp.Body), "skew window") {
		t.Fatalf("expired ts should be rejected, got %d body=%s", resp.Status, resp.Body)
	}
}

func TestRegister_RejectsUnallowedName(t *testing.T) {
	secret := []byte("topsecret")
	gw := useMeshSecret(t, secret)
	if err := gw.Use(newTestAllowedServices("Auth", "Feed")); err != nil {
		t.Fatalf("Use allowed-services: %v", err)
	}
	body := registerBody("Evil", "")
	sig, ts := meshsecretproto.Sign(secret, body, time.Now())
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/_register",
		Header: Header{meshsecretproto.RegisterSigHeader: sig, meshsecretproto.RegisterTsHeader: ts},
		Body:   body,
	})
	if resp.Status != 403 || !strings.Contains(string(resp.Body), "allow list") {
		t.Fatalf("disallowed name should be 403, got %d body=%s", resp.Status, resp.Body)
	}
}

func TestRegister_RoleTakeoverConflict(t *testing.T) {
	gw := newRegistryGateway()
	first := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/_register",
		Header: Header{}, Body: registerBody("Auth", "auth"),
	})
	if first.Status != 200 {
		t.Fatalf("first register: %d %s", first.Status, first.Body)
	}
	second := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/_register",
		Header: Header{}, Body: registerBody("Imposter", "auth"),
	})
	if second.Status != 409 || !strings.Contains(string(second.Body), "ROLE_CONFLICT") {
		t.Fatalf("cross-name role claim should be 409, got %d body=%s", second.Status, second.Body)
	}
}

type testRoleTakeoverPlugin struct{}

func (testRoleTakeoverPlugin) PluginName() string                             { return "test-roletakeover" }
func (testRoleTakeoverPlugin) AllowMeshConflict(_, _ string, c Conflict) bool { return c.Role != 0 }
func (testRoleTakeoverPlugin) ConsumeConflict(_ string, _ Conflict)           {}

func TestRegister_RoleTakeoverAllowed(t *testing.T) {
	gw := newRegistryGateway()
	if err := gw.Use(testRoleTakeoverPlugin{}); err != nil {
		t.Fatalf("Use roletakeover: %v", err)
	}
	first := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/_register",
		Header: Header{}, Body: registerBody("Auth", "auth"),
	})
	if first.Status != 200 {
		t.Fatalf("first register: %d %s", first.Status, first.Body)
	}
	second := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/_register",
		Header: Header{}, Body: registerBody("NewAuth", "auth"),
	})
	if second.Status != 200 {
		t.Fatalf("WithRoleTakeover should permit cross-name swap, got %d body=%s", second.Status, second.Body)
	}
	if gw.AuthBindingForTest() == nil || gw.AuthBindingForTest().Service != "NewAuth" {
		t.Fatalf("expected authBinding to point at NewAuth, got %+v", gw.AuthBindingForTest())
	}
}

func TestRegister_SameNameReclaimAllowed(t *testing.T) {
	gw := newRegistryGateway()
	for i := 0; i < 3; i++ {
		resp := gw.Handle(context.Background(), &Request{
			Method: http.MethodPost, Path: "/rpc/_register",
			Header: Header{}, Body: registerBody("Auth", "auth"),
		})
		if resp.Status != 200 {
			t.Fatalf("same-name reclaim iter %d: %d %s", i, resp.Status, resp.Body)
		}
	}
}
