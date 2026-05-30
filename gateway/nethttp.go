package gateway

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"time"
)

// NetHTTPServer is the default Server implementation. Production-grade
// defaults: 10s header timeout, 60s write timeout, configurable max body
// (default 4 MiB). Consumers who need different limits or TLS pass a
// pre-configured *http.Server via NetHTTPOptions.HTTPServer.
type NetHTTPServer struct {
	opts    NetHTTPOptions
	handler RequestHandler
	// trustGuard, when set, is consulted on inbound X-Sov-* bundles:
	// the bundle is kept only if the guard returns true. The gateway
	// wires this from its HMAC secret + upstream allowlist after
	// construction (see Gateway.wireTrustGuard).
	trustGuard func(req *http.Request, body []byte) bool
	// headerClaim, when set, is consulted on every otherwise-stripped
	// header: returning true preserves the header (bypasses the
	// identity-strip). Gateway wires this with a closure over all
	// HeaderClaimer plugins so claimed headers reach req.Header intact.
	headerClaim func(canonicalName string) bool
}

// SetTrustGuard wires a predicate that decides whether to KEEP an
// inbound X-Sov-* bundle (only meaningful when TrustUpstreamClaims is
// true). False means strip the claims; the request still flows through
// — auth gating is the authz service's job, not the trust layer.
func (s *NetHTTPServer) SetTrustGuard(fn func(req *http.Request, body []byte) bool) {
	s.trustGuard = fn
}

// SetHeaderClaim wires the predicate consulted before stripping an
// identity-shaped header. Returning true preserves the header so a
// plugin that claimed it can read it from req.Header.
func (s *NetHTTPServer) SetHeaderClaim(fn func(canonicalName string) bool) {
	s.headerClaim = fn
}

// NetHTTPOptions configures NetHTTPServer.
type NetHTTPOptions struct {
	// MaxBodyBytes caps the request body size. 0 → default 4 MiB.
	MaxBodyBytes int64
	// HTTPServer, if set, is used verbatim and Addr/timeouts are
	// honored. Otherwise the constructor builds a server with sensible
	// defaults using ListenAndServe's addr.
	HTTPServer *http.Server
	// TrustUpstreamClaims controls inbound X-Sov-* handling.
	//
	//   false (default): strip every inbound X-Sov-* header. The right
	//     answer for any gateway facing the public internet — clients
	//     could otherwise smuggle X-Sov-Subject: admin.
	//
	//   true: pass X-Sov-* through. The right answer for downstream
	//     service pods that sit behind a trusted upstream gateway which
	//     itself injects the verified claims (and ideally HMAC-seals
	//     them — pair with hmacseal/proto.Verify middleware to cryptographically
	//     verify rather than trust the network alone).
	TrustUpstreamClaims bool
}

// NewNetHTTPServer returns a Server backed by net/http.
func NewNetHTTPServer(opts NetHTTPOptions) *NetHTTPServer {
	if opts.MaxBodyBytes == 0 {
		opts.MaxBodyBytes = 4 << 20 // 4 MiB
	}
	return &NetHTTPServer{opts: opts}
}

// Handle implements Server.
func (s *NetHTTPServer) Handle(h RequestHandler) { s.handler = h }

// ListenAndServe implements Server.
func (s *NetHTTPServer) ListenAndServe(ctx context.Context, addr string) error {
	if s.handler == nil {
		return errors.New("gateway: NetHTTPServer.ListenAndServe called before Handle")
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", s.serve)

	srv := s.opts.HTTPServer
	if srv == nil {
		srv = &http.Server{
			ReadHeaderTimeout: 10 * time.Second,
			WriteTimeout:      60 * time.Second,
			IdleTimeout:       120 * time.Second,
		}
	}
	srv.Addr = addr
	srv.Handler = mux

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return ctx.Err()
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func (s *NetHTTPServer) serve(w http.ResponseWriter, r *http.Request) {
	if !strings.HasPrefix(r.URL.Path, "/rpc/") && r.URL.Path != "/health" {
		http.NotFound(w, r)
		return
	}

	body, err := bodyFromReader(http.MaxBytesReader(w, r.Body, s.opts.MaxBodyBytes), s.opts.MaxBodyBytes)
	if err != nil {
		http.Error(w, "request too large", http.StatusRequestEntityTooLarge)
		return
	}

	// When TrustUpstreamClaims is true, the gateway-wired trust guard
	// gets to vet the inbound X-Sov-* bundle. If the guard says no, we
	// strip the bundle and proceed anonymous — never 401 here; auth
	// gating belongs to the authz service.
	trustClaims := s.opts.TrustUpstreamClaims
	if trustClaims && s.trustGuard != nil && !s.trustGuard(r, body) {
		trustClaims = false
	}

	hdr := Header{}
	for k, v := range r.Header {
		ks := http.CanonicalHeaderKey(k)
		if !trustClaims && isIdentityHeader(ks) {
			if s.headerClaim == nil || !s.headerClaim(ks) {
				continue // strip inbound smuggled IDENTITY claim headers
			}
		}
		hdr[ks] = strings.Join(v, ",")
	}

	req := &Request{
		Method:   r.Method,
		Path:     r.URL.Path,
		Header:   hdr,
		Body:     body,
		RemoteIP: remoteIPFromHTTP(r),
	}

	resp := s.handler(r.Context(), req)
	if resp == nil {
		http.Error(w, "internal: nil response", http.StatusInternalServerError)
		return
	}
	for k, v := range resp.Header {
		w.Header().Set(k, v)
	}
	if _, ok := resp.Header["Content-Type"]; !ok {
		w.Header().Set("Content-Type", "application/json")
	}
	w.WriteHeader(resp.Status)
	_, _ = w.Write(resp.Body)
}

// isIdentityHeader returns true for the X-Sov-* headers that carry
// caller IDENTITY (subject/issuer/scopes/expires/seal). These are
// the anti-smuggling targets — an untrusted edge POSTing
// X-Sov-Subject: admin could otherwise impersonate. Framework
// signaling headers (X-Sov-Register-*, X-Sov-Introspect-*,
// X-Sov-Request-Id, X-Sov-Upstream) are NOT identity claims and pass
// through so plugins can read them.
func isIdentityHeader(canonical string) bool {
	switch canonical {
	case "X-Sov-Subject", "X-Sov-Issuer", "X-Sov-Scopes", "X-Sov-Expires", "X-Sov-Seal":
		return true
	}
	return false
}

func remoteIPFromHTTP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
