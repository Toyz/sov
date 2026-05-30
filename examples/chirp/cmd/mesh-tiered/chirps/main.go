// Tiered mesh — chirps pod. Registers against TEAM-FEED, not prime.
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/Toyz/sov"
	"github.com/Toyz/sov/examples/chirp/handlers/chirps"
	"github.com/Toyz/sov/gateway/builtin/hmacseal"
	"github.com/Toyz/sov/gateway/builtin/introspect"
)

func main() {
	gw := sov.NewPod(sov.PodConfig{HMACSeal: hmacseal.Config{Secret: []byte(env("SOV_HMAC_SECRET", "demo-only-secret"))}}, sov.WithTrustUpstreamClaims(true))
	gw.Register(&chirps.ChirpRouter{Store: chirps.NewMemoryStore()})
	gw.MustUse(introspect.New())

	log.Fatal(gw.JoinMesh(context.Background(), sov.MeshOptions{
		UpstreamURL:    env("SOV_TEAM", "http://team-feed:9100"),
		Address:        env("SOV_LISTEN", ":9101"),
		Advertise:      env("SOV_ADVERTISE", "http://chirps:9101"),
		Heartbeat:      5 * time.Second,
		Introspectable: true,
		MeshSecret:     []byte(env("SOV_MESH_SECRET", "demo-only-mesh-secret")),
	}))
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
