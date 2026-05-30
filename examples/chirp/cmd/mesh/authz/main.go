// Mesh pod: Authz service. Self-declares Roles=RoleAuthz so the central
// gateway routes every Check decision here.
package main

import (
	"context"
	"log"
	"os"
	"time"

	"github.com/Toyz/sov"
	"github.com/Toyz/sov/examples/chirp/handlers/authz"
	"github.com/Toyz/sov/gateway/builtin/hmacseal"
	"github.com/Toyz/sov/gateway/builtin/introspect"
)

func main() {
	gw := sov.NewPod(sov.PodConfig{HMACSeal: hmacseal.Config{Secret: []byte(env("SOV_HMAC_SECRET", ""))}}, sov.WithTrustUpstreamClaims(true))
	gw.Register(authz.NewAuthzRouter())
	gw.MustUse(introspect.New())

	log.Fatal(gw.JoinMesh(context.Background(), sov.MeshOptions{
		UpstreamURL:    env("SOV_GATEWAY", "http://localhost:8080"),
		Address:        env("SOV_LISTEN", ":9005"),
		Advertise:      env("SOV_ADVERTISE", "http://localhost:9005"),
		Heartbeat:      5 * time.Second,
		Roles:          sov.RoleAuthz,
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
