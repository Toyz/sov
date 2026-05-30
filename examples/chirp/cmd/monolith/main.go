// Monolith binary: gateway + all chirp services + the authz policy in
// one process. PEMM Day 1. Same handler source as the per-service mesh
// pods; only the cmd wiring differs.
//
// Plugin configuration goes through plain structs — operators can
// build them from yaml/toml/env/viper. Below uses literals for the
// demo; mesh examples show viper.
package main

import (
	"context"
	"log"
	"os"

	"github.com/Toyz/sov"
	"github.com/Toyz/sov/examples/chirp/handlers/auth"
	"github.com/Toyz/sov/examples/chirp/handlers/authz"
	"github.com/Toyz/sov/examples/chirp/handlers/chirps"
	"github.com/Toyz/sov/examples/chirp/handlers/feed"
	"github.com/Toyz/sov/examples/chirp/handlers/users"
	"github.com/Toyz/sov/gateway/builtin/audit"
	authplugin "github.com/Toyz/sov/gateway/builtin/auth"
)

func main() {
	gw := sov.NewMonolith(sov.MonolithConfig{
		Audit: audit.Config{Out: os.Stdout},
	})

	// Chirp business services.
	gw.Register(&auth.AuthRouter{
		Credentials: auth.NewCredentialStore(),
		Sessions:    auth.NewSessionStore(),
	})
	gw.Register(authz.NewAuthzRouter())
	gw.Register(&users.UserRouter{Store: users.NewMemoryStore()})
	gw.Register(&chirps.ChirpRouter{Store: chirps.NewMemoryStore()})
	gw.Register(&feed.FeedRouter{Client: feed.NewClientAdapter(gw.LocalClient())})

	// Domain plugin: AuthTranslator stamps legacy X-Forwarded-User
	// headers on proxy hops for brownfield downstreams. Not in the
	// preset because most consumers don't need it.
	gw.MustUse(authplugin.New(authplugin.Config{}))

	addr := os.Getenv("SOV_LISTEN")
	if addr == "" {
		addr = ":8080"
	}
	log.Printf("chirp monolith on %s (Auth + Authz + User + Chirp + Feed in-process)", addr)
	if err := gw.Run(context.Background(), addr); err != nil {
		log.Fatal(err)
	}
}
