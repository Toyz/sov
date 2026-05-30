package rpc

import (
	"context"
	"testing"
)

// BenchRouter is a trivial router for measuring the in-process dispatch
// hot path (reflection lookup + JSON decode + field bind + reflect call).
type BenchRouter struct{}

type benchParams struct {
	Name string `json:"name"`
}

func (BenchRouter) Greet(_ *Context, p *benchParams) (map[string]string, error) {
	return map[string]string{"hello": p.Name}, nil
}

// BenchmarkEngineDispatchLocal measures one in-process RPC: the cost of
// the engine's reflection dispatch with no HTTP, no gateway middleware.
// This is the "in-process call ≈ a function call" number — the floor PEMM
// pays to keep the SAME handler addressable locally or remotely.
func BenchmarkEngineDispatchLocal(b *testing.B) {
	e := NewEngine()
	e.Register(&BenchRouter{})
	body := []byte(`{"args":{"name":"sov"}}`)

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := NewContext(context.Background())
		status, _ := e.Dispatch(ctx, "Bench", "greet", body)
		if status != 200 {
			b.Fatalf("status %d", status)
		}
	}
}
