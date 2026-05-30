# static — static file server plugin

Serves a static file tree (a built SPA, a docs site, an asset bundle) from the gateway, so a sov
binary can host its own frontend with no sidecar nginx/Caddy. One process serves both the API
(`/rpc/...`) and the web app (`/`).

## Use

Single-binary deploy (embed the build):

```go
//go:embed all:dist
var dist embed.FS

func main() {
	gw := sov.New()
	sub, _ := fs.Sub(dist, "dist")          // strip the embed prefix → index.html at root
	gw.Use(static.New(static.Config{
		FS:          sub,
		SPAFallback: true,                    // deep links / client-side routing → index.html
	}))
	gw.Run(ctx, ":8080")
}
```

Dev / bind-mounted build (serve a directory):

```go
gw.Use(static.New(static.Config{Dir: "./frontend/dist", SPAFallback: true}))
```

## Config

| Field | Default | Meaning |
|---|---|---|
| `FS` | — | `fs.FS` to serve (e.g. an `embed.FS` sub-tree). Wins over `Dir`. |
| `Dir` | — | Filesystem dir to serve when `FS` is nil. One of `FS`/`Dir` is required (else `New` panics). |
| `PathPrefix` | `/` | Mount point. Leading slash added if missing. |
| `SPAFallback` | `false` | Serve `IndexFile` (HTTP 200) for unresolved paths — required for SPA routing. Off → 404. |
| `IndexFile` | `index.html` | Served for the root and directories; the `SPAFallback` target. |

## Behavior

- Implements `gateway.RouteHandler` — claims the `PathPrefix` subtree.
- **Does not shadow the API.** Framework endpoints and `/rpc/...` are matched before plugin routes
  and win by longest-prefix, so a `/` mount never captures `/rpc/_introspect` etc. Pinned by the
  `TestRPCNotShadowed` case.
- Only `GET`/`HEAD` are served (else `405`).
- Path traversal (`..`) is collapsed to the tree root — requests cannot escape the served tree.
- `Content-Type` is derived from the file extension, sniffed when unknown.
