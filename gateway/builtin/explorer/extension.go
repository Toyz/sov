package explorer

import "github.com/Toyz/sov/gateway"

// Extender lets a plugin extend the explorer dashboard with its OWN client-side
// assets — ES modules ("js") and stylesheets ("css"). A plugin implements this
// interface (importing the explorer package it extends), and the explorer finds
// every implementer via gateway.PluginsImplementing[Extender]. The core gateway
// knows nothing about this interface — the explorer owns it, and a plugin owns
// which interfaces it satisfies:
//
//	//go:embed web/codegen.js
//	var codegenJS []byte
//
//	func (p *Plugin) ExplorerAssets() []explorer.Asset {
//	    return []explorer.Asset{{Kind: "js", Name: "codegen", Body: codegenJS}}
//	}
//
// The module's default export receives the sovx SDK (window.sovx) and registers
// against it — actions on methods/types, request hooks, settings, panels — all
// styled with the dashboard's own theme tokens so extensions look native. See
// docs/EXPLORER_EXTENSIONS.md.
type Extender interface {
	ExplorerAssets() []Asset
}

// Asset is one client-side asset: an ES module (Kind "js") or a stylesheet
// (Kind "css"). Provide the content inline via Body (the explorer serves it at
// {prefix}/ext/{Name}.{Kind}, so the plugin needs no route of its own) OR via a
// URL the plugin serves itself. Name is the dedup key (with Kind) and the served
// filename (sanitized). Load order follows plugin registration order.
type Asset struct {
	Kind string // "js" (ES module) or "css"
	Name string // identifier, e.g. "codegen"
	Body []byte // raw bytes; explorer-served when set
	URL  string // alternatively, a path the plugin serves itself
}

// collectAssets gathers and dedups (by Kind+Name+URL) the assets of every
// registered Extender. Discovery is generic — the core never references Extender.
func (p *Plugin) collectAssets() []Asset {
	var out []Asset
	seen := map[string]bool{}
	for _, ext := range gateway.PluginsImplementing[Extender](p.gw) {
		for _, a := range ext.ExplorerAssets() {
			if a.Kind == "" || (len(a.Body) == 0 && a.URL == "") {
				continue
			}
			key := a.Kind + "\x00" + a.Name + "\x00" + a.URL
			if seen[key] {
				continue
			}
			seen[key] = true
			out = append(out, a)
		}
	}
	return out
}
