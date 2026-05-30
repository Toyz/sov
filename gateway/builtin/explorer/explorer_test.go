package explorer_test

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/explorer"
)

func TestExplorer_RoutesAndRendersHTML(t *testing.T) {
	gw := gateway.New()
	if err := gw.Use(explorer.New(explorer.Config{})); err != nil {
		t.Fatalf("Use Explorer: %v", err)
	}
	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodGet, Path: "/rpc/_explorer/", Header: gateway.Header{},
	})
	if resp.Status != 200 {
		t.Fatalf("explorer status=%d body=%s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), "<!DOCTYPE html>") {
		t.Fatalf("expected HTML, got %s", resp.Body)
	}
}
