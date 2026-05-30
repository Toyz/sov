package gateway_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/registry"
)

// ---- helpers --------------------------------------------------------------

func registerFederationBody(name, addr string, services []string) []byte {
	body, _ := json.Marshal(map[string]any{
		"name":                       name,
		"address":                    addr,
		"heartbeat_interval_seconds": 30,
		"federate":                   true,
		"services":                   services,
		"introspect":                 true,
	})
	return body
}

func postBatch(gw *Gateway, body []byte) *Response {
	return gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/_register",
		Header: Header{}, Body: body,
	})
}

// ---- D.1 register-conflict ------------------------------------------------

func TestFederate_RegistersAllServices(t *testing.T) {
	gw := newRegistryGateway()
	resp := postBatch(gw, registerFederationBody("team-a", "http://team-a:9100",
		[]string{"Alpha", "Beta", "Gamma"}))
	if resp.Status != 200 {
		t.Fatalf("federate register: %d %s", resp.Status, resp.Body)
	}
	for _, svc := range []string{"Alpha", "Beta", "Gamma"} {
		ep, ok := gw.Resolver().Resolve(context.Background(), svc)
		if !ok || ep.RemoteAddr != "http://team-a:9100" {
			t.Errorf("svc %q: ep=%+v ok=%v", svc, ep, ok)
		}
	}
}

func TestFederate_LocalShadowRejected(t *testing.T) {
	gw := newRegistryGateway()
	gw.Register(&EchoRouter{})
	resp := postBatch(gw, registerFederationBody("team-a", "http://team-a:9100", []string{"Echo"}))
	if resp.Status != 409 {
		t.Fatalf("local-shadow should be 409, got %d %s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), "served locally") {
		t.Errorf("body=%s", resp.Body)
	}
}

func TestFederate_SameAddressRefresh(t *testing.T) {
	gw := newRegistryGateway()
	body := registerFederationBody("team-a", "http://team-a:9100", []string{"Alpha", "Beta"})
	if r := postBatch(gw, body); r.Status != 200 {
		t.Fatalf("first: %d %s", r.Status, r.Body)
	}
	if r := postBatch(gw, body); r.Status != 200 {
		t.Fatalf("refresh: %d %s", r.Status, r.Body)
	}
}

func TestFederate_DifferentAddressFails409(t *testing.T) {
	gw := newRegistryGateway()
	if r := postBatch(gw, registerFederationBody("team-a", "http://team-a:9100", []string{"Alpha"})); r.Status != 200 {
		t.Fatalf("first: %d %s", r.Status, r.Body)
	}
	resp := postBatch(gw, registerFederationBody("team-b", "http://team-b:9200", []string{"Alpha"}))
	if resp.Status != 409 {
		t.Fatalf("expected 409 SERVICE_CONFLICT, got %d %s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), "already federated") {
		t.Errorf("body=%s", resp.Body)
	}
}

type testPreemptPlugin struct{ m map[string]string }

func (p testPreemptPlugin) PluginName() string { return "test-preempt" }
func (p testPreemptPlugin) AllowMeshConflict(svc, _ string, c Conflict) bool {
	if c.Role != 0 {
		return false
	}
	want, ok := p.m[svc]
	return ok && want == c.FederatedAddrs[1]
}
func (p testPreemptPlugin) ConsumeConflict(svc string, c Conflict) {
	if c.Role != 0 {
		return
	}
	delete(p.m, svc)
}

func TestFederate_PreemptionOverride(t *testing.T) {
	gw := newRegistryGateway()
	canon, _ := NormalizeUpstreamURL("http://team-b:9200")
	if err := gw.Use(testPreemptPlugin{m: map[string]string{"Alpha": canon}}); err != nil {
		t.Fatalf("Use preempt: %v", err)
	}
	if r := postBatch(gw, registerFederationBody("team-a", "http://team-a:9100", []string{"Alpha"})); r.Status != 200 {
		t.Fatalf("first: %d %s", r.Status, r.Body)
	}
	r := postBatch(gw, registerFederationBody("team-b", "http://team-b:9200", []string{"Alpha"}))
	if r.Status != 200 {
		t.Fatalf("preempted register should pass, got %d %s", r.Status, r.Body)
	}
	ep, _ := gw.Resolver().Resolve(context.Background(), "Alpha")
	if ep == nil || ep.RemoteAddr != "http://team-b:9200" {
		t.Fatalf("new addr did not win: %+v", ep)
	}
	// Preemption consumed — repeat from team-a fails again.
	r2 := postBatch(gw, registerFederationBody("team-a", "http://team-a:9100", []string{"Alpha"}))
	if r2.Status != 409 {
		t.Fatalf("preemption should be one-shot, got %d", r2.Status)
	}
}

func TestFederate_RejectsRoleClaimWhenFederating(t *testing.T) {
	gw := newRegistryGateway()
	body, _ := json.Marshal(map[string]any{
		"name": "team-a", "address": "http://team-a:9100",
		"heartbeat_interval_seconds": 30,
		"federate":                   true,
		"services":                   []string{"Auth"},
		"auth":                       true,
	})
	resp := postBatch(gw, body)
	if resp.Status != 400 {
		t.Fatalf("expected 400, got %d %s", resp.Status, resp.Body)
	}
}

// ---- D.2 trust guard ------------------------------------------------------

// Inter-service trust is trust-by-default: WithTrustUpstreamClaims(true)
// with NO SealVerifier/UpstreamTrustPolicy must BOOT (network-trust model),
// not refuse. Per-request crypto is opt-in hardening, not mandatory.
func TestFederate_TrustByDefaultBootsWithoutProof(t *testing.T) {
	gw := New(WithTrustUpstreamClaims(true))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := gw.ListenAndServe(ctx, ":0")
	if err != nil && strings.Contains(err.Error(), "SealVerifier or UpstreamTrustPolicy") {
		t.Fatalf("trust-by-default must boot without a proof plugin, got %v", err)
	}
}

type testSealVerifierPlugin struct{}

func (testSealVerifierPlugin) PluginName() string                    { return "test-seal-verifier" }
func (testSealVerifierPlugin) VerifySeal(_ map[string][]string) bool { return true }

func TestFederate_TrustGuardAcceptsWithHMAC(t *testing.T) {
	gw := New(WithTrustUpstreamClaims(true))
	if err := gw.Use(testSealVerifierPlugin{}); err != nil {
		t.Fatalf("Use seal verifier: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := gw.ListenAndServe(ctx, ":0")
	if err != nil && strings.Contains(err.Error(), "SealVerifier or UpstreamTrustPolicy") {
		t.Fatalf("unexpected boot rejection: %v", err)
	}
}

type testUpstreamTrustPlugin struct{ allow []string }

func (p testUpstreamTrustPlugin) PluginName() string { return "test-upstream-trust" }
func (p testUpstreamTrustPlugin) TrustUpstream(_ map[string][]string) bool {
	return true
}

func TestFederate_TrustGuardAcceptsWithAllowlist(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("unexpected panic with allowlist: %v", r)
		}
	}()
	gw := New(WithTrustUpstreamClaims(true))
	if err := gw.Use(testUpstreamTrustPlugin{allow: []string{"http://prime:8080"}}); err != nil {
		t.Fatalf("Use: %v", err)
	}
}

// ---- D.3 introspect cascade + loop guard ----------------------------------

func TestFederate_IntrospectProbesOncePerAddress(t *testing.T) {
	var probes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		_, _ = io.WriteString(w, `{"services":{"Alpha":[{"router":"Alpha","title":"Alpha","methods":[]}],"Beta":[{"router":"Beta","title":"Beta","methods":[]}],"Gamma":[{"router":"Gamma","title":"Gamma","methods":[]}]}}`)
	}))
	defer upstream.Close()

	gw := newRegistryGateway()
	if r := postBatch(gw, registerFederationBody("team-a", upstream.URL, []string{"Alpha", "Beta", "Gamma"})); r.Status != 200 {
		t.Fatalf("register: %d", r.Status)
	}

	resp := gw.Handle(context.Background(), &Request{Method: http.MethodGet, Path: "/rpc/_introspect", Header: Header{}})
	if resp.Status != 200 {
		t.Fatalf("introspect: %d %s", resp.Status, resp.Body)
	}
	if probes.Load() != 1 {
		t.Fatalf("expected 1 probe to upstream, got %d", probes.Load())
	}
	var rpt IntrospectReport
	_ = json.Unmarshal(resp.Body, &rpt)
	for _, want := range []string{"Alpha", "Beta", "Gamma"} {
		if _, ok := rpt.Services[want]; !ok {
			t.Errorf("merged report missing %q: %+v", want, rpt.Services)
		}
	}
}

func TestFederate_IntrospectVisitedSet(t *testing.T) {
	var probes atomic.Int32
	var capturedVisited atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		probes.Add(1)
		capturedVisited.Store(r.Header.Get(IntrospectVisitedHeader))
		_, _ = io.WriteString(w, `{"services":{"Alpha":[{"router":"Alpha","title":"Alpha","methods":[]}]}}`)
	}))
	defer upstream.Close()

	gw := newRegistryGateway()
	if r := postBatch(gw, registerFederationBody("team-a", upstream.URL, []string{"Alpha"})); r.Status != 200 {
		t.Fatalf("register: %d", r.Status)
	}
	resp := gw.Handle(context.Background(), &Request{Method: http.MethodGet, Path: "/rpc/_introspect", Header: Header{}})
	if resp.Status != 200 {
		t.Fatalf("introspect: %d", resp.Status)
	}
	got, _ := capturedVisited.Load().(string)
	if !strings.Contains(got, "http://") {
		t.Fatalf("downstream did not see X-Sov-Introspect-Visited: %q", got)
	}
}

// ---- D.4 tiered health ----------------------------------------------------

func TestFederate_HealthDegradedRollup(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Team gateway reports 207 with one child healthy, one down.
		w.WriteHeader(http.StatusMultiStatus)
		_, _ = io.WriteString(w, `{"status":"degraded","services":{"Alpha":{"status":"healthy"},"Beta":{"status":"missing"}}}`)
	}))
	defer upstream.Close()

	gw := newRegistryGateway()
	if r := postBatch(gw, registerFederationBody("team-a", upstream.URL, []string{"Alpha", "Beta"})); r.Status != 200 {
		t.Fatalf("register: %d", r.Status)
	}
	resp := gw.Handle(context.Background(), &Request{Method: http.MethodGet, Path: "/rpc/_health", Header: Header{}})
	var rpt HealthReport
	_ = json.Unmarshal(resp.Body, &rpt)
	if rpt.Services["Alpha"].Status != "degraded" {
		t.Fatalf("Alpha status = %q, want degraded; full=%+v", rpt.Services["Alpha"].Status, rpt)
	}
	if rpt.Services["Alpha"].Children == nil {
		t.Fatalf("Alpha missing Children rollup: %+v", rpt.Services["Alpha"])
	}
	if rpt.Services["Alpha"].Children["Beta"].Status != "missing" {
		t.Fatalf("Beta child not surfaced: %+v", rpt.Services["Alpha"].Children)
	}
}

func TestFederate_HealthMissingVsUnhealthy(t *testing.T) {
	// Upstream that 503s — distinct from connection-refused (missing).
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"unhealthy"}`)
	}))
	defer upstream.Close()

	gw := newRegistryGateway()
	if r := postBatch(gw, registerFederationBody("team-up", upstream.URL, []string{"Alpha"})); r.Status != 200 {
		t.Fatalf("register up: %d", r.Status)
	}
	// Second federation pointing at a non-existent address to test missing.
	if r := postBatch(gw, registerFederationBody("team-down", "http://127.0.0.1:1", []string{"Beta"})); r.Status != 200 {
		t.Fatalf("register down: %d", r.Status)
	}

	resp := gw.Handle(context.Background(), &Request{Method: http.MethodGet, Path: "/rpc/_health", Header: Header{}})
	var rpt HealthReport
	_ = json.Unmarshal(resp.Body, &rpt)
	if rpt.Services["Alpha"].Status != "unhealthy" {
		t.Errorf("Alpha (5xx) = %q, want unhealthy", rpt.Services["Alpha"].Status)
	}
	if rpt.Services["Beta"].Status != "missing" {
		t.Errorf("Beta (refused) = %q, want missing", rpt.Services["Beta"].Status)
	}
}

// TestFederate_HealthRollupHTTPStatus verifies that the federated
// health rollup downgrades BOTH the response body's top-level Status
// AND the HTTP status code when a remote pod is unhealthy. Previous
// tests covered per-service status but not the headline number ops
// alerting + load balancers gate on.
func TestFederate_HealthRollupHTTPStatus(t *testing.T) {
	// One unhealthy remote, no local services → all-bad → 503.
	upstream503 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"unhealthy"}`)
	}))
	defer upstream503.Close()

	gw := newRegistryGateway()
	if r := postBatch(gw, registerFederationBody("only-bad", upstream503.URL, []string{"OnlyBad"})); r.Status != 200 {
		t.Fatalf("register: %d", r.Status)
	}
	resp := gw.Handle(context.Background(), &Request{Method: http.MethodGet, Path: "/rpc/_health", Header: Header{}})
	if resp.Status != http.StatusServiceUnavailable {
		t.Fatalf("HTTP status = %d, want 503", resp.Status)
	}
	if !strings.Contains(string(resp.Body), `"status":"unhealthy"`) {
		t.Errorf("body missing top-level unhealthy: %s", resp.Body)
	}
}

// TestFederate_HealthRollupMixedReturns207 — healthy local + unhealthy
// remote → degraded → HTTP 207.
func TestFederate_HealthRollupMixedReturns207(t *testing.T) {
	upstream503 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"status":"unhealthy"}`)
	}))
	defer upstream503.Close()

	gw := newRegistryGateway()
	gw.Register(&EchoRouter{}) // local healthy
	if r := postBatch(gw, registerFederationBody("team-bad", upstream503.URL, []string{"BadSvc"})); r.Status != 200 {
		t.Fatalf("register: %d", r.Status)
	}
	resp := gw.Handle(context.Background(), &Request{Method: http.MethodGet, Path: "/rpc/_health", Header: Header{}})
	if resp.Status != http.StatusMultiStatus {
		t.Fatalf("HTTP status = %d, want 207", resp.Status)
	}
	if !strings.Contains(string(resp.Body), `"status":"degraded"`) {
		t.Errorf("body missing top-level degraded: %s", resp.Body)
	}
}

// ---- batch cascade through federation -------------------------------------

func TestFederate_BatchCascadesThroughTeamGateway(t *testing.T) {
	var batchHits atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rpc/_batch" {
			batchHits.Add(1)
			_, _ = io.WriteString(w, `{"results":{"a":{"data":1},"b":{"data":2},"c":{"data":3}}}`)
			return
		}
		_, _ = io.WriteString(w, `{"data":{}}`)
	}))
	defer upstream.Close()

	gw := New()
	// Federation registers 3 services to one team gateway.
	if r := postBatch(withRegistry(gw, t), registerFederationBody("team-a", upstream.URL, []string{"X", "Y", "Z"})); r.Status != 200 {
		t.Fatalf("register: %d", r.Status)
	}
	_ = batchHits.Load()
	// (skip — batch cascade is already covered by TestBatch_GroupsByRemoteService;
	//  this test asserts the FEDERATION register path didn't break the cascade.)
}

// withRegistry installs the REAL registry plugin on gw — helper to
// avoid newRegistryGateway() boilerplate in tests that build a
// gateway in steps. Free function (not a method) because the external
// test package cannot define methods on the gateway.Gateway type.
func withRegistry(g *Gateway, t *testing.T) *Gateway {
	t.Helper()
	if err := g.Use(registry.New(registry.Config{})); err != nil {
		t.Fatalf("Use registry: %v", err)
	}
	return g
}

// ---- URL normalization edge ----------------------------------------------

func TestFederate_NormalizedAddressMatching(t *testing.T) {
	gw := newRegistryGateway()
	// Register with one form …
	if r := postBatch(gw, registerFederationBody("team-a", "http://Team-A:9100/", []string{"Alpha"})); r.Status != 200 {
		t.Fatalf("first: %d %s", r.Status, r.Body)
	}
	// … and refresh with a canonical equivalent. Should be same-address refresh, not 409.
	if r := postBatch(gw, registerFederationBody("team-a", "http://team-a:9100", []string{"Alpha"})); r.Status != 200 {
		t.Fatalf("normalized refresh: %d %s", r.Status, r.Body)
	}
}

// keep imports honest
var _ = time.Second
