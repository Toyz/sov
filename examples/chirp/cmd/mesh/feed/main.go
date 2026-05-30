// Mesh pod: Feed service. Cross-service calls (User.listFollowing,
// Chirp.listByAuthors) flow back through the central gateway — same
// handler code as monolith mode; only the wired Client differs.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/Toyz/sov"
	"github.com/Toyz/sov/examples/chirp/handlers/feed"
	"github.com/Toyz/sov/gateway/builtin/hmacseal"
)

func main() {
	gwURL := env("SOV_GATEWAY", "http://localhost:8080")
	cli := sov.NewClient(gwURL, sov.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))

	gw := sov.NewPod(sov.PodConfig{HMACSeal: hmacseal.Config{Secret: []byte(env("SOV_HMAC_SECRET", ""))}}, sov.WithTrustUpstreamClaims(true))
	gw.Register(&feed.FeedRouter{Client: feed.NewClientAdapter(cli)})

	log.Fatal(gw.JoinMesh(context.Background(), sov.MeshOptions{
		UpstreamURL:    gwURL,
		Address:        env("SOV_LISTEN", ":9004"),
		Advertise:      env("SOV_ADVERTISE", "http://localhost:9004"),
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
