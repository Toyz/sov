// Tiered mesh — users pod. Registers against TEAM-USERS.
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/Toyz/sov"
	"github.com/Toyz/sov/examples/chirp/handlers/users"
	"github.com/Toyz/sov/gateway/builtin/hmacseal"
	"github.com/Toyz/sov/gateway/builtin/introspect"
)

func main() {
	gw := sov.NewPod(sov.PodConfig{HMACSeal: hmacseal.Config{Secret: []byte(env("SOV_HMAC_SECRET", "demo-only-secret"))}}, sov.WithTrustUpstreamClaims(true))
	gw.Register(&users.UserRouter{Store: users.NewMemoryStore()})
	gw.MustUse(introspect.New())

	log.Fatal(gw.JoinMesh(context.Background(), sov.MeshOptions{
		UpstreamURL:    env("SOV_TEAM", "http://team-users:9200"),
		Address:        env("SOV_LISTEN", ":9201"),
		Advertise:      env("SOV_ADVERTISE", "http://users:9201"),
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
