// Tiered mesh — team-feed gateway. Federates its LIVE service set
// (Chirp + Feed sub-pods) to prime via FederateAll — no hand-maintained
// list, so prime's introspect/health never drift as sub-pods come and go.
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/Toyz/sov"
	"github.com/Toyz/sov/gateway/builtin/hmacseal"
	"github.com/Toyz/sov/gateway/builtin/introspect"
	"github.com/Toyz/sov/gateway/builtin/meshsecret"
	"github.com/Toyz/sov/gateway/builtin/registry"
)

func main() {
	advertiseURL := env("SOV_ADVERTISE", "http://team-feed:9100")
	// Two independent join secrets — separate trust domains per tier:
	//   downSecret gates THIS gateway's /rpc/_register (its sub-pods present it)
	//   upSecret   is what THIS gateway presents to prime
	// Leaking the team's downSecret lets you register a feed sub-pod, not
	// join prime. SOV_UPSTREAM_MESH_SECRET defaults to SOV_MESH_SECRET so
	// the single-secret quickstart still works.
	downSecret := env("SOV_MESH_SECRET", "demo-only-mesh-secret")
	upSecret := env("SOV_UPSTREAM_MESH_SECRET", downSecret)
	gw := sov.NewRegistry(sov.RegistryConfig{
		Registry:   registry.Config{AllowedNames: []string{"Chirp", "Feed"}},
		HMACSeal:   hmacseal.Config{Secret: []byte(env("SOV_HMAC_SECRET", "demo-only-secret"))},
		MeshSecret: meshsecret.Config{Secret: []byte(downSecret)},
	}, sov.WithTrustUpstreamClaims(true), sov.WithAdvertiseURL(advertiseURL))
	gw.MustUse(introspect.New())

	log.Fatal(gw.JoinMesh(context.Background(), sov.MeshOptions{
		UpstreamURL:    env("SOV_PRIME", "http://prime:8080"),
		Address:        env("SOV_LISTEN", ":9100"),
		Advertise:      advertiseURL,
		Heartbeat:      5 * time.Second,
		Introspectable: true,
		MeshSecret:     []byte(upSecret),
		FederateAll:    true, // advertise whatever sub-pods are live, refreshed each heartbeat
	}))
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
