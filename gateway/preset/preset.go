// Package preset curates default plugin bundles for the common sov
// deployment modes. Each function returns a []any consumable by
// gw.UseAll — operators get sensible defaults for monolith / pod /
// registry / hybrid shapes via a single config struct.
//
//	var cfg preset.PodConfig
//	viper.Unmarshal(&cfg)
//	gw.UseAll(preset.Pod(cfg)...)
//
// Presets are batteries-included STARTING POINTS, not policy
// statements. Operators append/replace plugins as needed:
//
//	plugins := preset.Pod(preset.PodConfig{HMACSecret: secret})
//	plugins = append(plugins, audit.New(audit.Config{}))
//	gw.UseAll(plugins...)
//
// Drift detection is an operator-side CLI concern — run
// `sov drift -from <gateway-url>` in CI rather than carrying a
// server-side detector plugin.
package preset

import (
	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/batch"
	"github.com/Toyz/sov/gateway/builtin/cors"
	"github.com/Toyz/sov/gateway/builtin/hmacseal"
	"github.com/Toyz/sov/gateway/builtin/meshsecret"
	"github.com/Toyz/sov/gateway/builtin/registertoken"
	"github.com/Toyz/sov/gateway/builtin/registry"
	"github.com/Toyz/sov/gateway/builtin/requestid"
)

// MonolithConfig composes the plugin configs for a single-binary
// gateway hosting all services in-process. Also drives the Hybrid
// preset (HybridConfig is an alias), where a live /rpc/_register lets
// remote pods self-register alongside the in-process services — so the
// join gates below matter: a hybrid gateway exposes _register exactly
// like a registry does.
//
// The base bundle is intentionally minimal + safe-by-default. The
// observability / info-disclosure plugins (audit, explorer, manifest)
// are NOT wired here — they are opt-in via gw.Use(...) after the gateway
// is constructed.
type MonolithConfig struct {
	RequestID requestid.Config
	Registry  registry.Config // set Registry.AllowedNames for a name allowlist gate
	Batch     batch.Config
	Cors      cors.Config
	// Join/seal gates — all optional, empty value skips the plugin.
	// Pure-monolith deploys (no remote pods) leave these empty. A hybrid
	// gateway reachable on an untrusted network should set a join gate
	// (MeshSecret or RegisterToken) so _register isn't open, and HMACSeal
	// if it also needs cryptographic proof on inbound identity claims.
	HMACSeal      hmacseal.Config      // optional — empty Secret skips (X-Sov-Seal claim proof)
	MeshSecret    meshsecret.Config    // optional — empty Secret skips (HMAC _register join gate)
	RegisterToken registertoken.Config // optional — empty Token skips (shared-token _register join gate)
}

// Monolith returns the plugin set for the cmd. Pass MonolithConfig{} for
// sane defaults. The bundle is minimal + safe-by-default: requestid,
// registry, batch, cors, plus any configured join/seal gates.
//
// audit, explorer, and manifest are OPT-IN and NOT included here — they
// disclose information (audit logs every dispatch incl. SUBJECT identity;
// explorer exposes the full API catalog + try-it UI; manifest exposes the
// plugin list), so enable them explicitly:
//
//	gw.Use(explorer.New(explorer.Config{}))
//	gw.Use(manifest.New(manifest.Config{}))
//	gw.Use(audit.New(audit.Config{Out: os.Stdout}))
//
// The HMACSeal/MeshSecret/RegisterToken gates are wired only when their
// secret/token is set — important for the Hybrid preset, whose _register
// endpoint is OPEN unless one of the join gates (MeshSecret/RegisterToken)
// or Registry.AllowedNames is set.
func Monolith(cfg MonolithConfig) []any {
	out := []any{
		requestid.New(cfg.RequestID),
		registry.New(cfg.Registry),
		batch.New(cfg.Batch),
		cors.New(cfg.Cors),
	}
	if len(cfg.HMACSeal.Secret) > 0 {
		out = append(out, hmacseal.New(cfg.HMACSeal))
	}
	if len(cfg.MeshSecret.Secret) > 0 {
		out = append(out, meshsecret.New(cfg.MeshSecret))
	}
	if len(cfg.RegisterToken.Token) > 0 {
		out = append(out, registertoken.New(cfg.RegisterToken))
	}
	return out
}

// PodConfig composes plugin configs for a mesh-pod (a binary that
// hosts one service + JoinMesh's an upstream registry). For
// AdvertiseURL pass sov.WithAdvertiseURL(...) at gateway construction.
type PodConfig struct {
	RequestID requestid.Config
	HMACSeal  hmacseal.Config
}

// Pod returns the plugin set for a mesh-pod deployment. Empty
// HMACSeal.Secret leaves that plugin off.
func Pod(cfg PodConfig) []any {
	out := []any{requestid.New(cfg.RequestID)}
	if len(cfg.HMACSeal.Secret) > 0 {
		out = append(out, hmacseal.New(cfg.HMACSeal))
	}
	return out
}

// RegistryConfig composes plugin configs for a central registry /
// master gateway fronting a mesh of pods. AllowedNames on the
// registry plugin replaces the standalone allowlist plugin. For
// AdvertiseURL pass sov.WithAdvertiseURL(...) at gateway construction.
//
// Like MonolithConfig, the base bundle is minimal + safe-by-default;
// audit, explorer, and manifest are NOT wired here — opt in via
// gw.Use(...) after construction.
type RegistryConfig struct {
	RequestID     requestid.Config
	Registry      registry.Config // set Registry.AllowedNames to gate _register
	Batch         batch.Config
	Cors          cors.Config
	HMACSeal      hmacseal.Config      // optional — empty Secret skips
	MeshSecret    meshsecret.Config    // optional — empty Secret skips (HMAC join gate)
	RegisterToken registertoken.Config // optional — empty Token skips (simple shared-token join gate)
}

// Registry returns the plugin set for a registry/master gateway.
// Empty-valued config entries skip their plugin so a minimal call
// like preset.Registry(preset.RegistryConfig{}) still works.
//
// audit, explorer, and manifest are OPT-IN and NOT included — they
// disclose information, so enable them explicitly via gw.Use(...).
func Registry(cfg RegistryConfig) []any {
	out := []any{
		requestid.New(cfg.RequestID),
		registry.New(cfg.Registry),
		batch.New(cfg.Batch),
		cors.New(cfg.Cors),
	}
	if len(cfg.HMACSeal.Secret) > 0 {
		out = append(out, hmacseal.New(cfg.HMACSeal))
	}
	if len(cfg.MeshSecret.Secret) > 0 {
		out = append(out, meshsecret.New(cfg.MeshSecret))
	}
	if len(cfg.RegisterToken.Token) > 0 {
		out = append(out, registertoken.New(cfg.RegisterToken))
	}
	return out
}

// HybridConfig aliases MonolithConfig — hybrid deployment is wired
// identically to monolith at the plugin level; the difference is at
// the cmd (some services registered in-process, others self-registering
// remotely via /rpc/_register).
//
// SECURITY: a hybrid gateway exposes a live _register endpoint. With no
// gate set it is OPEN — any reachable actor can self-register a
// non-reserved service name and receive routed traffic (the only
// built-in protection is the auth/authz role-conflict guard). Set a join
// gate before exposing it on an untrusted network:
//
//	gw := sov.NewHybrid(sov.HybridConfig{
//	    RegisterToken: registertoken.Config{Token: os.Getenv("SOV_JOIN_TOKEN")},
//	    // or MeshSecret: meshsecret.Config{Secret: ...} for HMAC,
//	    // or Registry: registry.Config{AllowedNames: []string{...}} for a name allowlist,
//	    // and HMACSeal: hmacseal.Config{Secret: ...} to also require proof on inbound claims.
//	})
type HybridConfig = MonolithConfig

// Hybrid returns the plugin set for a hybrid gateway.
func Hybrid(cfg HybridConfig) []any { return Monolith(cfg) }

// NewMonolith returns a gateway pre-loaded with the Monolith preset.
// Equivalent to:
//
//	gw := gateway.New(opts...)
//	gw.MustUseAll(preset.Monolith(cfg)...)
//
// One call instead of two for the 80% case.
func NewMonolith(cfg MonolithConfig, opts ...gateway.Option) *gateway.Gateway {
	gw := gateway.New(opts...)
	gw.MustUseAll(Monolith(cfg)...)
	return gw
}

// NewPod returns a gateway pre-loaded with the Pod preset.
func NewPod(cfg PodConfig, opts ...gateway.Option) *gateway.Gateway {
	gw := gateway.New(opts...)
	gw.MustUseAll(Pod(cfg)...)
	return gw
}

// NewRegistry returns a gateway pre-loaded with the Registry preset.
func NewRegistry(cfg RegistryConfig, opts ...gateway.Option) *gateway.Gateway {
	gw := gateway.New(opts...)
	gw.MustUseAll(Registry(cfg)...)
	return gw
}

// NewHybrid returns a gateway pre-loaded with the Hybrid preset.
func NewHybrid(cfg HybridConfig, opts ...gateway.Option) *gateway.Gateway {
	gw := gateway.New(opts...)
	gw.MustUseAll(Hybrid(cfg)...)
	return gw
}
