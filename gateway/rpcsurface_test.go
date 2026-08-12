package gateway_test

import (
	. "github.com/Toyz/sov/gateway"
	rpcsurface "github.com/Toyz/sov/gateway/builtin/rpc"
)

// newGW is New plus the rpc surface builtin. The /rpc/{router}/{method} wire is
// a builtin now (gateway/builtin/rpc), not hardcoded core routing — so a test
// that dispatches /rpc registers it, exactly as production does via the presets.
// The gateway_test suite was mechanically switched from New(...) to newGW(...).
func newGW(opts ...Option) *Gateway {
	gw := New(opts...)
	gw.MustUse(rpcsurface.New())
	return gw
}
