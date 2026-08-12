// mcpmesh: ONE binary, TWO nodes, proving RPC and MCP mesh over the SAME code.
//
//   - Node B (127.0.0.1:9101) hosts the chirp services. User/Chirp/Feed embed
//     mcp.Tool, so the exact same struct serves /rpc AND is an MCP tool source.
//   - Node A (127.0.0.1:9100) is a thin edge: registry + mcp + introspect. It
//     federates B's services and owns NO business logic. Auth/authz are enforced
//     by B when A proxies to it.
//
// So node A serves BOTH surfaces, meshed:
//   - /rpc/{service}/{method}  -> proxied to B          (RPC meshes)
//   - /mcp tools/list          -> B's tools via the federated introspect catalog
//                                                        (MCP discovery meshes)
//   - /mcp tools/call          -> routed to B via Dispatch (MCP dispatch meshes)
//
// None of this has MCP-specific mesh code: MCP rides the same Dispatch fabric
// and the federated Surfaces tag that /rpc and introspect already use.
//
//	go run ./examples/chirp/cmd/mcpmesh
//	curl -s localhost:9100/mcp -d '{"jsonrpc":"2.0","id":1,"method":"tools/list"}'
//	curl -s localhost:9100/rpc/Chirp/list -d '{"args":[{"limit":50}]}'
package main

import (
	"context"
	"log"
	"net/http"
	"time"

	"github.com/Toyz/sov/examples/chirp/handlers/auth"
	"github.com/Toyz/sov/examples/chirp/handlers/authz"
	"github.com/Toyz/sov/examples/chirp/handlers/chirps"
	"github.com/Toyz/sov/examples/chirp/handlers/feed"
	"github.com/Toyz/sov/examples/chirp/handlers/users"
	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/introspect"
	"github.com/Toyz/sov/gateway/builtin/mcp"
	"github.com/Toyz/sov/gateway/builtin/registry"
)

// The tool wrappers: embed the chirp router (its methods promote onto the
// wrapper) AND mcp.Tool (the marker). The wrapper's type name is the wire name
// — "User"/"Chirp"/"Feed", no "Router" suffix needed now — matching what the
// authz policy keys on and what Feed's in-process client calls. The SAME struct
// serves /rpc and MCP.
type (
	User struct {
		*users.UserRouter
		mcp.Tool
	}
	Chirp struct {
		*chirps.ChirpRouter
		mcp.Tool
	}
	Feed struct {
		*feed.FeedRouter
		mcp.Tool
	}
)

const (
	bAddr = "127.0.0.1:9101" // node B: chirp services
	aAddr = "127.0.0.1:9100" // node A: edge (RPC + MCP)
)

func main() {
	ctx := context.Background()

	// ---- node B: the chirp services (RPC + MCP tool sources) ----
	gwB := gateway.New()
	gwB.Register(&auth.AuthRouter{Credentials: auth.NewCredentialStore(), Sessions: auth.NewSessionStore()})
	gwB.Register(authz.NewAuthzRouter())
	gwB.Register(&User{UserRouter: &users.UserRouter{Store: users.NewMemoryStore()}})
	gwB.Register(&Chirp{ChirpRouter: &chirps.ChirpRouter{Store: chirps.NewMemoryStore()}})
	gwB.Register(&Feed{FeedRouter: &feed.FeedRouter{Client: feed.NewClientAdapter(gwB.LocalClient())}})
	gwB.MustUse(mcp.New(mcp.Config{ServerName: "chirp-node-b"})) // tags B's tool routers in introspect
	gwB.MustUse(introspect.New())                               // exposes B's /rpc/_introspect for the fan-out
	go func() { log.Fatal(gwB.ListenAndServe(ctx, bAddr)) }()
	waitReady("http://" + bAddr)

	// ---- node A: the edge — serves RPC + MCP, business lives on B ----
	gwA := gateway.New()
	gwA.MustUse(registry.New(registry.Config{AllowedNames: []string{"Auth", "Authz", "User", "Chirp", "Feed"}}))
	gwA.MustUse(mcp.New(mcp.Config{ServerName: "chirp-edge-a"}))
	gwA.MustUse(introspect.New())
	base := "http://" + bAddr
	for _, svc := range []string{"Auth", "Authz", "User", "Chirp", "Feed"} {
		gwA.RegisterRemote(svc, base, time.Minute, gateway.RemoteOptions{Introspect: true})
	}

	log.Printf("mcpmesh up: edge A %s (/rpc + /mcp) -> services B %s", aAddr, bAddr)
	log.Printf("  tools:  curl -s localhost:9100/mcp -d '{\"jsonrpc\":\"2.0\",\"id\":1,\"method\":\"tools/list\"}'")
	log.Printf("  rpc:    curl -s localhost:9100/rpc/Chirp/list -d '{\"args\":[{\"limit\":50}]}'")
	log.Fatal(gwA.ListenAndServe(ctx, aAddr))
}

// waitReady blocks until base answers /rpc/_health (node B is listening).
func waitReady(base string) {
	for i := 0; i < 200; i++ {
		resp, err := http.Post(base+"/rpc/_health", "application/json", nil)
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode < 500 {
				return
			}
		}
		time.Sleep(25 * time.Millisecond)
	}
	log.Fatalf("node B never became ready at %s", base)
}
