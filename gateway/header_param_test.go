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

type HeaderOnlyRouter struct{}

type headerOnlyParams struct {
	Tenant string `sov:"header=X-Tenant-Id"`
}

func (HeaderOnlyRouter) Do(_ *rpc.Context, p *headerOnlyParams) (string, error) { return p.Tenant, nil }

// A method whose ONLY params are header-bound has no JSON body shape, so the
// type catalog must not emit an (empty) Params type for it — otherwise every
// generator forces callers to send a meaningless {} for a no-body method.
func TestHeaderParam_HeaderOnlyMethodEmitsNoParamsType(t *testing.T) {
	gw := gwtest.New()
	gw.Register(&HeaderOnlyRouter{})
	resp := gw.IntrospectBody(context.Background(), &Request{Header: Header{}})
	var rep IntrospectReport
	if err := json.Unmarshal(resp.Body, &rep); err != nil {
		t.Fatalf("introspect decode: %v", err)
	}
	if _, ok := rep.Types["HeaderOnly.DoParams"]; ok {
		t.Fatalf("header-only method must not create a Params type: %v", rep.Types)
	}
}

type tenantRewriteParser struct{}

func (tenantRewriteParser) PluginName() string { return "tenant-rewrite" }
func (tenantRewriteParser) ParseHeaders(req *Request) *rpc.Error {
	if req.Header.Get("X-Tenant-Id") == "raw-alias" {
		req.Header.Set("X-Tenant-Id", "canonical-tenant")
	}
	return nil
}

type recordingHeaderAuthz struct{ seen *string }

func (a recordingHeaderAuthz) Check(_ *rpc.Context, p *CheckParams) (*AuthzDecision, error) {
	if a.seen != nil {
		*a.seen = p.Headers["X-Tenant-Id"]
	}
	return &AuthzDecision{Allow: true}, nil
}

type PermTenantRouter struct{}

type permTenantParams struct {
	_      struct{} `sov:"perm=read"`
	Note   string   `json:"note"`
	Tenant string   `sov:"header=X-Tenant-Id"`
}

func (PermTenantRouter) Echo(_ *rpc.Context, p *permTenantParams) (string, error) {
	return p.Note + "@" + p.Tenant, nil
}

// A HeaderParser that rewrites a header must NOT make a header= param bind a
// different value than the authz gate (AuthzService.Check) authorized. Both see
// the pre-parser value — closing the confused-deputy divergence. Before the
// fix the handler bound the parser-rewritten "canonical-tenant" while Check
// authorized "raw-alias".
func TestHeaderParam_AuthzAndBindAgreeUnderHeaderParser(t *testing.T) {
	var authzSaw string
	gw := gwtest.New()
	gw.Register(&PermTenantRouter{})
	gw.RegisterAuthz(&recordingHeaderAuthz{seen: &authzSaw})
	gw.MustUse(&tenantRewriteParser{})

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/PermTenant/echo",
		Header: Header{"Content-Type": "application/json", "X-Tenant-Id": "raw-alias"},
		Body:   []byte(`{"args":{"note":"hi"}}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	if authzSaw != "raw-alias" {
		t.Fatalf("authz saw %q, want raw-alias (pre-parser)", authzSaw)
	}
	if !strings.Contains(string(resp.Body), "hi@raw-alias") {
		t.Fatalf("handler bound the parser-rewritten value, not what authz saw: %s", resp.Body)
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
