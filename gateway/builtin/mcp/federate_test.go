package mcp_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/introspect"
	"github.com/Toyz/sov/gateway/builtin/mcp"
	"github.com/Toyz/sov/gateway/builtin/registry"
	rpcsurface "github.com/Toyz/sov/gateway/builtin/rpc"
	"github.com/Toyz/sov/rpc"
)

// serveGateway exposes a gateway as an HTTP endpoint so another gateway can
// federate to it over real HTTP — introspect fan-out AND proxied dispatch.
func serveGateway(gw *gateway.Gateway) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		hdr := gateway.Header{}
		for k := range r.Header {
			hdr.Set(k, r.Header.Get(k))
		}
		resp := gw.Handle(r.Context(), &gateway.Request{
			Method: r.Method, Path: r.URL.Path, Header: hdr, Body: body, RemoteIP: r.RemoteAddr,
		})
		if resp == nil {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		for k, v := range resp.Header {
			w.Header().Set(k, v)
		}
		status := resp.Status
		if status == 0 {
			status = http.StatusOK
		}
		w.WriteHeader(status)
		_, _ = w.Write(resp.Body)
	}))
}

// FederatedNoteRouter is node B's tool service.
type FederatedNoteRouter struct{ mcp.Tool }

type fnParams struct {
	ID string `json:"id"`
}

func (FederatedNoteRouter) Read(_ *rpc.Context, p *fnParams) (string, error) {
	return "remote-note:" + p.ID, nil
}

// True meshed MCP with NO MCP-specific mesh code: node A runs the MCP surface
// but the tool service lives on node B. A discovers B's tool through the
// FEDERATED introspect catalog (B's mcp tagged it; the tag rode the wire) and
// routes the call to B through the mesh Dispatch fabric.
func TestMCP_FederatedToolDiscoveryAndCall(t *testing.T) {
	// node B: the tool service, exposed over HTTP.
	gwB := gateway.New()
	gwB.Register(&FederatedNoteRouter{})
	gwB.MustUse(rpcsurface.New())      // B serves /rpc (A proxies business calls here)
	gwB.MustUse(mcp.New(mcp.Config{})) // tags B's tool routers in introspect
	gwB.MustUse(introspect.New())      // opens B's public /rpc/_introspect for the fan-out
	srvB := serveGateway(gwB)
	defer srvB.Close()

	// node A: MCP surface + registry aggregator. A has NO local tool router.
	gwA := gateway.New()
	gwA.MustUse(registry.New(registry.Config{AllowedNames: []string{"FederatedNote"}}))
	gwA.MustUse(mcp.New(mcp.Config{}))
	gwA.RegisterRemote("FederatedNote", srvB.URL, time.Minute, gateway.RemoteOptions{Introspect: true})

	// Discovery: A's tools/list includes B's tool, pulled from the federated
	// catalog even though FederatedNote is not in A's local engine.
	names := toolNames(mcpPost(t, gwA, "", "tools/list", map[string]any{}))
	if !names["FederatedNote.read"] {
		t.Fatalf("node A did not discover node B's federated tool: %v", names)
	}

	// Dispatch: A routes the tool call across the mesh to B and gets B's result.
	call := mcpPost(t, gwA, "", "tools/call", map[string]any{
		"name": "FederatedNote.read", "arguments": map[string]any{"id": "9"},
	})
	res, _ := call["result"].(map[string]any)
	if res["isError"] == true {
		t.Fatalf("federated tools/call flagged error: %v", res)
	}
	content, _ := res["content"].([]any)
	first, _ := content[0].(map[string]any)
	if !strings.Contains(first["text"].(string), "remote-note:9") {
		t.Fatalf("node A did not route the tool call to node B: %v", res)
	}
}
