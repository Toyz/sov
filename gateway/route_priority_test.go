package gateway_test

import (
	"context"
	"net/http"
	"testing"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/internal/gwtest"
)

// routeSpy is a RouteHandler that reports which plugin won (its label in the
// body), implements RoutePrioritizer, and can DECLINE a specific path (return
// nil) to exercise fall-through.
type routeSpy struct {
	label     string
	patterns  []string
	priority  int
	declineOn string
}

func (p *routeSpy) PluginName() string      { return "spy-" + p.label }
func (p *routeSpy) RoutePatterns() []string { return p.patterns }
func (p *routeSpy) RoutePriority() int      { return p.priority }
func (p *routeSpy) ServeRoute(_ context.Context, req *Request) *Response {
	if p.declineOn != "" && req.Path == p.declineOn {
		return nil // decline -> fall through
	}
	return &Response{Status: 200, Header: Header{}, Body: []byte(p.label)}
}

func getBody(t *testing.T, gw *Gateway, path string) string {
	t.Helper()
	resp := gw.Handle(context.Background(), &Request{Method: http.MethodGet, Path: path, Header: Header{}})
	return string(resp.Body)
}

// A higher RoutePriority wins over a strictly longer pattern.
func TestRoutePriority_OverridesLongerPattern(t *testing.T) {
	gw := gwtest.New()
	long := &routeSpy{label: "long", patterns: []string{"/foo/bar/"}, priority: 0}
	broad := &routeSpy{label: "broad", patterns: []string{"/"}, priority: 10}
	if err := gw.Use(long); err != nil {
		t.Fatalf("use long: %v", err)
	}
	if err := gw.Use(broad); err != nil {
		t.Fatalf("use broad: %v", err)
	}
	if got := getBody(t, gw, "/foo/bar/x"); got != "broad" {
		t.Fatalf("high-priority broad route should beat the longer pattern; got %q", got)
	}
}

// With equal priority (default 0), the LONGEST pattern wins regardless of
// registration order — broad is registered first but the longer one still wins.
func TestRoutePriority_DefaultLongestWinsOverRegOrder(t *testing.T) {
	gw := gwtest.New()
	broad := &routeSpy{label: "broad", patterns: []string{"/"}}
	long := &routeSpy{label: "long", patterns: []string{"/foo/bar/"}}
	if err := gw.Use(broad); err != nil { // registered FIRST
		t.Fatalf("use broad: %v", err)
	}
	if err := gw.Use(long); err != nil {
		t.Fatalf("use long: %v", err)
	}
	if got := getBody(t, gw, "/foo/bar/x"); got != "long" {
		t.Fatalf("longest pattern should win over an earlier-registered broad one; got %q", got)
	}
}

// Two identical patterns at equal priority resolve to the earliest-registered.
func TestRoutePriority_TieGoesToRegistrationOrder(t *testing.T) {
	gw := gwtest.New()
	first := &routeSpy{label: "first", patterns: []string{"/api/"}}
	second := &routeSpy{label: "second", patterns: []string{"/api/"}}
	if err := gw.Use(first); err != nil {
		t.Fatalf("use first: %v", err)
	}
	if err := gw.Use(second); err != nil {
		t.Fatalf("use second: %v", err)
	}
	if got := getBody(t, gw, "/api/x"); got != "first" {
		t.Fatalf("equal patterns should resolve to earliest-registered; got %q", got)
	}
}

// A high-priority handler that DECLINES a path falls through to the next match;
// paths it does not decline it still claims.
func TestRoutePriority_DeclineFallsThrough(t *testing.T) {
	gw := gwtest.New()
	broad := &routeSpy{label: "broad", patterns: []string{"/"}, priority: 100, declineOn: "/special"}
	specific := &routeSpy{label: "specific", patterns: []string{"/special"}}
	if err := gw.Use(broad); err != nil {
		t.Fatalf("use broad: %v", err)
	}
	if err := gw.Use(specific); err != nil {
		t.Fatalf("use specific: %v", err)
	}
	if got := getBody(t, gw, "/special"); got != "specific" {
		t.Fatalf("declined path should fall through to the next match; got %q", got)
	}
	if got := getBody(t, gw, "/other"); got != "broad" {
		t.Fatalf("non-declined path should be claimed by the high-priority handler; got %q", got)
	}
}
