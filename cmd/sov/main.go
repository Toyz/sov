// sov — the sov gateway operator CLI.
//
// Subcommands:
//
//	sov init <mode>    Scaffold a sov project (monolith|hybrid|mesh|mesh tiered|secrets)
//	sov gen <lang>     Generate a typed client (ts|go|kotlin|swift|python)
//	sov drift          Check the gateway catalog for type-shape drift across services
//	sov inspect        Pretty-print /rpc/_introspect (services, types, plugins)
//	sov health         Pretty-print /rpc/_health (rollup, per-service status)
//	sov version        Print sov CLI version + build info
//	sov help           Show the top-level help
//
// The subcommand router is intentionally minimal (stdlib `flag` only —
// no Cobra dep) so a single static binary lands without third-party
// transitive imports. The drift subcommand subsumes what the legacy
// `gateway/builtin/drift` plugin used to do server-side; carrying a
// detector in the gateway is unnecessary when CI can run `sov drift`
// against every PR.
package main

import (
	"fmt"
	"io"
	"os"
	"runtime/debug"

	"github.com/Toyz/sov/cmd/sov/call"
	"github.com/Toyz/sov/cmd/sov/conform"
	"github.com/Toyz/sov/cmd/sov/drift"
	"github.com/Toyz/sov/cmd/sov/gen"
	"github.com/Toyz/sov/cmd/sov/health"
	initcmd "github.com/Toyz/sov/cmd/sov/init_cmd"
	"github.com/Toyz/sov/cmd/sov/inspect"
)

func main() {
	if len(os.Args) < 2 {
		usage(os.Stderr)
		os.Exit(2)
	}
	sub := os.Args[1]
	args := os.Args[2:]
	switch sub {
	case "init":
		os.Exit(initcmd.Run(args, os.Stdout, os.Stderr))
	case "gen":
		os.Exit(gen.Run(args, os.Stdout, os.Stderr))
	case "drift":
		os.Exit(drift.Run(args, os.Stdout, os.Stderr))
	case "inspect":
		os.Exit(inspect.Run(args, os.Stdout, os.Stderr))
	case "health":
		os.Exit(health.Run(args, os.Stdout, os.Stderr))
	case "call":
		os.Exit(call.Run(args, os.Stdout, os.Stderr))
	case "conform":
		os.Exit(conform.Run(args, os.Stdout, os.Stderr))
	case "version", "--version", "-v":
		printVersion(os.Stdout)
	case "-h", "--help", "help":
		usage(os.Stdout)
	default:
		fmt.Fprintf(os.Stderr, "sov: unknown subcommand %q\n", sub)
		usage(os.Stderr)
		os.Exit(2)
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `sov — sov gateway CLI

Usage:
  sov <command> [flags]

Commands:
  init <mode>    Scaffold a sov project (monolith|hybrid|mesh|mesh tiered|secrets)
  gen <lang>     Generate a typed client (ts|go|kotlin|swift|python|all)
  call           Invoke a method (Service.method) and print the response
  conform        Validate a pod against the sov wire contract (polyglot conformance)
  drift          Check the gateway catalog for type-shape drift across services
  inspect        Pretty-print /rpc/_introspect (services, types, plugins)
  health         Pretty-print /rpc/_health (rollup, per-service status)
  version        Print sov CLI version + build info
  help           Show this message

Run "sov <command> -h" for command-specific flags.`)
}

// printVersion reports the module version and VCS revision when the
// binary was built with module info (the common case for `go install`
// or `go build` on a tagged repo). Falls back to "(devel)" when no
// build metadata is available — that's the standard `go run` shape.
func printVersion(w io.Writer) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Fprintln(w, "sov (devel)")
		return
	}
	rev := ""
	mod := info.Main.Version
	for _, s := range info.Settings {
		if s.Key == "vcs.revision" {
			rev = s.Value
			break
		}
	}
	if mod == "" || mod == "(devel)" {
		mod = "(devel)"
	}
	fmt.Fprintf(w, "sov %s\n", mod)
	fmt.Fprintf(w, "module: %s\n", info.Main.Path)
	if rev != "" {
		fmt.Fprintf(w, "revision: %s\n", rev)
	}
	fmt.Fprintf(w, "go: %s\n", info.GoVersion)
}
