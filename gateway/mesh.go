package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"

	meshsecretproto "github.com/Toyz/sov/gateway/builtin/meshsecret/proto"
	registertokenproto "github.com/Toyz/sov/gateway/builtin/registertoken/proto"
)

// RoleFlag is a bit-flag a pod uses to declare gateway-level roles to
// the upstream registry on /_register. The upstream binds those roles
// (auth verifier, authz checker) to the pod's wire name.
type RoleFlag uint32

const (
	// RoleAuth declares the pod hosts the auth verifier (AuthService).
	// Upstream gateway routes every inbound bearer token through this
	// pod's /rpc/{Service}/verify endpoint.
	RoleAuth RoleFlag = 1 << iota
	// RoleAuthz declares the pod hosts the policy-as-service hook.
	RoleAuthz
)

// Has reports whether r contains every bit in flag.
func (r RoleFlag) Has(flag RoleFlag) bool { return r&flag == flag }

// MeshOptions configures gw.JoinMesh.
type MeshOptions struct {
	// UpstreamURL is the base URL of the central registry, e.g.
	// "http://gateway:8080". Required.
	UpstreamURL string
	// Address is the local listen address (":9001"). Required.
	Address string
	// Advertise is the URL the upstream gateway will reach this pod on
	// (e.g. "http://auth-pod:9001"). Required.
	Advertise string
	// ServiceName, if set, overrides the pod's advertised wire name.
	// Defaults to the wire name of the single router registered on the
	// gateway — i.e. for a pod hosting just AuthRouter, defaults to
	// "Auth". When the pod hosts multiple routers, ServiceName MUST be
	// set explicitly.
	ServiceName string
	// Heartbeat is the registration refresh interval. Default 5s.
	// Upstream TTL is heartbeat × 3.
	Heartbeat time.Duration
	// Roles is the bit set of gateway-level roles to self-declare.
	// Use RoleAuth | RoleAuthz combos.
	Roles RoleFlag
	// HTTPClient overrides the http.Client used for /_register POSTs.
	HTTPClient *http.Client
	// Introspectable, when true, opts this pod into the central
	// gateway's /rpc/_introspect aggregation. Default false (zero
	// value) keeps surface minimal; chirp mesh sets it true so the
	// type catalog spans every pod.
	Introspectable bool
	// MeshSecret, when non-nil, signs every _register POST with
	// HMAC-SHA256 + a current timestamp. Required when the upstream
	// gateway was constructed WithMeshSecret(...); the bytes must
	// match. Without this, _register on a hardened gateway returns
	// 401 and the pod never joins the mesh.
	MeshSecret []byte
	// RegisterToken, when non-empty, is stamped as the X-Sov-Register-Token
	// header on every _register POST — the simple shared-bearer join gate
	// (see builtin/registertoken). Required when the upstream gateway runs
	// the registertoken plugin; the bytes must match. Independent of
	// MeshSecret (you may set either, both, or neither).
	RegisterToken []byte
	// Federate, when non-empty, advertises this gateway as a tiered
	// router that fronts every name in the slice. Upstream registers
	// all of them at Advertise via one POST + one heartbeat. The pod
	// path (single ServiceName) is bypassed when Federate is set.
	//
	// Use this for an EXPLICIT, fixed federation list. For a team gateway
	// whose service set changes at runtime (sub-pods come and go), prefer
	// FederateAll so the list can't drift from reality.
	Federate []string
	// FederateAll, when true, advertises this gateway's LIVE service set
	// (everything its resolver currently knows — local routers + sub-pods
	// registered to it), recomputed on every heartbeat. A service that
	// appears is federated upstream on the next beat; one that disappears
	// drops out of the heartbeat and TTL-expires upstream (~3 beats). This
	// keeps the upstream's introspect + health in sync with the team
	// gateway with zero hand-maintained lists. Takes precedence over
	// Federate when both are set.
	FederateAll bool
}

// LocalRouters returns every wire-named router registered on gw — a
// convenience for team gateways that want to federate "everything I
// host":
//
//	gw.JoinMesh(ctx, sov.MeshOptions{
//	    UpstreamURL: prime,
//	    Federate:    sov.LocalRouters(gw),
//	})
func LocalRouters(gw *Gateway) []string { return gw.engine.Routers() }

// RunMesh is the pod equivalent of Run — JoinMesh + SIGINT/SIGTERM
// graceful shutdown. Returns nil on signal-driven shutdown so
// callers can `log.Fatal(gw.RunMesh(ctx, opts))` without printing
// a spurious "<nil>" on Ctrl-C.
//
//	func main() {
//	    log.Fatal(sov.NewPod(cfg).RunMesh(ctx, sov.MeshOptions{...}))
//	}
func (g *Gateway) RunMesh(ctx context.Context, opts MeshOptions) error {
	return g.runWithSignals(ctx, func(ctx context.Context) error {
		return g.JoinMesh(ctx, opts)
	})
}

// JoinMesh starts the pod: registers with the upstream gateway,
// heartbeats on the configured interval, and serves until ctx is
// cancelled. Combines what the previous demo's hand-coded heartbeat()
// + ListenAndServe() did into one call.
//
// JoinMesh blocks until ctx is cancelled or the server returns an
// error. The heartbeat goroutine stops when ctx is cancelled.
func (g *Gateway) JoinMesh(ctx context.Context, opts MeshOptions) error {
	if opts.UpstreamURL == "" {
		return errors.New("gateway.JoinMesh: UpstreamURL is required")
	}
	if opts.Address == "" {
		return errors.New("gateway.JoinMesh: Address is required")
	}
	if opts.Advertise == "" {
		return errors.New("gateway.JoinMesh: Advertise is required")
	}
	federated := opts.FederateAll || len(opts.Federate) > 0
	name := opts.ServiceName
	if !federated {
		// Single-service pod path. Need a name.
		if name == "" {
			routers := g.engine.Routers()
			switch len(routers) {
			case 0:
				return errors.New("gateway.JoinMesh: no routers registered and ServiceName not set")
			case 1:
				name = routers[0]
			default:
				return fmt.Errorf("gateway.JoinMesh: %d routers registered; set ServiceName explicitly", len(routers))
			}
		}
	} else if name == "" {
		// Federated team gateway. Name is just a diagnostic label.
		name = "team-" + opts.Advertise
	}
	interval := opts.Heartbeat
	if interval <= 0 {
		interval = 5 * time.Second
	}
	hc := opts.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 5 * time.Second}
	}

	// introspectable may be flipped to true at runtime when the upstream
	// reports it's explorer-equipped. Read+written only on the heartbeat
	// goroutine, so a plain var is race-free.
	introspectable := opts.Introspectable

	// buildPayload renders the current register body. In FederateAll mode
	// it recomputes the federated service list from the gateway's live
	// resolver every call (so adds/removes can't drift); it returns nil
	// when there's nothing to advertise yet, signaling "skip this beat".
	buildPayload := func() []byte {
		federate := opts.Federate
		if opts.FederateAll {
			federate = g.resolver.Services()
			if len(federate) == 0 {
				return nil
			}
		}
		return buildRegisterPayload(name, opts.Advertise, interval, opts.Roles, introspectable, federate)
	}

	var payload atomic.Pointer[[]byte]
	if initial := buildPayload(); initial != nil {
		payload.Store(&initial)
	} else {
		placeholder := []byte("{}") // never POSTed; refresh() gates the empty case
		payload.Store(&placeholder)
	}

	// In FederateAll mode the heartbeat recomputes the payload each beat.
	var refresh func() []byte
	if opts.FederateAll {
		refresh = buildPayload
	}

	g.Log().Info("mesh: joining upstream",
		"name", name, "advertise", opts.Advertise, "upstream", opts.UpstreamURL,
		"interval", interval, "mesh_secret", len(opts.MeshSecret) > 0,
		"introspectable", opts.Introspectable, "federate", federated, "federate_all", opts.FederateAll)

	go runHeartbeat(ctx, hc, opts.UpstreamURL, &payload, interval, opts.MeshSecret, opts.RegisterToken, refresh, g.Log(),
		func() {
			// Upstream gateway force-flipped introspect=true (explorer
			// enabled there). Flip our flag so future heartbeats agree;
			// in static mode re-stamp the payload now (FederateAll's
			// per-beat refresh picks it up on its own).
			if !introspectable {
				introspectable = true
				if refresh == nil {
					np := buildPayload()
					payload.Store(&np)
				}
				g.Log().Info("mesh: upstream gateway force-enabled introspect (explorer-equipped); pod payload updated",
					"upstream", opts.UpstreamURL)
			}
		})

	return g.ListenAndServe(ctx, opts.Address)
}

func buildRegisterPayload(name, advertise string, interval time.Duration, roles RoleFlag, introspectable bool, federate []string) []byte {
	body := map[string]any{
		"name":                       name,
		"address":                    advertise,
		"heartbeat_interval_seconds": int(interval.Seconds()),
	}
	if roles.Has(RoleAuth) {
		body["auth"] = true
		body["verify"] = "verify"
	}
	if roles.Has(RoleAuthz) {
		body["authz"] = true
		body["check"] = "check"
	}
	if introspectable {
		body["introspect"] = true
	}
	if len(federate) > 0 {
		body["federate"] = true
		body["services"] = federate
	}
	payload, _ := json.Marshal(body)
	return payload
}

func runHeartbeat(ctx context.Context, hc *http.Client, upstream string, payload *atomic.Pointer[[]byte], interval time.Duration, meshSecret, registerToken []byte, refresh func() []byte, logger Logger, onForceIntrospect func()) {
	register := func() {
		// refresh (FederateAll mode) recomputes the payload from live
		// state each beat; a nil return means "nothing to advertise yet"
		// — skip this beat rather than POST an empty federation.
		if refresh != nil {
			fresh := refresh()
			if fresh == nil {
				return
			}
			payload.Store(&fresh)
		}
		body := *payload.Load()
		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			upstream+"/rpc/_register", bytes.NewReader(body))
		if err != nil {
			if logger != nil {
				logger.Warn("mesh.heartbeat: build register request failed", "upstream", upstream, "err", err)
			}
			return
		}
		req.Header.Set("Content-Type", "application/json")
		if len(meshSecret) > 0 {
			sig, ts := meshsecretproto.Sign(meshSecret, body, time.Now())
			req.Header.Set(meshsecretproto.RegisterSigHeader, sig)
			req.Header.Set(meshsecretproto.RegisterTsHeader, ts)
		}
		if len(registerToken) > 0 {
			req.Header.Set(registertokenproto.RegisterTokenHeader, string(registerToken))
		}
		resp, err := hc.Do(req)
		if err != nil {
			if logger != nil {
				logger.Warn("mesh.heartbeat: upstream unreachable", "upstream", upstream, "err", err)
			}
			return
		}
		defer resp.Body.Close()
		raw, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			if logger != nil {
				logger.Warn("mesh.heartbeat: upstream rejected register",
					"upstream", upstream, "status", resp.StatusCode, "body", string(raw))
			}
			return
		}
		if onForceIntrospect == nil {
			return
		}
		var env struct {
			Data RegisterResponse `json:"data"`
		}
		if json.Unmarshal(raw, &env) == nil && env.Data.ForceIntrospect {
			onForceIntrospect()
		}
	}
	register()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			register()
		}
	}
}
