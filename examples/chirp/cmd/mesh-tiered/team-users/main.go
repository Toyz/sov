// Tiered mesh — team-users gateway. Federates its LIVE service set to
// prime via FederateAll — no hand-maintained list.
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/Toyz/sov"
	"github.com/Toyz/sov/gateway/builtin/hmacseal"
	"github.com/Toyz/sov/gateway/builtin/meshsecret"
	"github.com/Toyz/sov/gateway/builtin/registry"
)

func main() {
	advertiseURL := env("SOV_ADVERTISE", "http://team-users:9200")
	// Separate down/up join secrets per tier — see team-feed for the
	// blast-radius rationale. SOV_UPSTREAM_MESH_SECRET defaults to
	// SOV_MESH_SECRET so the single-secret quickstart still works.
	downSecret := env("SOV_MESH_SECRET", "demo-only-mesh-secret")
	upSecret := env("SOV_UPSTREAM_MESH_SECRET", downSecret)
	gw := sov.NewRegistry(sov.RegistryConfig{
		Registry:   registry.Config{AllowedNames: []string{"User"}},
		HMACSeal:   hmacseal.Config{Secret: []byte(env("SOV_HMAC_SECRET", "demo-only-secret"))},
		MeshSecret: meshsecret.Config{Secret: []byte(downSecret)},
	}, sov.WithTrustUpstreamClaims(true), sov.WithAdvertiseURL(advertiseURL))

	log.Fatal(gw.JoinMesh(context.Background(), sov.MeshOptions{
		UpstreamURL:    env("SOV_PRIME", "http://prime:8080"),
		Address:        env("SOV_LISTEN", ":9200"),
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
