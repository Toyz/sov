// Mesh pod: Chirp service.
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
	gw := sov.NewPod(sov.PodConfig{HMACSeal: hmacseal.Config{Secret: []byte(env("SOV_HMAC_SECRET", ""))}}, sov.WithTrustUpstreamClaims(true))
	gw.Register(&chirps.ChirpRouter{Store: chirps.NewMemoryStore()})
	gw.MustUse(introspect.New())

	log.Fatal(gw.JoinMesh(context.Background(), sov.MeshOptions{
		UpstreamURL:    env("SOV_GATEWAY", "http://localhost:8080"),
		Address:        env("SOV_LISTEN", ":9002"),
		Advertise:      env("SOV_ADVERTISE", "http://localhost:9002"),
		Heartbeat:      5 * time.Second,
		Introspectable: true,
		MeshSecret:     []byte(env("SOV_MESH_SECRET", "demo-only-mesh-secret")),
		RegisterToken:  []byte(env("SOV_REGISTER_TOKEN", "")),
	}))
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
