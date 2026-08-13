package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/internal/gwtest"
	"github.com/Toyz/sov/rpc"
)

type TenantRouter struct{}

type tenantParams struct {
	Note   string `json:"note"`
	Tenant string `sov:"header=X-Tenant-Id"`
}

func (TenantRouter) Echo(_ *rpc.Context, p *tenantParams) (string, error) {
	return p.Note + "@" + p.Tenant, nil
}

// The gateway populates the header getter, so a header= param binds from the
// inbound request header end-to-end over the /rpc surface.
func TestHeaderParam_GatewayBindsFromInboundHeader(t *testing.T) {
	gw := gwtest.New()
	gw.Register(&TenantRouter{})
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Tenant/echo",
		Header: Header{"Content-Type": "application/json", "X-Tenant-Id": "acme"},
		Body:   []byte(`{"args":{"note":"hi"}}`),
	})
	if resp.Status != 200 || !strings.Contains(string(resp.Body), "hi@acme") {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
}

// An absent header leaves the optional field at its zero value (no error).
func TestHeaderParam_GatewayOptionalAbsentZeroes(t *testing.T) {
	gw := gwtest.New()
	gw.Register(&TenantRouter{})
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Tenant/echo",
		Header: Header{"Content-Type": "application/json"}, // no X-Tenant-Id
		Body:   []byte(`{"args":{"note":"hi"}}`),
	})
	if resp.Status != 200 || !strings.Contains(string(resp.Body), `"hi@"`) {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
}

// The header survives a cross-gateway hop: the edge forwards the request (and
// its headers) to the LinkPeer'd node, whose dispatchLocal populates the getter
// there, so the header= param binds on the peer. Same code, different gateway.
func TestHeaderParam_BindsAcrossLinkPeer(t *testing.T) {
	peer := gwtest.New()
	peer.Register(&TenantRouter{})
	edge := gwtest.New()
	edge.LinkPeer(peer, "Tenant")

	resp := edge.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Tenant/echo",
		Header: Header{"Content-Type": "application/json", "X-Tenant-Id": "acme"},
		Body:   []byte(`{"args":{"note":"hi"}}`),
	})
	if resp.Status != 200 || !strings.Contains(string(resp.Body), "hi@acme") {
		t.Fatalf("header not bound across LinkPeer: status=%d body=%s", resp.Status, resp.Body)
	}
}

// The introspection contract the explorer/codegen consume: the generated
// request TYPE (JSON shape) omits the header field, while the METHOD params
// keep it flagged Source="header" with its header name.
func TestHeaderParam_IntrospectSplitsHeaderFromType(t *testing.T) {
	gw := gwtest.New()
	gw.Register(&TenantRouter{})
	resp := gw.IntrospectBody(context.Background(), &Request{Header: Header{}})
	if resp.Status != 200 {
		t.Fatalf("introspect status=%d", resp.Status)
	}
	var rep IntrospectReport
	if err := json.Unmarshal(resp.Body, &rep); err != nil {
		t.Fatalf("introspect decode: %v", err)
	}

	// Request type = JSON shape → no header field.
	td, ok := rep.Types["Tenant.EchoParams"]
	if !ok {
		t.Fatalf("Tenant.EchoParams type missing from catalog")
	}
	for _, f := range td.Fields {
		if f.Source == "header" || f.Header != "" {
			t.Fatalf("header field leaked into the type catalog: %+v", td.Fields)
		}
	}

	// Method params → header field present, flagged.
	found := false
	for _, rd := range rep.Services["Tenant"] {
		for _, m := range rd.Methods {
			if m.Method != "echo" {
				continue
			}
			for _, p := range m.Params {
				if p.Source == "header" && p.Header == "X-Tenant-Id" {
					found = true
				}
			}
		}
	}
	if !found {
		t.Fatalf("method params lost the header field (Source/Header)")
	}
}
