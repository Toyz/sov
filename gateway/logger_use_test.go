package gateway_test

import (
	"log/slog"
	"testing"

	"github.com/Toyz/sov/gateway/internal/gwtest"
)

// The README documents `gw.Use(slog.Default())` as the way to install a logger,
// and *slog.Logger satisfies gateway.Logger. A plugin whose ONLY binding is
// Logger must therefore be accepted by Use — it was rejected before Logger was
// counted as a satisfied hook.
func TestUse_PlainLoggerAccepted(t *testing.T) {
	gw := gwtest.New()
	if err := gw.Use(slog.Default()); err != nil {
		t.Fatalf("gw.Use(slog.Default()) must be accepted: %v", err)
	}
}
