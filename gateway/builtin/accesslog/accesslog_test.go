package accesslog_test

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/accesslog"
	"github.com/Toyz/sov/gateway/internal/gwtest"
)

// capHandler captures slog records. gw.Log() falls back to slog.Default() when
// no Logger plugin is registered, so swapping the default handler captures
// exactly what the access log emits on the real path.
type capHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *capHandler) Enabled(context.Context, slog.Level) bool { return true }
func (h *capHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	h.records = append(h.records, r.Clone())
	h.mu.Unlock()
	return nil
}
func (h *capHandler) WithAttrs([]slog.Attr) slog.Handler { return h }
func (h *capHandler) WithGroup(string) slog.Handler      { return h }

func (h *capHandler) rpcRecords() []slog.Record {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []slog.Record
	for _, r := range h.records {
		if r.Message == "rpc" {
			out = append(out, r)
		}
	}
	return out
}

func attrsOf(r slog.Record) map[string]slog.Value {
	m := map[string]slog.Value{}
	r.Attrs(func(a slog.Attr) bool { m[a.Key] = a.Value; return true })
	return m
}

func TestAccessLog_LogsEveryDispatch(t *testing.T) {
	h := &capHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	defer slog.SetDefault(old)

	gw := gwtest.New()
	if err := gw.Use(accesslog.New()); err != nil {
		t.Fatalf("Use accesslog: %v", err)
	}

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodGet, Path: "/rpc/_health", Header: Header{}, RemoteIP: "10.1.2.3",
	})
	if resp.Status != 200 {
		t.Fatalf("health status = %d", resp.Status)
	}
	recs := h.rpcRecords()
	if len(recs) == 0 {
		t.Fatal("accesslog logged nothing on the dispatch path")
	}
	r := recs[len(recs)-1]
	if r.Level != slog.LevelInfo {
		t.Fatalf("2xx should log at info, got %v", r.Level)
	}
	m := attrsOf(r)
	if m["status"].Int64() != 200 {
		t.Fatalf("status attr = %v", m["status"])
	}
	if m["ip"].String() != "10.1.2.3" {
		t.Fatalf("ip attr = %v (RemoteIP must reach the access log)", m["ip"])
	}
	if m["method"].String() != "/rpc/_health" {
		t.Fatalf("method attr = %v", m["method"])
	}
}

func TestAccessLog_ErrorsLogAboveInfo(t *testing.T) {
	h := &capHandler{}
	old := slog.Default()
	slog.SetDefault(slog.New(h))
	defer slog.SetDefault(old)

	gw := gwtest.New()
	_ = gw.Use(accesslog.New())

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Nope/doThing", Header: Header{},
	})
	if resp.Status < 400 {
		t.Fatalf("expected an error status, got %d", resp.Status)
	}
	recs := h.rpcRecords()
	if len(recs) == 0 {
		t.Fatal("no access log entry for the error")
	}
	r := recs[len(recs)-1]
	if r.Level < slog.LevelWarn {
		t.Fatalf("an error (%d) must log at warn or above, got %v", resp.Status, r.Level)
	}
}
