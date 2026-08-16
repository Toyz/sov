package deadline_test

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/deadline"
)

func run(p *deadline.Plugin, req *gateway.Request) (time.Time, bool) {
	var dl time.Time
	var had bool
	p.Wrap(func(ctx context.Context, _ *gateway.Request) *gateway.Response {
		dl, had = ctx.Deadline()
		return &gateway.Response{Status: 200}
	})(context.Background(), req)
	return dl, had
}

func TestDeadline_AppliesDefault(t *testing.T) {
	dl, had := run(deadline.New(deadline.Config{Default: 5 * time.Second}), &gateway.Request{Header: gateway.Header{}})
	if !had {
		t.Fatal("a default should set a ctx deadline")
	}
	if d := time.Until(dl); d <= 0 || d > 6*time.Second {
		t.Fatalf("deadline ~5s expected, got %v", d)
	}
}

func TestDeadline_HonorsInbound(t *testing.T) {
	want := time.Now().Add(2 * time.Second)
	req := &gateway.Request{Header: gateway.Header{deadline.Header: strconv.FormatInt(want.UnixNano(), 10)}}
	dl, had := run(deadline.New(), req)
	if !had {
		t.Fatal("an inbound X-Sov-Deadline should be honored")
	}
	if dl.UnixNano() != want.UnixNano() {
		t.Fatalf("deadline = %d, want %d", dl.UnixNano(), want.UnixNano())
	}
}

func TestDeadline_NoneWhenNeither(t *testing.T) {
	if _, had := run(deadline.New(), &gateway.Request{Header: gateway.Header{}}); had {
		t.Fatal("no default + no inbound header must leave the ctx without a deadline")
	}
}
