// Tiered mesh — feed pod. Registers against TEAM-FEED.
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
	teamURL := env("SOV_TEAM", "http://team-feed:9100")
	// Cross-service calls (User, Chirp) route back through prime — the
	// team gateway only knows its own services.
	cli := sov.NewClient(env("SOV_PRIME", "http://prime:8080"),
		sov.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}))

	gw := sov.NewPod(sov.PodConfig{HMACSeal: hmacseal.Config{Secret: []byte(env("SOV_HMAC_SECRET", "demo-only-secret"))}}, sov.WithTrustUpstreamClaims(true))
	gw.Register(&feed.FeedRouter{Client: feed.NewClientAdapter(cli)})

	log.Fatal(gw.JoinMesh(context.Background(), sov.MeshOptions{
		UpstreamURL:    teamURL,
		Address:        env("SOV_LISTEN", ":9102"),
		Advertise:      env("SOV_ADVERTISE", "http://feed:9102"),
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
