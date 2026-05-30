// Tiered mesh — master/prime gateway. Hosts Auth + Authz; routes
// everything else through federated team gateways that register
// themselves with `federate:true, services:[...]`.
package main

import (
	"context"
	"log"
	"os"

	"github.com/Toyz/sov"
	"github.com/Toyz/sov/examples/chirp/handlers/auth"
	"github.com/Toyz/sov/examples/chirp/handlers/authz"
	"github.com/Toyz/sov/gateway/builtin/audit"
	"github.com/Toyz/sov/gateway/builtin/explorer"
	"github.com/Toyz/sov/gateway/builtin/hmacseal"
	"github.com/Toyz/sov/gateway/builtin/introspect"
	"github.com/Toyz/sov/gateway/builtin/manifest"
	"github.com/Toyz/sov/gateway/builtin/meshsecret"
	"github.com/Toyz/sov/gateway/builtin/registry"
)

func main() {
	addr := env("SOV_LISTEN", ":8080")
	advertiseURL := env("SOV_ADVERTISE", "http://prime:8080")
	gw := sov.NewRegistry(sov.RegistryConfig{
		Registry:   registry.Config{AllowedNames: []string{"Auth", "Authz", "User", "Chirp", "Feed"}},
		HMACSeal:   hmacseal.Config{Secret: []byte(env("SOV_HMAC_SECRET", "demo-only-secret"))},
		MeshSecret: meshsecret.Config{Secret: []byte(env("SOV_MESH_SECRET", "demo-only-mesh-secret"))},
	}, sov.WithAdvertiseURL(advertiseURL))

	// Opt-in observability / info-disclosure plugins (not in the base
	// preset): explorer UI, plugin manifest, and the audit log.
	gw.MustUse(explorer.New(explorer.Config{}))
	gw.MustUse(introspect.New())
	gw.MustUse(manifest.New(manifest.Config{}))
	gw.MustUse(audit.New(audit.Config{Out: os.Stdout}))

	gw.Register(&auth.AuthRouter{
		Credentials: auth.NewCredentialStore(),
		Sessions:    auth.NewSessionStore(),
	})
	gw.Register(authz.NewAuthzRouter())

	log.Printf("chirp prime gateway on %s — federation enabled", addr)
	if err := gw.Run(context.Background(), addr); err != nil {
		log.Fatal(err)
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
