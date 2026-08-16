// Package meshsecret ships the HMAC gate for /rpc/_register. Pods
// joining the mesh sign their register POST with the same key the
// registry was constructed with; mismatches, stale timestamps (outside
// the ±5 min skew window), and exact replays get 401.
//
//	gw.Use(meshsecret.New(meshsecret.Config{Secret: secret}))
package meshsecret

import (
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/meshsecret/proto"
	"github.com/Toyz/sov/rpc"
)

// Config configures the meshsecret plugin. Secret is the HMAC key
// shared between the registry and every joining pod. Empty secret
// disables the gate.
type Config struct {
	Secret []byte
}

// Plugin is the HMAC-gate plugin returned by New.
type Plugin struct {
	secret []byte

	// seen guards against exact replays inside the skew window. The HMAC
	// signature is unique per (body, ts), so a byte-identical resend carries
	// the same sig; we reject a sig already recorded. Entries map sig -> unix
	// expiry (ts + SkewWindow, the last instant Verify would still accept that
	// ts) and are swept lazily, bounding memory to the live window. Only a
	// secret-holder can produce a sig that reaches this map (Verify runs
	// first), so it is not an unauthenticated growth vector.
	mu   sync.Mutex
	seen map[string]int64
}

// New returns the meshsecret plugin from cfg.
func New(cfgs ...Config) *Plugin {
	if len(cfgs) > 1 {
		panic("meshsecret.New: at most one Config")
	}
	var cfg Config
	if len(cfgs) == 1 {
		cfg = cfgs[0]
	}
	return &Plugin{secret: cfg.Secret, seen: map[string]int64{}}
}

// Compile-time proof of the hooks this plugin binds — a signature
// drift here is a build error, not a silent non-binding at runtime.
var (
	_ gateway.Plugin        = (*Plugin)(nil)
	_ gateway.PluginDoc     = (*Plugin)(nil)
	_ gateway.HeaderClaimer = (*Plugin)(nil)
	_ gateway.HeaderParser  = (*Plugin)(nil)
)

// PluginName surfaces in /rpc/_introspect.plugins[].
func (p *Plugin) PluginName() string { return "mesh-secret" }

// Doc surfaces a one-line description in /rpc/_introspect + the explorer.
func (p *Plugin) Doc() string {
	return "Gates /rpc/_register with an HMAC signature so only secret-holding pods can join the mesh."
}

// ClaimedHeaders declares the X-Sov-Register-* signature pair so the
// edge-strip preserves them (otherwise the X-Sov- prefix strip nukes
// them before ParseHeaders fires).
func (p *Plugin) ClaimedHeaders() []string {
	return []string{proto.RegisterSigHeader, proto.RegisterTsHeader}
}

// ParseHeaders intercepts /rpc/_register and verifies the
// X-Sov-Register-Sig header against the request body, then rejects an
// exact replay of an already-seen signature. Other paths pass through
// untouched.
func (p *Plugin) ParseHeaders(req *gateway.Request) *rpc.Error {
	if req.Path != "/rpc/_register" {
		return nil
	}
	if len(p.secret) == 0 {
		return nil
	}
	sig := req.Header.Get(proto.RegisterSigHeader)
	ts := req.Header.Get(proto.RegisterTsHeader)
	now := time.Now()
	if err := proto.Verify(p.secret, sig, ts, req.Body, now); err != nil {
		return rpc.Unauthorized("_register: %v", err)
	}
	if err := p.markSeen(sig, ts, now); err != nil {
		return rpc.Unauthorized("_register: %v", err)
	}
	return nil
}

// markSeen records sig as consumed and rejects a duplicate. ts is the
// already-Verify-validated timestamp string; the sig is retained until
// ts + SkewWindow, after which Verify's window check rejects that ts on
// its own and the entry is safe to evict.
func (p *Plugin) markSeen(sig, ts string, now time.Time) error {
	tsInt, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return errors.New("invalid register timestamp")
	}
	expiry := tsInt + int64(proto.SkewWindow/time.Second)
	nowUnix := now.UTC().Unix()

	p.mu.Lock()
	defer p.mu.Unlock()
	// Sweep expired sigs first — bounds the map to signatures still inside
	// their replayable window.
	for s, exp := range p.seen {
		if exp <= nowUnix {
			delete(p.seen, s)
		}
	}
	if _, dup := p.seen[sig]; dup {
		return errors.New("duplicate register signature (replay)")
	}
	p.seen[sig] = expiry
	return nil
}
