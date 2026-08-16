package gateway

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
)

type notReadyPlugin struct{ reason string }

func (p *notReadyPlugin) PluginName() string          { return "test-not-ready" }
func (p *notReadyPlugin) Ready(context.Context) error { return errors.New(p.reason) }

type readyPlugin struct{}

func (readyPlugin) PluginName() string          { return "test-ready" }
func (readyPlugin) Ready(context.Context) error { return nil }

func TestReady_StartingIs503(t *testing.T) {
	gw := New() // never ListenAndServe'd → still starting
	resp := gw.handleReady(context.Background())
	if resp.Status != http.StatusServiceUnavailable {
		t.Fatalf("starting should be 503, got %d", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "starting") {
		t.Fatalf("body = %s", resp.Body)
	}
}

func TestReady_ReadyIs200(t *testing.T) {
	gw := New()
	gw.serving.Store(servingReady)
	resp := gw.handleReady(context.Background())
	if resp.Status != http.StatusOK {
		t.Fatalf("ready should be 200, got %d body=%s", resp.Status, resp.Body)
	}
}

func TestReady_DrainingIs503(t *testing.T) {
	gw := New()
	gw.serving.Store(servingDraining)
	resp := gw.handleReady(context.Background())
	if resp.Status != http.StatusServiceUnavailable || !strings.Contains(string(resp.Body), "draining") {
		t.Fatalf("draining should be 503/draining, got %d body=%s", resp.Status, resp.Body)
	}
}

func TestReady_ContributorGatesAndReports(t *testing.T) {
	gw := New()
	gw.serving.Store(servingReady)
	if err := gw.Use(&notReadyPlugin{reason: "cache warming"}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	resp := gw.handleReady(context.Background())
	if resp.Status != http.StatusServiceUnavailable {
		t.Fatalf("a not-ready contributor must 503, got %d", resp.Status)
	}
	if !strings.Contains(string(resp.Body), "cache warming") {
		t.Fatalf("reason not surfaced: %s", resp.Body)
	}
}

func TestReady_ContributorReadyPasses(t *testing.T) {
	gw := New()
	gw.serving.Store(servingReady)
	if err := gw.Use(readyPlugin{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	if resp := gw.handleReady(context.Background()); resp.Status != http.StatusOK {
		t.Fatalf("ready contributor should keep 200, got %d body=%s", resp.Status, resp.Body)
	}
}

func TestReady_EndpointRoutedAndUnauthed(t *testing.T) {
	gw := New()
	gw.serving.Store(servingReady)
	// /rpc/_ready is a framework path — reached before auth/authz, no gating.
	resp := gw.frameworkEndpoint(context.Background(), &Request{Method: http.MethodGet, Path: "/rpc/_ready", Header: Header{}})
	if resp == nil || resp.Status != http.StatusOK {
		t.Fatalf("/rpc/_ready should route to 200 when ready, got %v", resp)
	}
}
