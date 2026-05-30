// Mesh-mode gateway: registry role only.
package main

import (
	"context"
	"log"
	"os"

	"github.com/Toyz/sov"
	"github.com/Toyz/sov/gateway/builtin/hmacseal"
	"github.com/Toyz/sov/gateway/builtin/meshsecret"
	"github.com/Toyz/sov/gateway/builtin/registertoken"
	"github.com/Toyz/sov/gateway/builtin/registry"
)

func main() {
	addr := envDefault("SOV_LISTEN", ":8080")
	gw := sov.NewRegistry(sov.RegistryConfig{
		Registry:   registry.Config{AllowedNames: []string{"Auth", "Authz", "User", "Chirp", "Feed"}},
		HMACSeal:   hmacseal.Config{Secret: []byte(envDefault("SOV_HMAC_SECRET", ""))},
		MeshSecret: meshsecret.Config{Secret: []byte(envDefault("SOV_MESH_SECRET", "demo-only-mesh-secret"))},
		// Optional simple shared-token join gate (kubeadm-style). Off
		// unless SOV_REGISTER_TOKEN is set; composes with the mesh-secret
		// HMAC gate and the AllowedNames gate.
		RegisterToken: registertoken.Config{Token: []byte(envDefault("SOV_REGISTER_TOKEN", ""))},
	})

	log.Printf("chirp mesh gateway on %s — registry mode, mesh-secret gated", addr)
	if err := gw.Run(context.Background(), addr); err != nil {
		log.Fatal(err)
	}
}

func envDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
