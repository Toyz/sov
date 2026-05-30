// Nested PEMM: TWO gateways in ONE binary linked via localpeer. Calls
// across them bypass HTTP entirely — DispatchEvent.Mode == "peer"
// surfaces the seam in the audit log. Same handler code as monolith;
// the only delta is that admin services live on a separate Gateway.
//
//	go run ./examples/chirp/cmd/nested-pemm
//	curl -s -X POST http://localhost:8080/rpc/Chirp/list -d '{"args":{}}'
//
// Public hosts Chirp + Feed; admin hosts Auth + Authz + User. The
// public gateway routes Auth/Authz/User to admin via localpeer — no
// network hop between them.
package main

import (
	"context"
	"io"
	"log"
	"os"

	"github.com/Toyz/sov"
	"github.com/Toyz/sov/examples/chirp/handlers/auth"
	"github.com/Toyz/sov/examples/chirp/handlers/authz"
	"github.com/Toyz/sov/examples/chirp/handlers/chirps"
	"github.com/Toyz/sov/examples/chirp/handlers/feed"
	"github.com/Toyz/sov/examples/chirp/handlers/users"
	"github.com/Toyz/sov/gateway/builtin/audit"
	"github.com/Toyz/sov/gateway/builtin/explorer"
	"github.com/Toyz/sov/gateway/builtin/introspect"
	"github.com/Toyz/sov/gateway/builtin/manifest"
)

func main() {
	// Admin gateway — Auth + Authz + User. Audit goes to discard so
	// the demo's stdout doesn't double-stream events. Audit is opt-in
	// (not in the base preset), so wire it explicitly.
	admin := sov.NewMonolith(sov.MonolithConfig{})
	admin.MustUse(audit.New(audit.Config{Out: io.Discard}))
	admin.Register(&auth.AuthRouter{
		Credentials: auth.NewCredentialStore(),
		Sessions:    auth.NewSessionStore(),
	})
	admin.Register(authz.NewAuthzRouter())
	admin.Register(&users.UserRouter{Store: users.NewMemoryStore()})

	// Public gateway — Chirp + Feed, plus a localpeer pointer at
	// admin for the Auth/Authz/User services. Audit on stdout so the
	// demo shows mode=peer for cross-gateway calls. Explorer + manifest
	// + audit are opt-in (not in the base preset).
	public := sov.NewMonolith(sov.MonolithConfig{})
	public.MustUse(explorer.New(explorer.Config{}))
	public.MustUse(introspect.New())
	public.MustUse(manifest.New(manifest.Config{}))
	public.MustUse(audit.New(audit.Config{Out: os.Stdout}))
	public.Register(&chirps.ChirpRouter{Store: chirps.NewMemoryStore()})
	public.Register(&feed.FeedRouter{Client: feed.NewClientAdapter(public.LocalClient())})

	// Nested PEMM wiring: public's resolver chain gains Auth/Authz/User
	// → admin. dispatchRemote will see Endpoint.Peer non-nil and call
	// admin.Handle directly (no HTTP). Mode label is "peer".
	public.LinkPeer(admin, "Auth", "Authz", "User")

	// admin still binds to a separate port so external clients can
	// hit it directly if they want. Public is the canonical entry.
	go func() {
		log.Printf("admin gateway on :9090 (Auth + Authz + User)")
		if err := admin.Run(context.Background(), ":9090"); err != nil {
			log.Fatalf("admin: %v", err)
		}
	}()

	log.Printf("public gateway on :8080 (Chirp + Feed, routes Auth/Authz/User to admin in-process via localpeer)")
	if err := public.Run(context.Background(), ":8080"); err != nil {
		log.Fatal(err)
	}
}
