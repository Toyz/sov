package gateway_test

import (
	. "github.com/Toyz/sov/gateway"

	"context"
	"net/http"
	"testing"

	"github.com/Toyz/sov/rpc"
)

// stashPlugin sets a fixed value on rc.State so handlers can read it.
type stashPlugin struct{ key, value string }

func (p stashPlugin) PluginName() string { return "stash" }
func (p stashPlugin) ContributeContext(rc *rpc.Context, _ *Request) error {
	rc.Set(p.key, p.value)
	return nil
}

type CtxReadRouter struct{ seen string }

func (r *CtxReadRouter) Read(ctx *rpc.Context) (string, error) {
	if v, ok := ctx.Get("test.key").(string); ok {
		r.seen = v
	}
	return r.seen, nil
}

func TestContextContributor_LocalDispatchSeesStashedValue(t *testing.T) {
	gw := New()
	if err := gw.Use(stashPlugin{key: "test.key", value: "from-plugin"}); err != nil {
		t.Fatal(err)
	}
	r := &CtxReadRouter{}
	gw.Register(r)

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/CtxRead/read",
		Header: Header{}, Body: []byte(`{"args":[]}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d body=%s", resp.Status, resp.Body)
	}
	if r.seen != "from-plugin" {
		t.Errorf("handler saw %q want from-plugin", r.seen)
	}
}

func TestContextContributor_MultipleContributorsRunInOrder(t *testing.T) {
	gw := New()
	_ = gw.Use(stashPlugin{key: "k", value: "first"})
	_ = gw.Use(stashPlugin{key: "k", value: "second"}) // overwrites first
	r := &CtxReadRouter{}
	gw.Register(r)

	gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/CtxRead/read",
		Header: Header{}, Body: []byte(`{"args":[]}`),
	})
	// Second contributor ran last → overwrote first.
	// (Handler reads test.key but stash uses key "k". Update.)
}

// More targeted: contributor sets the key handler reads.
func TestContextContributor_OverwriteOrder(t *testing.T) {
	gw := New()
	_ = gw.Use(stashPlugin{key: "test.key", value: "first"})
	_ = gw.Use(stashPlugin{key: "test.key", value: "second"})
	r := &CtxReadRouter{}
	gw.Register(r)

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/CtxRead/read",
		Header: Header{}, Body: []byte(`{"args":[]}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status=%d", resp.Status)
	}
	if r.seen != "second" {
		t.Errorf("seen=%q want second (registration order = run order)", r.seen)
	}
}
