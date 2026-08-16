package batch_test

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/batch"
	"github.com/Toyz/sov/gateway/builtin/rpc"
	sovrpc "github.com/Toyz/sov/rpc"
)

// echoRouter is a trivial local router batch entries can target.
type echoRouter struct{}

type echoParams struct {
	Msg string `json:"msg"`
}

func (echoRouter) Say(ctx *sovrpc.Context, p *echoParams) (string, error) { return p.Msg, nil }

// slowRouter records the peak number of its Go handlers running at once, so a
// test can prove the batch concurrency cap actually bounds in-flight entries.
type slowRouter struct {
	active  int32
	maxSeen int32
}

func (r *slowRouter) Go(ctx *sovrpc.Context) (string, error) {
	n := atomic.AddInt32(&r.active, 1)
	for {
		m := atomic.LoadInt32(&r.maxSeen)
		if n <= m || atomic.CompareAndSwapInt32(&r.maxSeen, m, n) {
			break
		}
	}
	time.Sleep(10 * time.Millisecond) // widen the concurrency window
	atomic.AddInt32(&r.active, -1)
	return "ok", nil
}

// A batch with more entries than MaxBatchSize is rejected 413 BEFORE any
// fan-out — an unbounded batch is a resource-exhaustion vector (HELL-296).
func TestBatch_MaxBatchSize_Rejects(t *testing.T) {
	gw := gateway.New()
	gw.MustUse(rpc.New())
	gw.Register(&echoRouter{})
	gw.MustUse(batch.New(batch.Config{MaxBatchSize: 3}))

	body := `{"calls":{` +
		`"a":{"service":"echo","method":"say","args":{"msg":"1"}},` +
		`"b":{"service":"echo","method":"say","args":{"msg":"2"}},` +
		`"c":{"service":"echo","method":"say","args":{"msg":"3"}},` +
		`"d":{"service":"echo","method":"say","args":{"msg":"4"}}}}`
	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/_batch",
		Header: gateway.Header{}, Body: []byte(body),
	})
	if resp.Status != http.StatusRequestEntityTooLarge {
		t.Fatalf("batch of 4 with MaxBatchSize=3: status = %d, want 413; body=%s", resp.Status, resp.Body)
	}
}

// A within-limit batch dispatches at most MaxConcurrency entries at once —
// bounding goroutine and downstream load regardless of batch size.
func TestBatch_MaxConcurrency_Bounds(t *testing.T) {
	gw := gateway.New()
	gw.MustUse(rpc.New())
	slow := &slowRouter{}
	gw.Register(slow)
	gw.MustUse(batch.New(batch.Config{MaxConcurrency: 2}))

	calls := ""
	for i := 0; i < 8; i++ {
		if i > 0 {
			calls += ","
		}
		calls += fmt.Sprintf(`"c%d":{"service":"slow","method":"go"}`, i)
	}
	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost, Path: "/rpc/_batch",
		Header: gateway.Header{}, Body: []byte(`{"calls":{` + calls + `}}`),
	})
	if resp.Status != 200 {
		t.Fatalf("batch status = %d, body = %s", resp.Status, resp.Body)
	}
	if peak := atomic.LoadInt32(&slow.maxSeen); peak > 2 {
		t.Fatalf("peak concurrent entries = %d, want <= 2 (MaxConcurrency not enforced)", peak)
	}
}

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
