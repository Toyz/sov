// Hybrid binary: ONE gateway hosting Auth + Authz + User in-process
// while routing Chirp + Feed to remote pods registered via _register.
package main

import (
	"context"
	"log"
	"os"

	"github.com/Toyz/sov"
	"github.com/Toyz/sov/examples/chirp/handlers/auth"
	"github.com/Toyz/sov/examples/chirp/handlers/authz"
	"github.com/Toyz/sov/examples/chirp/handlers/users"
	"github.com/Toyz/sov/gateway/builtin/audit"
	"github.com/Toyz/sov/gateway/builtin/explorer"
	"github.com/Toyz/sov/gateway/builtin/introspect"
	"github.com/Toyz/sov/gateway/builtin/manifest"
)

func main() {
	gw := sov.NewHybrid(sov.HybridConfig{})

	// Opt-in observability / info-disclosure plugins (not in the base
	// preset): explorer UI, plugin manifest, and the audit log.
	gw.MustUse(explorer.New(explorer.Config{}))
	gw.MustUse(introspect.New())
	gw.MustUse(manifest.New(manifest.Config{}))
	gw.MustUse(audit.New(audit.Config{Out: os.Stdout}))

	// Identity propagates to the remote Chirp+Feed pods with no extra
	// wiring: the pods run WithTrustUpstreamClaims(true) and trust their
	// gateway by default (network-isolated mesh). Same as monolith and
	// mesh — no per-link seal config. To require cryptographic proof on an
	// untrusted network, add gw.Use(hmacseal.New(...)) here and on the pods
	// with a shared secret.

	gw.Register(&auth.AuthRouter{
		Credentials: auth.NewCredentialStore(),
		Sessions:    auth.NewSessionStore(),
	})
	gw.Register(authz.NewAuthzRouter())
	gw.Register(&users.UserRouter{Store: users.NewMemoryStore()})

	log.Printf("chirp hybrid on :8080 — Auth+Authz+User in-process, Chirp+Feed routed remote via _register")
	if err := gw.Run(context.Background(), ":8080"); err != nil {
		log.Fatal(err)
	}
}
