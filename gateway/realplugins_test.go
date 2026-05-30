package gateway_test

import (
	"github.com/Toyz/sov/gateway/builtin/batch"
	"github.com/Toyz/sov/gateway/builtin/registry"

	. "github.com/Toyz/sov/gateway"
)

// newBatchGateway returns a gateway pre-wired with the REAL builtin
// batch plugin. Replaces the deleted clone helper.
func newBatchGateway(opts ...Option) *Gateway {
	gw := New(opts...)
	_ = gw.Use(batch.New(batch.Config{}))
	return gw
}

// newRegistryGateway returns a gateway pre-wired with the REAL builtin
// registry AND batch plugins, mirroring the deleted clone helper which
// wired both.
func newRegistryGateway(opts ...Option) *Gateway {
	gw := New(opts...)
	_ = gw.Use(registry.New(registry.Config{}))
	_ = gw.Use(batch.New(batch.Config{}))
	return gw
}
