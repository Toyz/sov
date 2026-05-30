// Package hmacseal seals injected X-Sov-* claim headers with HMAC-SHA256
// so downstream services can detect forged claim bundles.
//
// Two hats:
//
//   - HeaderInjector — writes X-Sov-Seal across the X-Sov-* bundle on
//     every outbound proxy hop. Register LAST so it covers every
//     header any earlier plugin wrote.
//
//   - SealVerifier — verifies inbound X-Sov-Seal against the same
//     key on the pod-side trust guard.
//
//     gw.Use(hmacseal.New(hmacseal.Config{Secret: secret}))
package hmacseal

import (
	"context"
	"net/http"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/hmacseal/proto"
)

// Config configures the hmacseal plugin. Secret is the HMAC key
// (32+ bytes recommended). Empty secret turns the plugin into a
// no-op — useful for dev environments without mesh hardening.
type Config struct {
	Secret []byte
}

// Plugin is the seal-injector + verifier returned by New.
type Plugin struct{ secret []byte }

// New returns an hmac-seal plugin from cfg.
func New(cfg Config) *Plugin { return &Plugin{secret: cfg.Secret} }

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin             = (*Plugin)(nil)
	_ gateway.PluginDoc          = (*Plugin)(nil)
	_ gateway.CapabilityProvider = (*Plugin)(nil)
	_ gateway.HeaderInjector     = (*Plugin)(nil)
	_ gateway.SealVerifier       = (*Plugin)(nil)
)

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "hmac-seal" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "HMAC-seals the X-Sov-* identity bundle on outbound hops and verifies it inbound."
}

// SealKeyFn returns a COPY of the seal secret. Capability so future
// crypto plugins can sign with the same key. Copy semantics prevent
// mutation leaks.
type SealKeyFn func() []byte

// Capabilities publishes a read-only view of the seal key.
func (p *Plugin) Capabilities() []gateway.Capability {
	return []gateway.Capability{
		{Type: "hmacseal.SealKey", Impl: SealKeyFn(func() []byte {
			cp := make([]byte, len(p.secret))
			copy(cp, p.secret)
			return cp
		})},
	}
}

// InjectHeaders writes proto.HeaderSeal across the existing X-Sov-*
// bundle. No-op when there's no Subject to seal or no secret.
func (p *Plugin) InjectHeaders(_ context.Context, _ *gateway.Request, hreq *http.Request) error {
	if len(p.secret) == 0 {
		return nil
	}
	if hreq.Header.Get(gateway.HeaderSubject) == "" {
		return nil
	}
	hreq.Header.Set(proto.HeaderSeal, proto.Sign(hreq.Header, p.secret))
	return nil
}

// VerifySeal returns true iff the inbound X-Sov-Seal header verifies
// under the plugin's secret. Framework trust guard iterates registered
// SealVerifiers; first true wins.
func (p *Plugin) VerifySeal(headers map[string][]string) bool {
	if len(p.secret) == 0 {
		return false
	}
	return proto.Verify(http.Header(headers), p.secret)
}
