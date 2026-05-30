package gateway_test

import (
	. "github.com/Toyz/sov/gateway"

	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	hmacsealproto "github.com/Toyz/sov/gateway/builtin/hmacseal/proto"
	"github.com/Toyz/sov/rpc"
)

// ---- An auth router that maps token "good-N" → Claims{Subject:"u_N"}.

type AuthRouter struct {
	expiry time.Time
}

func (r *AuthRouter) Verify(ctx *rpc.Context, p *VerifyParams) (*Claims, error) {
	if !strings.HasPrefix(p.Token, "good-") {
		return nil, rpc.Unauthorized("bad token")
	}
	uid := "u_" + strings.TrimPrefix(p.Token, "good-")
	exp := r.expiry
	if exp.IsZero() {
		exp = time.Now().Add(1 * time.Hour).UTC()
	}
	return &Claims{Subject: uid, Issuer: "test", Scopes: []string{"read"}, ExpiresAt: exp}, nil
}

// ---- A protected router that reads Subject from ctx.

type WhoRouter struct{}

func (r *WhoRouter) Me(ctx *rpc.Context) (string, error) {
	return rpc.RequireSubject(ctx)
}

// ---- Tests ----------------------------------------------------------------

func TestAuth_BearerToClaimsLocal(t *testing.T) {
	gw := New()
	gw.RegisterAuth(&AuthRouter{})
	gw.Register(&WhoRouter{})

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost,
		Path:   "/rpc/Who/me",
		Header: Header{"Authorization": "Bearer good-alice"},
	})
	if resp.Status != 200 {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), `"u_alice"`) {
		t.Fatalf("body = %s", resp.Body)
	}
}

func TestAuth_BadBearerRejected(t *testing.T) {
	gw := New()
	gw.RegisterAuth(&AuthRouter{})
	gw.Register(&WhoRouter{})

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost,
		Path:   "/rpc/Who/me",
		Header: Header{"Authorization": "Bearer evil-token"},
	})
	if resp.Status != 401 {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
}

func TestAuth_AnonymousRejectedByHandler(t *testing.T) {
	gw := New()
	gw.RegisterAuth(&AuthRouter{})
	gw.Register(&WhoRouter{})

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost,
		Path:   "/rpc/Who/me",
		Header: Header{},
	})
	if resp.Status != 401 {
		t.Fatalf("status = %d", resp.Status)
	}
}

func TestAuth_CachedAcrossCalls(t *testing.T) {
	gw := New()
	calls := 0
	gw.RegisterAuth(&CountingAuthRouter{baseline: &AuthRouter{}, calls: &calls})
	gw.Register(&WhoRouter{})

	req := &Request{Method: http.MethodPost, Path: "/rpc/Who/me", Header: Header{"Authorization": "Bearer good-bob"}}
	for i := 0; i < 5; i++ {
		resp := gw.Handle(context.Background(), req)
		if resp.Status != 200 {
			t.Fatalf("iter %d: status = %d", i, resp.Status)
		}
	}
	if calls != 1 {
		t.Fatalf("verify called %d times, want 1 (cache miss only on first)", calls)
	}
}

// CountingAuthRouter wraps AuthRouter to count verify invocations so
// the cache-hit test can assert "verify called only on cache miss".
type CountingAuthRouter struct {
	baseline *AuthRouter
	calls    *int
}

func (c *CountingAuthRouter) Verify(ctx *rpc.Context, p *VerifyParams) (*Claims, error) {
	*c.calls++
	return c.baseline.Verify(ctx, p)
}

// sealerPlugin is a test-only HeaderInjector that mirrors what the
// builtin/HMACSeal plugin does — kept inline here to avoid an import
// cycle (gateway package can't import gateway/builtin).
type sealerPlugin struct{ secret []byte }

func (s sealerPlugin) PluginName() string { return "test-sealer" }
func (s sealerPlugin) InjectHeaders(_ context.Context, _ *Request, h *http.Request) error {
	if h.Header.Get(HeaderSubject) == "" {
		return nil
	}
	h.Header.Set(hmacsealproto.HeaderSeal, hmacsealproto.Sign(h.Header, s.secret))
	return nil
}

func TestSeal_InjectAndVerify(t *testing.T) {
	secret := []byte("topsecret")
	var seenSubject, seenSeal string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenSubject = r.Header.Get(HeaderSubject)
		seenSeal = r.Header.Get(hmacsealproto.HeaderSeal)
		if !hmacsealproto.Verify(r.Header, secret) {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"message":"bad seal"}}`))
			return
		}
		_, _ = io.WriteString(w, `{"data":{"ok":true}}`)
	}))
	defer upstream.Close()

	gw := New()
	if err := gw.Use(sealerPlugin{secret: secret}); err != nil {
		t.Fatalf("Use sealer: %v", err)
	}
	gw.RegisterRemote("Remote", upstream.URL, time.Minute)

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost,
		Path:   "/rpc/Remote/x",
		Header: Header{},
		Body:   []byte(`{"args":[]}`),
		User:   &Claims{Subject: "alice", Issuer: "test"},
	})
	if resp.Status != 200 {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
	if seenSubject != "alice" {
		t.Fatalf("X-Sov-Subject = %q", seenSubject)
	}
	if seenSeal == "" {
		t.Fatal("X-Sov-Seal not injected")
	}
}

func TestSeal_TamperedDetected(t *testing.T) {
	secret := []byte("topsecret")
	h := http.Header{}
	h.Set(HeaderSubject, "alice")
	h.Set(HeaderIssuer, "test")
	h.Set(hmacsealproto.HeaderSeal, hmacsealproto.Sign(h, secret))

	if !hmacsealproto.Verify(h, secret) {
		t.Fatal("clean seal should verify")
	}

	h.Set(HeaderSubject, "root")
	if hmacsealproto.Verify(h, secret) {
		t.Fatal("tampered seal must not verify")
	}
}

// ---- Authz ----------------------------------------------------------------

type AuthzRouter struct {
	denyMethod  string // if set, denies any call whose target method equals this
	publicAll   bool   // if true, allow even anonymous; otherwise anonymous→Authenticate
	denyAnonNon bool   // if true, anonymous gets 401 on any non-public method
}

func (r *AuthzRouter) Check(ctx *rpc.Context, p *CheckParams) (*AuthzDecision, error) {
	if r.denyMethod != "" && p.Method == r.denyMethod {
		return &AuthzDecision{Allow: false, Reason: "method " + p.Method + " denied"}, nil
	}
	if p.Claims == nil && r.denyAnonNon {
		return &AuthzDecision{Allow: false, Authenticate: true, Reason: "login required"}, nil
	}
	return &AuthzDecision{Allow: true}, nil
}

func TestAuthz_AllowAndDeny(t *testing.T) {
	gw := New()
	gw.RegisterAuth(&AuthRouter{})
	gw.RegisterAuthz(&AuthzRouter{denyMethod: "deleteAll"})
	gw.Register(&WhoRouter{})

	ok := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Who/me",
		Header: Header{"Authorization": "Bearer good-x"},
	})
	if ok.Status != 200 {
		t.Fatalf("allow status = %d", ok.Status)
	}

	bad := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Who/deleteAll",
		Header: Header{"Authorization": "Bearer good-x"},
	})
	if bad.Status != 403 {
		t.Fatalf("deny status = %d, body = %s", bad.Status, bad.Body)
	}
}

// Anonymous request flows through the authz service. {Authenticate:true}
// turns into 401 UNAUTHORIZED (not 403 FORBIDDEN) — the authz service
// owns the "this method requires auth" decision, not the gateway.
func TestAuthz_AnonymousAuthenticateDecision(t *testing.T) {
	gw := New()
	gw.RegisterAuth(&AuthRouter{})
	gw.RegisterAuthz(&AuthzRouter{denyAnonNon: true})
	gw.Register(&WhoRouter{})

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Who/me",
		Header: Header{},
	})
	if resp.Status != 401 {
		t.Fatalf("anonymous non-public status = %d, body = %s", resp.Status, resp.Body)
	}
	if !strings.Contains(string(resp.Body), "login required") {
		t.Fatalf("body = %s", resp.Body)
	}
}

// Anonymous requests must be sent to the authz service when bound — the
// pre-fix middleware skipped on nil claims, masking misconfiguration.
func TestAuthz_AnonymousIsEvaluated(t *testing.T) {
	gw := New()
	gw.RegisterAuthz(&AuthzRouter{denyAnonNon: true})
	gw.Register(&WhoRouter{})

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Who/me",
		Header: Header{},
	})
	if resp.Status != 401 {
		t.Fatalf("anonymous w/o auth verifier still must hit authz: status = %d", resp.Status)
	}
}

// ---- _register-driven auth binding ---------------------------------------

func TestAuth_RegisterFlagBindsRemoteAuth(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Args json.RawMessage `json:"args"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		var p VerifyParams
		_ = json.Unmarshal(body.Args, &p)
		if p.Token != "good-bob" {
			w.WriteHeader(401)
			_, _ = w.Write([]byte(`{"error":{"message":"bad","code":"UNAUTHORIZED"}}`))
			return
		}
		_, _ = w.Write([]byte(`{"data":{"sub":"u_bob","exp":"2030-01-01T00:00:00Z"}}`))
	}))
	defer upstream.Close()

	gw := newRegistryGateway()
	gw.Register(&WhoRouter{})

	body, _ := json.Marshal(map[string]any{
		"name":                       "Auth",
		"address":                    upstream.URL,
		"heartbeat_interval_seconds": 30,
		"auth":                       true,
		"verify":                     "verify",
	})
	regResp := gw.Handle(context.Background(), &Request{Method: http.MethodPost, Path: "/rpc/_register", Header: Header{}, Body: body})
	if regResp.Status != 200 {
		t.Fatalf("register status = %d, body = %s", regResp.Status, regResp.Body)
	}

	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/Who/me",
		Header: Header{"Authorization": "Bearer good-bob"},
	})
	if resp.Status != 200 || !strings.Contains(string(resp.Body), `"u_bob"`) {
		t.Fatalf("status = %d, body = %s", resp.Status, resp.Body)
	}
}
