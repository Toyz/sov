package gateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	. "github.com/Toyz/sov/gateway"
	meshsecretproto "github.com/Toyz/sov/gateway/builtin/meshsecret/proto"
)

// postSignedRegister issues a signed POST /rpc/_register with body and
// asserts a 200. Returns the response for further inspection.
func postSignedRegister(t *testing.T, gw *Gateway, secret, body []byte) *Response {
	t.Helper()
	sig, ts := meshsecretproto.Sign(secret, body, time.Now())
	resp := gw.Handle(context.Background(), &Request{
		Method: http.MethodPost, Path: "/rpc/_register",
		Header: Header{meshsecretproto.RegisterSigHeader: sig, meshsecretproto.RegisterTsHeader: ts},
		Body:   body,
	})
	if resp.Status != 200 {
		t.Fatalf("register/deregister should be 200, got %d body=%s", resp.Status, resp.Body)
	}
	return resp
}

func deregisterBody(name, address string) []byte {
	if address == "" {
		address = "http://" + name + ":9000"
	}
	out, _ := json.Marshal(map[string]any{"name": name, "address": address, "deregister": true})
	return out
}

func TestRegister_Deregisters(t *testing.T) {
	secret := []byte("topsecret")
	gw := useMeshSecret(t, secret)

	postSignedRegister(t, gw, secret, registerBody("Feed", ""))
	if _, ok := gw.Resolver().Resolve(context.Background(), "Feed"); !ok {
		t.Fatal("Feed should be registered")
	}

	postSignedRegister(t, gw, secret, deregisterBody("Feed", ""))
	if _, ok := gw.Resolver().Resolve(context.Background(), "Feed"); ok {
		t.Fatal("Feed should be gone after deregister")
	}
}

func TestRegister_DeregisterIgnoresForeignAddress(t *testing.T) {
	secret := []byte("topsecret")
	gw := useMeshSecret(t, secret)

	// Feed is owned by http://Feed:9000.
	postSignedRegister(t, gw, secret, registerBody("Feed", ""))
	// A deregister from a DIFFERENT address must not evict it — models a
	// stale deregister arriving after a newer pod took the service over.
	postSignedRegister(t, gw, secret, deregisterBody("Feed", "http://someone-else:9999"))
	if _, ok := gw.Resolver().Resolve(context.Background(), "Feed"); !ok {
		t.Fatal("foreign-address deregister must not remove a service it does not own")
	}
}
