package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"time"

	"github.com/Toyz/sov/rpc"
)

// Client is the cross-service caller a router uses to invoke another
// service's method. Two implementations satisfy it:
//
//   - gw.LocalClient() — calls back into the same gateway's dispatch
//     in-process. No HTTP loopback, no JSON round-trip; the engine
//     handles the call directly. Right answer for monolith mode.
//
//   - gateway.NewClient(baseURL) — HTTP POST to a remote gateway.
//     Right answer for mesh pods making cross-service calls.
//
// Both auto-forward the verbatim inbound Authorization header so
// identity propagates across hops without manual header plumbing.
// Consumer code calls the same Call signature in either topology —
// that is the PEMM "wire IS the in-process API" contract made literal.
type Client interface {
	Call(ctx *rpc.Context, router, method string, params, out any) error
}

// NewClient returns a Client that POSTs /rpc/{router}/{method} against
// baseURL. The base URL has no trailing slash and no /rpc/ suffix.
func NewClient(baseURL string, opts ...ClientOption) Client {
	c := &httpClient{
		baseURL: baseURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
	for _, fn := range opts {
		fn(c)
	}
	return c
}

// ClientOption mutates a *httpClient.
type ClientOption func(*httpClient)

// WithHTTPClient overrides the underlying *http.Client.
func WithHTTPClient(c *http.Client) ClientOption {
	return func(h *httpClient) { h.http = c }
}

// LocalClient returns a Client that dispatches against this gateway
// directly — no HTTP, no JSON encoding overhead. The resolver chain
// still decides whether a given service call is local or proxied to a
// remote pod, so a "local" client can transparently reach remote
// services too. This is the PEMM "wire IS the in-process API" point.
func (g *Gateway) LocalClient() Client { return &localClient{gw: g} }

// httpClient implements Client over HTTP. Used by mesh pods that need
// to talk back to the central gateway for cross-service calls.
type httpClient struct {
	baseURL string
	http    *http.Client
}

func (c *httpClient) Call(ctx *rpc.Context, router, method string, params, out any) error {
	body, err := marshalRequest(params)
	if err != nil {
		return err
	}
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/rpc/"+router+"/"+method, bytes.NewReader(body))
	if err != nil {
		return rpc.Internal("client build request: %v", err)
	}
	hreq.Header.Set("Content-Type", "application/json")
	if auth := AuthorizationFromContext(ctx); auth != "" {
		hreq.Header.Set("Authorization", auth)
	}
	resp, err := c.http.Do(hreq)
	if err != nil {
		return rpc.Internal("client call %s/%s: %v", router, method, err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	return decodeEnvelope(respBody, resp.StatusCode, out)
}

// localClient implements Client by calling gw.Handle directly.
type localClient struct{ gw *Gateway }

func (c *localClient) Call(ctx *rpc.Context, router, method string, params, out any) error {
	body, err := marshalRequest(params)
	if err != nil {
		return err
	}
	hdr := Header{}
	if auth := AuthorizationFromContext(ctx); auth != "" {
		hdr["Authorization"] = auth
	}
	resp := c.gw.Handle(ctx, &Request{
		Method: http.MethodPost,
		Path:   "/rpc/" + router + "/" + method,
		Header: hdr,
		Body:   body,
		User:   ctx.User, // bypass re-verify when already-resolved Claims are on ctx
	})
	return decodeEnvelope(resp.Body, resp.Status, out)
}

func marshalRequest(params any) ([]byte, error) {
	var args json.RawMessage
	if params != nil {
		raw, err := json.Marshal(params)
		if err != nil {
			return nil, rpc.Internal("encode params: %v", err)
		}
		args = raw
	}
	body, err := json.Marshal(rpc.Request{Args: args})
	if err != nil {
		return nil, rpc.Internal("encode request: %v", err)
	}
	return body, nil
}

// decodeEnvelope is a thin alias for rpc.DecodeEnvelope (the canonical
// client-side decoder) so the two call sites above read unchanged.
func decodeEnvelope(body []byte, status int, out any) error {
	return rpc.DecodeEnvelope(body, status, out)
}

// Compile-time: localClient and httpClient satisfy Client.
var _ Client = (*localClient)(nil)
var _ Client = (*httpClient)(nil)
