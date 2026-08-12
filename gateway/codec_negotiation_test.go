package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/batch"
	"github.com/Toyz/sov/rpc"
)

// testCodec is a non-JSON business codec whose encodes are recognizable.
// Who/me takes no params, so DecodeParams is never exercised here.
type testCodec struct{}

func (testCodec) Name() string                                            { return "test" }
func (testCodec) DecodeParams(body []byte, p any, fm *rpc.FieldMap) error { return nil }
func (testCodec) EncodeResult(d any) ([]byte, error) {
	b, _ := json.Marshal(d)
	return append([]byte("TEST:"), b...), nil
}
func (testCodec) EncodeError(e *rpc.Error) ([]byte, error) { return []byte("TEST-ERR:" + e.Code), nil }

// bnEchoRouter is a param router so a batch test can prove entries decode as
// JSON (not the outer binary codec's no-op decode).
type bnEchoRouter struct{}

type bnEchoParams struct {
	Msg string `json:"msg"`
}

func (bnEchoRouter) Say(ctx *rpc.Context, p *bnEchoParams) (string, error) { return p.Msg, nil }

// The negotiated codec is reflected on the response Content-Type so the
// caller decodes the body with the codec it sent.
func TestCodec_ResponseContentTypeReflectsCodec(t *testing.T) {
	gw := New()
	gw.RegisterAuth(&AuthRouter{})
	gw.Register(&WhoRouter{})
	gw.Engine().RegisterCodec(testCodec{})

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Who/me",
		Header: Header{"Authorization": "Bearer good-x", "Content-Type": "application/x-test"},
	})
	if got := resp.Header.Get("Content-Type"); got != "application/x-test" {
		t.Fatalf("response Content-Type = %q, want application/x-test", got)
	}
}

// A batch under a binary OUTER codec must still dispatch entries as JSON —
// batch is a JSON multiplexer. If entries inherited the outer codec, the
// no-op test decode would drop the param and Say would echo "" instead of
// the sent value.
func TestCodec_BatchEntriesPinnedToJSON(t *testing.T) {
	gw := New()
	gw.RegisterAuth(&AuthRouter{})
	gw.Register(&bnEchoRouter{})
	gw.Engine().RegisterCodec(testCodec{})
	if err := gw.Use(batch.New(batch.Config{})); err != nil {
		t.Fatalf("Use batch: %v", err)
	}

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/_batch",
		Header: Header{"Authorization": "Bearer good-x", "Content-Type": "application/x-test"},
		Body:   []byte(`{"calls":{"a":{"service":"bnEcho","method":"say","args":{"msg":"hi"}}}}`),
	})
	if resp.Status != 200 {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
	// Batch response framing is JSON; the entry decoded its param as JSON.
	if !strings.Contains(string(resp.Body), `"hi"`) {
		t.Fatalf("batch entry not JSON-decoded (param dropped?): %s", resp.Body)
	}
}

// A registered codec is selected PER REQUEST by Content-Type; absent it, the
// JSON default is used.
func TestCodec_NegotiatedPerRequest(t *testing.T) {
	gw := New()
	gw.RegisterAuth(&AuthRouter{})
	gw.Register(&WhoRouter{})
	gw.Engine().RegisterCodec(testCodec{})

	// No codec Content-Type → JSON default.
	j := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Who/me",
		Header: Header{"Authorization": "Bearer good-x"},
	})
	if j.Status != 200 || !strings.Contains(string(j.Body), `{"data":"u_x"}`) {
		t.Fatalf("json default: status=%d body=%s", j.Status, j.Body)
	}

	// application/x-test → the registered test codec owns the result body.
	tc := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Who/me",
		Header: Header{"Authorization": "Bearer good-x", "Content-Type": "application/x-test"},
	})
	if tc.Status != 200 || !strings.HasPrefix(string(tc.Body), "TEST:") {
		t.Fatalf("negotiated codec not used: status=%d body=%s", tc.Status, tc.Body)
	}
}

// Even when a business call negotiates a non-JSON codec, the gateway's
// internal authz Check sub-dispatch (which carries no codec Content-Type)
// still runs as JSON — so authz keeps working. And a gateway-level authz
// denial is JSON-framed (the framework-stays-JSON boundary), NOT the
// business codec: authz short-circuits in authzMiddleware, before the
// engine's per-request codec ever applies.
func TestCodec_InternalAuthzStaysJSON(t *testing.T) {
	gw := New()
	gw.RegisterAuth(&AuthRouter{})
	gw.RegisterAuthz(&AuthzRouter{denyMethod: "me"})
	gw.Register(&WhoRouter{})
	gw.Engine().RegisterCodec(testCodec{})

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Who/me",
		Header: Header{"Authorization": "Bearer good-x", "Content-Type": "application/x-test"},
	})
	if resp.Status != 403 {
		t.Fatalf("status = %d, want 403 (internal authz must run under a non-JSON business codec)", resp.Status)
	}
	// Gateway authz error is JSON framing — proves authz ran (decoding
	// CheckParams as JSON internally) and that framework errors stay JSON.
	if !strings.Contains(string(resp.Body), `"code":"FORBIDDEN"`) {
		t.Fatalf("authz deny not JSON-framed: %s", resp.Body)
	}
}
