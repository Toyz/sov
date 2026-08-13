package batch_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/batch"
	"github.com/Toyz/sov/gateway/builtin/rpc"
)

// batch fans out through /rpc, so it declares Requires("rpc"). Without the rpc
// builtin, boot must FAIL FAST — not serve and 404 every entry at request time.
// (Requires only takes effect when the plugin ALSO implements After(), i.e.
// satisfies gateway.PluginDependency — a regression guard for that binding.)
func TestBatch_RequiresRPC_BootFails(t *testing.T) {
	gw := gateway.New()
	gw.MustUse(batch.New(batch.Config{}))

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := gw.ListenAndServe(ctx, "127.0.0.1:0")
	if err == nil || !strings.Contains(err.Error(), "requires") || !strings.Contains(err.Error(), "rpc") {
		t.Fatalf("batch without rpc should fail boot with a requires-rpc error, got: %v", err)
	}
}

// With rpc registered the dependency is satisfied — boot proceeds (here it
// stops on the cancelled context, NOT on a requires error).
func TestBatch_WithRPC_BootOK(t *testing.T) {
	gw := gateway.New()
	gw.MustUse(rpc.New())
	gw.MustUse(batch.New(batch.Config{}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // stop immediately after boot validation passes
	if err := gw.ListenAndServe(ctx, "127.0.0.1:0"); err != nil && strings.Contains(err.Error(), "requires") {
		t.Fatalf("batch with rpc should NOT hit a requires error, got: %v", err)
	}
}
