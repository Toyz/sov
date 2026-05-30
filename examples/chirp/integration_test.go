// Package chirp_test is an in-process integration test that builds the
// chirp monolith — same wiring as examples/chirp/cmd/monolith/main.go —
// and exercises the cascading-batch endpoint end-to-end.
//
// Monolith mode has no remote pods to coalesce; the test verifies that
// the response shape, alias preservation, and identity propagation all
// hold against the real handler stack. Mesh-mode rebatching has its
// own coverage in gateway/batch_test.go.
package chirp_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/Toyz/sov"
	"github.com/Toyz/sov/examples/chirp/handlers/auth"
	"github.com/Toyz/sov/examples/chirp/handlers/authz"
	"github.com/Toyz/sov/examples/chirp/handlers/chirps"
	"github.com/Toyz/sov/examples/chirp/handlers/feed"
	"github.com/Toyz/sov/examples/chirp/handlers/users"
	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/batch"
	"github.com/Toyz/sov/gateway/builtin/registry"
)

// buildMonolith mirrors examples/chirp/cmd/monolith/main.go so the test
// exercises the exact same wiring users see in the demo binary.
func buildMonolith(t *testing.T) *sov.Gateway {
	t.Helper()
	gw := sov.New()
	if err := gw.Use(registry.New(registry.Config{})); err != nil {
		t.Fatalf("Use registry: %v", err)
	}
	if err := gw.Use(batch.New(batch.Config{})); err != nil {
		t.Fatalf("Use batch: %v", err)
	}
	// Auto-bind via interface detection.
	gw.Register(&auth.AuthRouter{
		Credentials: auth.NewCredentialStore(),
		Sessions:    auth.NewSessionStore(),
	})
	gw.Register(authz.NewAuthzRouter())
	gw.Register(&users.UserRouter{Store: users.NewMemoryStore()})
	gw.Register(&chirps.ChirpRouter{Store: chirps.NewMemoryStore()})
	gw.Register(&feed.FeedRouter{Client: feed.NewClientAdapter(gw.LocalClient())})
	return gw
}

// callJSON POSTs a synthetic request through the gateway's Handle entry
// point (same path the HTTP server uses) and returns (status, body).
func callJSON(t *testing.T, gw *sov.Gateway, path, body, bearer string) (int, []byte) {
	t.Helper()
	hdr := gateway.Header{"Content-Type": "application/json"}
	if bearer != "" {
		hdr["Authorization"] = "Bearer " + bearer
	}
	resp := gw.Handle(context.Background(), &gateway.Request{
		Method: http.MethodPost,
		Path:   path,
		Header: hdr,
		Body:   []byte(body),
	})
	return resp.Status, resp.Body
}

// signUp runs the chirp two-call sign-up + login dance and returns the
// freshly-issued session token.
func signUp(t *testing.T, gw *sov.Gateway, handle string) (subject, token string) {
	t.Helper()
	status, body := callJSON(t, gw, "/rpc/Auth/register",
		`{"args":{"handle":"`+handle+`","password":"pw"}}`, "")
	if status != 200 {
		t.Fatalf("Auth.register %s: %d %s", handle, status, body)
	}
	var reg struct {
		Data struct {
			Subject string `json:"subject"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &reg)
	subject = reg.Data.Subject

	status, _ = callJSON(t, gw, "/rpc/User/register",
		`{"args":{"subject":"`+subject+`","handle":"`+handle+`","display":"`+strings.ToUpper(handle[:1])+handle[1:]+`"}}`, "")
	if status != 200 {
		t.Fatalf("User.register %s: status %d", handle, status)
	}

	status, body = callJSON(t, gw, "/rpc/Auth/login",
		`{"args":{"handle":"`+handle+`","password":"pw"}}`, "")
	if status != 200 {
		t.Fatalf("Auth.login %s: %d %s", handle, status, body)
	}
	var lg struct {
		Data struct {
			Token string `json:"token"`
		} `json:"data"`
	}
	_ = json.Unmarshal(body, &lg)
	token = lg.Data.Token
	return subject, token
}

// TestChirp_CascadingBatch_Monolith hits the 4-call batch from
// walkthrough.sh step 14 against the in-process monolith and asserts
// every alias resolves cleanly. Same shape mesh-mode would return; the
// monolith path is the regression guard that the cascading-batch
// refactor did not break local dispatch.
func TestChirp_CascadingBatch_Monolith(t *testing.T) {
	gw := buildMonolith(t)
	aliceSub, aliceTok := signUp(t, gw, "alice")
	bobSub, bobTok := signUp(t, gw, "bob")

	// Bob posts one chirp.
	status, body := callJSON(t, gw, "/rpc/Chirp/post",
		`{"args":{"body":"hello from bob"}}`, bobTok)
	if status != 200 {
		t.Fatalf("Chirp.post: %d %s", status, body)
	}

	// Cascading-batch — 4 calls: User.get + Chirp.list + 2× listByAuthors.
	batchBody := `{"calls":{
		"alice":   {"service":"User",  "method":"get",          "args":{"id":"` + aliceSub + `"}},
		"list":    {"service":"Chirp", "method":"list",         "args":{"limit":5}},
		"byBob":   {"service":"Chirp", "method":"listByAuthors","args":{"author_ids":["` + bobSub + `"],"limit":5}},
		"byAlice": {"service":"Chirp", "method":"listByAuthors","args":{"author_ids":["` + aliceSub + `"],"limit":5}}
	}}`
	status, body = callJSON(t, gw, "/rpc/_batch", batchBody, aliceTok)
	if status != 200 {
		t.Fatalf("/rpc/_batch: %d %s", status, body)
	}

	var br gateway.BatchResponse
	if err := json.Unmarshal(body, &br); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, alias := range []string{"alice", "list", "byBob", "byAlice"} {
		if _, ok := br.Results[alias]; !ok {
			t.Fatalf("missing alias %q in %v", alias, br.Results)
		}
		if strings.Contains(string(br.Results[alias]), `"error"`) {
			t.Errorf("alias %q returned error: %s", alias, br.Results[alias])
		}
	}
	if !strings.Contains(string(br.Results["alice"]), `"display":"Alice"`) {
		t.Errorf("alice missing display: %s", br.Results["alice"])
	}
	if !strings.Contains(string(br.Results["byBob"]), "hello from bob") {
		t.Errorf("byBob missing chirp: %s", br.Results["byBob"])
	}
	if !strings.Contains(string(br.Results["byAlice"]), `"chirps":null`) {
		t.Errorf("byAlice should be empty: %s", br.Results["byAlice"])
	}
	_ = aliceTok
}

// TestChirp_Batch_AuthzGatesAnonymous proves authz still fires inside
// batched entries — the per-entry path goes through the same
// middleware chain as single-call dispatch.
func TestChirp_Batch_AuthzGatesAnonymous(t *testing.T) {
	gw := buildMonolith(t)
	_, bobTok := signUp(t, gw, "bob")
	bobSub, _ := signUp(t, gw, "carol")
	_ = bobSub

	// Anonymous batch — User.get is public so it should succeed;
	// Chirp.post is gated and should fail with UNAUTHORIZED.
	body := `{"calls":{
		"profile":   {"service":"User", "method":"get",  "args":{"id":"u_bob"}},
		"protected": {"service":"Chirp","method":"post", "args":{"body":"sneaky"}}
	}}`
	status, raw := callJSON(t, gw, "/rpc/_batch", body, "")
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, raw)
	}
	var br gateway.BatchResponse
	_ = json.Unmarshal(raw, &br)
	if !strings.Contains(string(br.Results["profile"]), `"data"`) {
		t.Errorf("public profile lookup should succeed: %s", br.Results["profile"])
	}
	if !strings.Contains(string(br.Results["protected"]), "UNAUTHORIZED") {
		t.Errorf("authz must gate Chirp.post inside batch: %s", br.Results["protected"])
	}

	// Authenticated batch — Chirp.post should now succeed for Bob.
	status, raw = callJSON(t, gw, "/rpc/_batch", body, bobTok)
	if status != 200 {
		t.Fatalf("status=%d body=%s", status, raw)
	}
	_ = json.Unmarshal(raw, &br)
	if !strings.Contains(string(br.Results["protected"]), `"data"`) {
		t.Errorf("authed Chirp.post via batch should succeed: %s", br.Results["protected"])
	}
}
