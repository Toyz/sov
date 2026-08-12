package static_test

import (
	"github.com/Toyz/sov/gateway"
	rpcsurface "github.com/Toyz/sov/gateway/builtin/rpc"
)

// newGW is gateway.New plus the rpc surface builtin — /rpc is a builtin now, so
// a test dispatching /rpc registers it (as production does via the presets).
func newGW(opts ...gateway.Option) *gateway.Gateway {
	gw := gateway.New(opts...)
	gw.MustUse(rpcsurface.New())
	return gw
}
