package gateway

import (
	"context"
	"io"
	"net/http"
)

// Server abstracts the HTTP runtime so the gateway is not coupled to any
// one router/framework. The default implementation (NewNetHTTPServer) is
// built into this package and uses net/http; consumers who want fiber,
// fasthttp, echo, or anything else implement Server themselves and pass
// it via WithServer.
//
// The contract: Server must route every incoming POST request whose path
// starts with /rpc/ to the gateway. The gateway tells Server how to do
// that via Handle. Server then calls the registered RequestHandler with
// the parsed Request and writes whatever ResponseWriter the gateway
// returns back to the client. Server is responsible for the network
// (listening, TLS, keep-alives, etc.); the gateway is responsible for
// the protocol.
type Server interface {
	// Handle registers the single catch-all handler for /rpc/* requests.
	// Server must invoke it for every POST under /rpc/.
	Handle(h RequestHandler)
	// ListenAndServe binds and serves until ctx is cancelled.
	ListenAndServe(ctx context.Context, addr string) error
}

// RequestHandler is what the gateway hands to Server.Handle. Server
// constructs a Request from the inbound HTTP request and passes it; the
// returned Response is written back to the client.
type RequestHandler func(ctx context.Context, req *Request) *Response

// Request is the transport-neutral inbound RPC. Server adapters
// populate this from whatever HTTP types they use.
type Request struct {
	// Method is the HTTP method, e.g. "POST" or "GET". Most business
	// calls are POST; framework endpoints like /rpc/_health are GET.
	Method string
	// Path is the full URL path, e.g. "/rpc/WidgetService/create".
	Path string
	// Header is the inbound HTTP headers, with sov-namespaced inbound
	// headers already stripped by Server.
	Header Header
	// Body is the request body, fully buffered. Empty body is allowed.
	Body []byte
	// RemoteIP is the caller's source IP. Server picks it from
	// X-Forwarded-For or the transport-level remote address.
	RemoteIP string
	// User is the authenticated subject, set by Server-side middleware
	// before dispatch. nil for anonymous calls. Gateway copies this onto
	// the rpc.Context handed to handlers.
	User any
}

// Response is what the RequestHandler returns. Server writes it back.
type Response struct {
	// Status is the HTTP status code.
	Status int
	// Header carries response headers (Content-Type etc.). Server merges
	// these onto its response.
	Header Header
	// Body is the response payload.
	Body []byte
	// Mode is the dispatch-mode hint set by the gateway's internal
	// dispatch path (local/remote/federated/framework/plugin). Surfaces
	// in DispatchEvent.Mode for observability. Plugins that build a
	// Response from scratch should leave this empty; the framework
	// fills it in at known dispatch sites.
	Mode string
}

// Header is a transport-neutral header map. Multi-value headers are
// represented as comma-joined strings — the gateway never needs more.
type Header map[string]string

// Get returns the value for key, case-insensitively. It looks up the
// canonical form (http.CanonicalHeaderKey) first — which is how inbound
// headers and Set store keys — then falls back to an exact match so a
// literal-constructed Header still resolves. Callers no longer need to try
// both "Authorization" and "authorization".
func (h Header) Get(key string) string {
	if v, ok := h[http.CanonicalHeaderKey(key)]; ok {
		return v
	}
	return h[key]
}

// Set replaces the value for key, storing it under its canonical form so
// later Get lookups are case-insensitive.
func (h Header) Set(key, value string) { h[http.CanonicalHeaderKey(key)] = value }

// Clone returns a shallow copy of h. Use it when a request's header map
// is handed to concurrent sub-dispatches (e.g. batch fan-out): plugins
// like requestid MUTATE req.Header, so parallel handlers must each get
// their own map or they race on the shared one (fatal concurrent map
// access). Returns nil for a nil receiver.
func (h Header) Clone() Header {
	if h == nil {
		return nil
	}
	out := make(Header, len(h))
	for k, v := range h {
		out[k] = v
	}
	return out
}

// Bytes returns body — convenience for io.Reader sources.
func bodyFromReader(r io.Reader, max int64) ([]byte, error) {
	if r == nil {
		return nil, nil
	}
	return io.ReadAll(io.LimitReader(r, max))
}
