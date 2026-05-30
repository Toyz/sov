package signing

import (
	"github.com/Toyz/sov/gateway"
)

// UseSigning returns a gateway.Option that wires the signing middleware
// onto the gateway. One-call zero-trust setup:
//
//	gw := gateway.New(
//	    gateway.WithRegistry(),
//	    signing.UseSigning(signing.NewMemoryStore()),
//	)
//
// The middleware installs ahead of the consumer middleware chain so
// signed-request verification runs before any handler-level work. The
// auth + authz middleware (which Gateway installs unconditionally
// inside New) still runs first — that order is fine because the auth
// middleware only acts on the bearer header, while signing acts on
// X-Session / X-Ts / X-Sig: orthogonal layers.
//
// SkipMethods names "{Service}/{method}" pairs that bypass signing
// (typically "Session/init" — the very call that registers a new public
// key cannot itself be signed). Append more as your app requires.
func UseSigning(store PublicKeyStore, skipMethods ...string) gateway.Option {
	mw := GatewayMiddleware(MiddlewareOptions{
		Validator:   New(Options{Store: store}),
		SkipMethods: skipMethods,
	})
	return gateway.WithMiddleware(mw)
}
