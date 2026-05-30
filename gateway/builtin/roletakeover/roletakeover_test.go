package roletakeover_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/registry"
	"github.com/Toyz/sov/gateway/builtin/roletakeover"
)

func TestRoleTakeover_PermitsCrossName(t *testing.T) {
	gw := gateway.New()
	_ = gw.Use(registry.New(registry.Config{}))
	_ = gw.Use(roletakeover.New(roletakeover.Config{}))
	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/_register", Header: gateway.Header{},
		Body: []byte(`{"name":"AuthA","address":"http://a:1","auth":true,"verify":"verify"}`),
	})
	if resp.Status != 200 {
		t.Fatalf("first: %d %s", resp.Status, resp.Body)
	}
	resp = gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/_register", Header: gateway.Header{},
		Body: []byte(`{"name":"AuthB","address":"http://b:1","auth":true,"verify":"verify"}`),
	})
	if resp.Status != 200 {
		t.Fatalf("second: %d %s", resp.Status, resp.Body)
	}
}

func TestRoleTakeover_HookIsMeshConflictPolicy(t *testing.T) {
	gw := gateway.New()
	_ = gw.Use(registry.New(registry.Config{}))
	_ = gw.Use(roletakeover.New(roletakeover.Config{}))
	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodGet, Path: "/rpc/_introspect", Header: gateway.Header{},
	})
	var rpt gateway.IntrospectReport
	_ = json.Unmarshal(resp.Body, &rpt)
	for _, p := range rpt.Plugins {
		if p.Name != "role-takeover" {
			continue
		}
		if !contains(p.Hooks, "MeshConflictPolicy") {
			t.Errorf("plugin %q hooks=%v; MeshConflictPolicy missing", p.Name, p.Hooks)
		}
		if contains(p.Hooks, "ConfigApplier") {
			t.Errorf("plugin %q now owns behavior — ConfigApplier must NOT be listed, got %v", p.Name, p.Hooks)
		}
	}
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
