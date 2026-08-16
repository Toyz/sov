package sov

import (
	"context"
	"os"
	"os/signal"
	"syscall"
)

// ShutdownContext returns a context cancelled on the first SIGINT or
// SIGTERM, plus a stop func (call it with defer) that releases the signal
// handler. Pass the context to Gateway.Run, ListenAndServe, or JoinMesh so
// shutdown is graceful: the gateway drains in-flight requests, and a mesh
// pod deregisters its services from the upstream registry, instead of
// dropping mid-request and leaving stale routes to time out.
//
// Run / ListenAndServe / JoinMesh return context.Canceled once the signal
// fires — the expected clean-stop path — so filter it:
//
//	func main() {
//	    ctx, stop := sov.ShutdownContext()
//	    defer stop()
//	    if err := gw.Run(ctx, ":8080"); err != nil && !errors.Is(err, context.Canceled) {
//	        log.Fatal(err)
//	    }
//	}
func ShutdownContext() (context.Context, context.CancelFunc) {
	return signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
}
