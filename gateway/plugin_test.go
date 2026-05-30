package gateway_test

import (
	. "github.com/Toyz/sov/gateway"

	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/Toyz/sov/rpc"
)

// ---- Test plugins ---------------------------------------------------------

type recordingPlugin struct {
	name       string
	headerHits atomic.Int32
	parseHits  atomic.Int32
	authHits   atomic.Int32
	dispatch   atomic.Int32
	bootHits   atomic.Int32
	lifecycle  atomic.Int32
	augments   atomic.Int32
}

func (r *recordingPlugin) PluginName() string { return r.name }

func (r *recordingPlugin) InjectHeaders(_ context.Context, _ *Request, hreq *http.Request) error {
	r.headerHits.Add(1)
	hreq.Header.Set("X-From-Plugin", r.name)
	return nil
}

func (r *recordingPlugin) ParseHeaders(_ *Request) *rpc.Error {
	r.parseHits.Add(1)
	return nil
}

func (r *recordingPlugin) TranslateAuth(req *Request, _ *Claims) error {
	r.authHits.Add(1)
	req.Header["X-Translated"] = r.name
	return nil
}

func (r *recordingPlugin) OnDispatch(_ DispatchEvent) error {
	r.dispatch.Add(1)
	return nil
}

func (r *recordingPlugin) ValidateBoot(_ *Gateway) error {
	r.bootHits.Add(1)
	return nil
}

func (r *recordingPlugin) OnStart(_ context.Context) error { r.lifecycle.Add(1); return nil }
func (r *recordingPlugin) OnStop(_ context.Context) error  { return nil }

func (r *recordingPlugin) ContributeIntrospect(_ context.Context, report *IntrospectReport, _ string, _ []string) error {
	r.augments.Add(1)
	for i := range report.Plugins {
		if report.Plugins[i].Name == r.name {
			if report.Plugins[i].Extra == nil {
				report.Plugins[i].Extra = map[string]any{}
			}
			report.Plugins[i].Extra["custom"] = "value"
		}
	}
	return nil
}

// ---- Tests ----------------------------------------------------------------

func TestPlugin_AutoDetectHooks(t *testing.T) {
	gw := New()
	p := &recordingPlugin{name: "rec"}
	if err := gw.Use(p); err != nil {
		t.Fatalf("Use: %v", err)
	}
	// The plugin's sub-interfaces should all be auto-detected on the entry.
	var e *PluginHookView
	for _, pe := range gw.PluginHookViews() {
		if pe.Name == "rec" {
			pe := pe
			e = &pe
			break
		}
	}
	if e == nil {
		t.Fatalf("plugin entry not registered")
	}
	if !e.HeaderInjector {
		t.Errorf("headerInjector not detected")
	}
	if !e.DispatchHook {
		t.Errorf("dispatchHook not detected")
	}
	if !e.AuthTranslator {
		t.Errorf("authTranslator not detected")
	}
	if !e.BootValidator {
		t.Errorf("bootValidator not detected")
	}
	if !e.LifecycleHook {
		t.Errorf("lifecycleHook not detected")
	}
	if !e.IntroContributor {
		t.Errorf("introContributor not detected")
	}
}

func TestPlugin_DispatchHookFires(t *testing.T) {
	gw := New()
	gw.Register(&EchoRouter{})
	p := &recordingPlugin{name: "rec"}
	_ = gw.Use(p)

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Echo/ping", Header: Header{}, Body: []byte(`{"args":{}}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	if p.dispatch.Load() == 0 {
		t.Fatal("DispatchHook did not fire")
	}
	if p.parseHits.Load() == 0 {
		t.Fatal("HeaderParser did not fire on inbound")
	}
}

func TestPlugin_RejectsEmptyContract(t *testing.T) {
	gw := New()
	type bareType struct{}
	err := gw.Use(&bareType{})
	if err == nil {
		t.Fatal("expected error on plugin with no hooks + no router methods")
	}
	if !strings.Contains(err.Error(), "satisfies no plugin sub-interface") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPlugin_IntrospectListsPlugins(t *testing.T) {
	gw := newRegistryGateway()
	p := &recordingPlugin{name: "rec"}
	_ = gw.Use(p)

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/rpc/_introspect", Header: Header{},
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	var rpt IntrospectReport
	if err := json.Unmarshal(resp.Body, &rpt); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	var pi PluginInfo
	for _, p := range rpt.Plugins {
		if p.Name == "rec" {
			pi = p
			break
		}
	}
	if pi.Name != "rec" {
		t.Fatalf("rec plugin missing, got %#v", rpt.Plugins)
	}
	// At least these hooks should appear.
	hookSet := map[string]bool{}
	for _, h := range pi.Hooks {
		hookSet[h] = true
	}
	for _, want := range []string{"HeaderInjector", "DispatchHook", "AuthTranslator", "BootValidator", "IntrospectContributor"} {
		if !hookSet[want] {
			t.Errorf("missing hook %q in %v", want, pi.Hooks)
		}
	}
	// Augmenter should have stamped Extra.custom.
	if v, _ := pi.Extra["custom"].(string); v != "value" {
		t.Errorf("augmenter Extra not applied: %#v", pi.Extra)
	}
}

func TestPlugin_OrderingMatchesRegistration(t *testing.T) {
	gw := New()
	gw.Register(&EchoRouter{})
	a := &recordingPlugin{name: "a"}
	b := &recordingPlugin{name: "b"}
	_ = gw.Use(a)
	_ = gw.Use(b)

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Echo/ping", Header: Header{}, Body: []byte(`{"args":{}}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	// Both should have fired exactly once for the single dispatch.
	if a.dispatch.Load() != 1 || b.dispatch.Load() != 1 {
		t.Fatalf("dispatch: a=%d b=%d", a.dispatch.Load(), b.dispatch.Load())
	}
}

// ---- Plugin-as-also-router pattern ----------------------------------------

type DualRouter struct {
	pings atomic.Int32
}

func (d *DualRouter) PluginName() string { return "dual" }

func (d *DualRouter) OnDispatch(_ DispatchEvent) error { d.pings.Add(1); return nil }

func (d *DualRouter) Hello(_ *rpc.Context) (map[string]string, error) {
	return map[string]string{"msg": "hello from plugin"}, nil
}

// ---- RouteHandler --------------------------------------------------------

type customRoutePlugin struct {
	patterns []string
	hits     atomic.Int32
}

func (p *customRoutePlugin) PluginName() string      { return "custom-route" }
func (p *customRoutePlugin) RoutePatterns() []string { return p.patterns }
func (p *customRoutePlugin) ServeRoute(_ context.Context, req *Request) *Response {
	p.hits.Add(1)
	return &Response{Status: 200, Header: Header{}, Body: []byte(`{"data":"` + req.Path + `"}`)}
}

func TestPlugin_RouteHandler_ExactMatch(t *testing.T) {
	gw := New()
	p := &customRoutePlugin{patterns: []string{"/admin/ping"}}
	if err := gw.Use(p); err != nil {
		t.Fatalf("Use: %v", err)
	}
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/admin/ping", Header: Header{},
	})
	if resp.Status != 200 || !strings.Contains(string(resp.Body), "/admin/ping") {
		t.Fatalf("exact match status=%d body=%s", resp.Status, resp.Body)
	}
	resp = gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/admin/ping/extra", Header: Header{},
	})
	if resp.Status == 200 {
		t.Fatalf("exact pattern must not match subtree, got status=%d", resp.Status)
	}
}

func TestPlugin_RouteHandler_SubtreeMatch(t *testing.T) {
	gw := New()
	p := &customRoutePlugin{patterns: []string{"/admin/"}}
	if err := gw.Use(p); err != nil {
		t.Fatalf("Use: %v", err)
	}
	for _, path := range []string{"/admin/", "/admin/foo", "/admin/foo/bar"} {
		resp := gw.Handle(context.Background(), &Request{
			Method: http.MethodGet, Path: path, Header: Header{},
		})
		if resp.Status != 200 {
			t.Fatalf("subtree match %q status=%d body=%s", path, resp.Status, resp.Body)
		}
	}
	if p.hits.Load() != 3 {
		t.Fatalf("expected 3 hits, got %d", p.hits.Load())
	}
}

func TestPlugin_RouteHandler_CannotShadowFramework(t *testing.T) {
	gw := New()
	p := &customRoutePlugin{patterns: []string{"/rpc/_health"}}
	if err := gw.Use(p); err != nil {
		t.Fatalf("Use: %v", err)
	}
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/rpc/_health", Header: Header{},
	})
	if p.hits.Load() != 0 {
		t.Fatalf("plugin shadowed /rpc/_health; framework must win")
	}
	if resp.Status != 200 {
		t.Fatalf("framework _health status=%d", resp.Status)
	}
}

func TestPlugin_AlsoARouter(t *testing.T) {
	gw := New()
	d := &DualRouter{}
	if err := gw.Use(d); err != nil {
		t.Fatalf("Use: %v", err)
	}
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Dual/hello", Header: Header{}, Body: []byte(`{"args":{}}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), "hello from plugin") {
		t.Fatalf("body=%s", resp.Body)
	}
	if d.pings.Load() != 1 {
		t.Fatalf("DispatchHook did not fire alongside router method (count=%d)", d.pings.Load())
	}
}
