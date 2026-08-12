// Package gwtest holds gateway test helpers shared across the sov test suite —
// the counterpart of rpctest for handler tests.
package gwtest

import (
	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/rpc"
)

// New builds a gateway with the rpc surface builtin registered. /rpc is a builtin
// now (gateway/builtin/rpc), so a test that dispatches /rpc registers it — this
// is the common setup, mirroring what the presets do in production. It replaces
// the per-package newGW helpers the rpc-as-a-builtin migration would otherwise
// have duplicated.
func New(opts ...gateway.Option) *gateway.Gateway {
	gw := gateway.New(opts...)
	gw.MustUse(rpc.New())
	return gw
}
