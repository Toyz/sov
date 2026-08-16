package main

import (
	_ "embed"

	"github.com/Toyz/sov/gateway/builtin/explorer"
)

//go:embed web/codegen.js
var codegenJS []byte

// codegenExt is a plugin that extends the explorer with a real ES module —
// "Python"/"C#" codegen actions on any type plus a bearer-token auth hook. It
// implements explorer.Extender (an interface the explorer owns); the core
// gateway never knows this interface exists. Registering it is all it takes:
//
//	gw.Use(codegenExt{})
type codegenExt struct{}

func (codegenExt) PluginName() string { return "codegen-ext" }

func (codegenExt) ExplorerAssets() []explorer.Asset {
	return []explorer.Asset{{Kind: "js", Name: "codegen", Body: codegenJS}}
}
