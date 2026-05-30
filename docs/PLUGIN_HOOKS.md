# Sov plugin hooks

A Sov plugin is **any value you pass to `gw.Use(p)`**. There is no base class and no
registration DSL — the gateway type-asserts your value against ~two dozen optional interfaces
and binds the ones it satisfies. Implement only the hooks you need; one struct can satisfy many.

```go
gw.Use(&MyPlugin{})
```

## The one footgun, and how to never hit it

Binding is duck-typed: `if h, ok := p.(ResponseInterceptor); ok { ... }`. If your method
signature is even slightly wrong, the assert silently fails and **your hook never binds — no
error**. Two guards, use both:

1. **Compile-time assertions (do this in every plugin).** Declare the hooks you intend, right
   after the type. A signature drift becomes a *build error*:

   ```go
   var (
       _ gateway.ConfigApplier       = (*MyPlugin)(nil)
       _ gateway.ResponseInterceptor = (*MyPlugin)(nil)
   )
   ```

   Every builtin plugin carries this block — copy the pattern. `sov init plugin` scaffolds it.

2. **Boot visibility.** At `ListenAndServe` the gateway logs, at **Debug**, exactly what each
   plugin bound:

   ```
   DEBUG gateway: plugin wired  plugin=my-plugin  hooks=[ConfigApplier ResponseInterceptor]
   ```

   If a hook you wrote isn't in that list, its signature didn't match. The same data is in
   `/rpc/_introspect` → `plugins[].hooks` and the explorer's Plugins tab.

`gw.Use` only *errors* when a value satisfies **zero** hooks (an empty plugin is a bug). The
partial case — two hooks, one mistyped — is exactly what the assertions above catch.

---

## Hook catalog

All interfaces live in package `github.com/Toyz/sov/gateway`. "Fires" = when the framework calls it.

### Identity & lifecycle metadata
| Interface | Method | Fires / purpose |
|---|---|---|
| `Plugin` | `PluginName() string` | Optional. Gives the plugin a human name in introspect + explorer. |
| `PluginDoc` | `Doc() string` | One-paragraph description surfaced in introspect `Extra["doc"]` + `sov inspect`. |

### Header cluster (independent — implement the subset you need)
| Interface | Method | Fires / purpose |
|---|---|---|
| `HeaderInjector` | `InjectHeaders(ctx, req, hreq *http.Request) error` | Before every **outbound** proxy hop (remote dispatch, federated probes). Add headers to `hreq`. |
| `HeaderParser` | `ParseHeaders(req) *rpc.Error` | Every **inbound** request, after the trust-strip. Read non-standard headers; return non-nil to short-circuit dispatch. |
| `HeaderClaimer` | `ClaimedHeaders() []string` | Declares inbound header names that bypass the `X-Sov-*` identity strip (e.g. `X-Sov-Register-Sig`). Identity headers can't be claimed. |
| `AuthTranslator` | `TranslateAuth(req, claims) error` | After auth resolves `Claims` (may be nil). Translate identity into legacy headers for brownfield downstreams. |

### Dispatch & response
| Interface | Method | Fires / purpose |
|---|---|---|
| `Middlewarer` | `Wrap(next Handler) Handler` | Wraps the dispatch chain (the `gw.Use`-visible form of a middleware closure). |
| `ContextContributor` | `ContributeContext(ctx *rpc.Context, req) error` | In `dispatchLocal`, before `engine.Dispatch`. Stash per-request metadata onto the in-process `*rpc.Context` (local-path counterpart of `HeaderInjector`). |
| `DispatchHook` | `OnDispatch(ev DispatchEvent) error` | **After** a handler returns. Sees router/method/status/duration/identity/mode. Runs on the dispatch goroutine — offload slow work. |
| `ResponseInterceptor` | `InterceptResponse(req, resp) error` | After dispatch, with the resolved `*Response`. Mutate/replace Status/Header/Body. Fires for ALL responses; `resp.Mode` tags the source. Registration order. |

### Boot & lifecycle
| Interface | Method | Fires / purpose |
|---|---|---|
| `ConfigApplier` | `Apply(g *Gateway) error` | Synchronously inside `gw.Use`, **before any other hook** — mutate gateway-owned config so it's live for every later request. Return error (HaltErr) to abort the Use. |
| `BootValidator` | `ValidateBoot(g *Gateway) error` | Once at `ListenAndServe`. Return error to refuse startup with a clear message. |
| `LifecycleHook` | `OnStart(ctx) / OnStop(ctx) error` | `OnStart` after boot validators pass; `OnStop` on context cancel. Background goroutines, pools, drains. |
| `PluginDependency` | `Requires() / After() []string` | `Requires` — `gw.Use` **errors** if a named plugin isn't registered (hard, fail-fast). `After` — soft ordering hint, recorded for introspect, not auto-applied. |

### Introspect & health
| Interface | Method | Fires / purpose |
|---|---|---|
| `IntrospectContributor` | `ContributeIntrospect(ctx, report, trace, visited) error` | After the local introspect report is built. Decorate it, or fan out to remote pods and merge (honor the `visited` cascade guard). |
| `HealthAggregator` | `AggregateHealth(ctx, report) error` | After the local `/rpc/_health` report is built, before marshal. Merge remote-pod probes; downgrade top-level Status only on a real degrade. |

### Mesh admission & trust
| Interface | Method | Fires / purpose |
|---|---|---|
| `MeshConflictPolicy` | `AllowMeshConflict(current, candidate, Conflict) bool` + `ConsumeConflict(name, Conflict)` | On `/rpc/_register` role-takeover or federation preemption. First policy returning true wins; default deny (409). `ConsumeConflict` cleans up after a successful takeover. |
| `UpstreamTrustPolicy` | `TrustUpstream(headers) bool` | Gates inbound `X-Sov-*` claims by upstream allowlist. ALL registered policies must return true (AND). |
| `SealVerifier` | `VerifySeal(headers) bool` | Gates inbound `X-Sov-*` claims by HMAC seal. First true wins. Pair with `UpstreamTrustPolicy` when neither is required. |

### Routing & capabilities
| Interface | Method | Fires / purpose |
|---|---|---|
| `RouteHandler` | `RoutePatterns() []string` + `ServeRoute(ctx, req) *Response` | Own a path (ServeMux conventions — trailing `/` = subtree). Checked after framework endpoints; can't shadow built-ins. |
| `CapabilityProvider` | `Capabilities() []Capability` | Publish a typed contract other plugins consume via `GetCapability[T]`. Convention: Type = `"<plugin>.<contract>"`. |
| `RecoveryHandler` | `HandleHookFailure(HookFailure) *Response` | On any hook error/panic. Log/alert/shape the 500. Return non-nil to override the default envelope. Framework installs a stderr default. |
| `Logger` | `Debug/Info/Warn/Error(msg, args...)` | Becomes the gateway-wide log sink (`gw.Log()`). slog-compatible; `*slog.Logger` satisfies it directly. First registered wins. |

### Advanced / framework-shape (rarely implemented by app plugins)
`Resolver` (custom service resolution), `Server` (custom transport). Implement these only when
replacing core machinery; see `gateway/resolver.go` and `gateway/server.go`.

---

## A minimal plugin

```go
package myplugin

import "github.com/Toyz/sov/gateway"

type Plugin struct{ cfg Config }

func New(cfg Config) *Plugin { return &Plugin{cfg: cfg} }

// Declare every hook this plugin intends — drift becomes a build error.
var (
	_ gateway.Plugin              = (*Plugin)(nil)
	_ gateway.PluginDoc           = (*Plugin)(nil)
	_ gateway.ResponseInterceptor = (*Plugin)(nil)
)

func (p *Plugin) PluginName() string { return "my-plugin" }
func (p *Plugin) Doc() string        { return "Does the thing on every response." }

func (p *Plugin) InterceptResponse(req *gateway.Request, resp *gateway.Response) error {
	resp.Header["X-Did-Thing"] = "1"
	return nil
}
```

Generate this skeleton (with your chosen hooks) via `sov init plugin <name> --hooks ...`.
