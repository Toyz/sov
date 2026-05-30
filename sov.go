// Package sov is the top-level façade for the sov framework. It
// re-exports the most-used types and constructors from sov/rpc,
// sov/gateway, and sov/signing so the 80% case can write `sov.X`
// against a single import.
//
//	import "github.com/Toyz/sov"
//
//	func main() {
//	    gw := sov.New()
//	    gw.Use(registry.New())
//	    gw.Register(&MyRouter{})
//	    log.Fatal(gw.ListenAndServe(ctx, ":8080"))
//	}
//
// Power users who need transport adapters, mesh wiring, or the raw
// rpc.Engine reach for the underlying packages directly.
package sov

import (
	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/preset"
	"github.com/Toyz/sov/rpc"
	"github.com/Toyz/sov/signing"
)

// ----------------------------------------------------------------------------
// Core types
// ----------------------------------------------------------------------------

type (
	// Gateway is the single entry point. Wraps a rpc.Engine + resolver
	// chain behind a pluggable Server. See gateway.Gateway.
	Gateway = gateway.Gateway
	// Context is the per-request value handed to every router method.
	Context = rpc.Context
	// Claims is the verified caller identity. Identity + delegation
	// only — authorization state lives in the AuthzService.
	Claims = rpc.Claims
	// Client is the cross-service caller interface. Either an
	// in-process LocalClient or an HTTP-backed NewClient(baseURL).
	Client = gateway.Client
	// ClaimsCache is the verified-claims cache contract. Implement it
	// (e.g. Redis-backed) and pass WithClaimsCache to share auth-verify
	// results across gateway replicas. Default is in-memory, per-replica.
	ClaimsCache = gateway.ClaimsCache
	// Error is the canonical wire-shaped error.
	Error = rpc.Error
	// Middleware wraps the gateway's request handler.
	Middleware = gateway.Middleware
	// Handler is the gateway's per-request callable.
	Handler = gateway.Handler
	// Request is the transport-agnostic request shape.
	Request = gateway.Request
	// Response is the transport-agnostic response shape.
	Response = gateway.Response
	// AuthService is the contract gw.RegisterAuth requires.
	AuthService = gateway.AuthService
	// AuthzService is the contract gw.RegisterAuthz requires.
	AuthzService = gateway.AuthzService
	// AuthzDecision is the shape an AuthzService.Check must return.
	AuthzDecision = gateway.AuthzDecision
	// VerifyParams is what the gateway sends to AuthService.Verify.
	VerifyParams = gateway.VerifyParams
	// CheckParams is what the gateway sends to AuthzService.Check.
	CheckParams = gateway.CheckParams
	// MeshOptions configures gw.JoinMesh.
	MeshOptions = gateway.MeshOptions
	// RoleFlag is the bit set a pod self-declares on _register.
	RoleFlag = gateway.RoleFlag
	// Option mutates gateway construction.
	Option = gateway.Option
)

// ----------------------------------------------------------------------------
// Construction
// ----------------------------------------------------------------------------

var (
	// New returns a Gateway.
	New = gateway.New
	// NewClient returns a Client that POSTs against baseURL.
	NewClient = gateway.NewClient
	// LocalRouters returns every wire-named router on gw — convenience
	// for team gateways that federate "everything I host".
	LocalRouters = gateway.LocalRouters
	// NormalizeUpstreamURL is the canonical form used by every
	// federation layer for identity comparisons.
	NormalizeUpstreamURL = gateway.NormalizeUpstreamURL
	// WithServer overrides the HTTP server.
	WithServer = gateway.WithServer
	// WithResolver overrides the resolver chain.
	WithResolver = gateway.WithResolver
	// WithMiddleware appends consumer middleware.
	WithMiddleware = gateway.WithMiddleware
	// WithProxyClient overrides the http.Client used for remote proxy.
	WithProxyClient = gateway.WithProxyClient
	// WithClaimsCache overrides the verified-claims cache (default
	// in-memory, per-replica). Pass a shared impl (e.g. Redis) so a fleet
	// of gateway replicas reuse each other's auth-verify results.
	WithClaimsCache = gateway.WithClaimsCache
	// WithTrustUpstreamClaims tells the default Server to trust inbound X-Sov-*.
	WithTrustUpstreamClaims = gateway.WithTrustUpstreamClaims
	// WithAdvertiseURL stamps the gateway's public URL on every outbound proxy hop as X-Sov-Upstream.
	WithAdvertiseURL = gateway.WithAdvertiseURL
	// WithHTTPClient overrides the underlying *http.Client for sov.NewClient.
	WithHTTPClient = gateway.WithHTTPClient
	// UseSigning wires the zero-trust signing middleware.
	UseSigning = signing.UseSigning
	// NewMonolith returns a gateway pre-loaded with preset.Monolith.
	NewMonolith = preset.NewMonolith
	// NewPod returns a gateway pre-loaded with preset.Pod.
	NewPod = preset.NewPod
	// NewRegistry returns a gateway pre-loaded with preset.Registry.
	NewRegistry = preset.NewRegistry
	// NewHybrid returns a gateway pre-loaded with preset.Hybrid.
	NewHybrid = preset.NewHybrid
	// HaltErr wraps an error so a boot-time hook refuses startup.
	HaltErr = gateway.HaltErr
	// RespondErr wraps an error with a *Response the gateway returns.
	RespondErr = gateway.RespondErr
	// IsHalt reports whether err carries a HaltErr sentinel.
	IsHalt = gateway.IsHalt
)

// Preset config type aliases — let consumers populate the preset
// configs without importing the preset package directly.
type (
	MonolithConfig = preset.MonolithConfig
	PodConfig      = preset.PodConfig
	RegistryConfig = preset.RegistryConfig
	HybridConfig   = preset.HybridConfig
)

// ClientOption mutates a gateway HTTP client constructed via NewClient.
type ClientOption = gateway.ClientOption

// NOTE: plugin sub-interfaces (Plugin, HeaderInjector, ConfigApplier,
// SealVerifier, RecoveryHandler, …) are intentionally NOT re-exported
// here. There are ~20 of them; re-exporting a subset was deceptive
// (authors hit a wall the moment they needed one of the un-aliased ones).
// Plugin authors import "github.com/Toyz/sov/gateway" directly and
// implement gateway.X — a single honest import for the whole plugin
// surface. The sov.* façade stays focused on the app-author 80% case
// (New/Use/Register/Run + Claims + error constructors).

// ----------------------------------------------------------------------------
// Role flags
// ----------------------------------------------------------------------------

const (
	RoleAuth  = gateway.RoleAuth
	RoleAuthz = gateway.RoleAuthz
)

// ----------------------------------------------------------------------------
// Helpers + errors
// ----------------------------------------------------------------------------

var (
	// RequireSubject extracts the authenticated subject id from ctx, or
	// returns 401 UNAUTHORIZED. The canonical handler-side gate.
	RequireSubject = rpc.RequireSubject
	// UserFromContext returns ctx.User or 401 UNAUTHORIZED.
	UserFromContext = rpc.UserFromContext

	// Common error constructors.
	NotFound        = rpc.NotFound
	Forbidden       = rpc.Forbidden
	ForbiddenCode   = rpc.ForbiddenCode
	Unauthorized    = rpc.Unauthorized
	BadRequest      = rpc.BadRequest
	BadRequestCode  = rpc.BadRequestCode
	Conflict        = rpc.Conflict
	Internal        = rpc.Internal
	NotImplemented  = rpc.NotImplemented
	TooManyRequests = rpc.TooManyRequests

	// Typed header accessors.
	ClaimsFromHeaders        = gateway.ClaimsFromHeaders
	AuthorizationFromContext = gateway.AuthorizationFromContext
)
