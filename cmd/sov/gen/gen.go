// Package gen is the `sov gen` subcommand router. It dispatches to a
// language-specific emitter (ts, go, kotlin, swift, python). Each
// emitter is its own subpackage owning its flag set and wire format;
// this router is the thin shim that maps argv[0] to a Run func.
//
// The router intentionally stays stdlib-only (no Cobra) to preserve
// the zero-dep invariant — a single static binary lands without
// third-party transitive imports.
package gen

import (
	"fmt"
	"io"

	"github.com/Toyz/sov/cmd/sov/gen/all"
	"github.com/Toyz/sov/cmd/sov/gen/golang"
	"github.com/Toyz/sov/cmd/sov/gen/kotlin"
	"github.com/Toyz/sov/cmd/sov/gen/python"
	"github.com/Toyz/sov/cmd/sov/gen/swift"
	"github.com/Toyz/sov/cmd/sov/gen/ts"
)

// Run executes `sov gen <lang> [flags]`. argv is everything after
// `sov gen` (i.e. starts with the lang name). Returns the exit code.
func Run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		usage(stderr)
		return 2
	}
	sub := argv[0]
	rest := argv[1:]
	switch sub {
	case "all":
		return all.Run(rest, stdout, stderr)
	case "ts":
		return ts.Run(rest, stdout, stderr)
	case "go":
		return golang.Run(rest, stdout, stderr)
	case "kotlin":
		return kotlin.Run(rest, stdout, stderr)
	case "swift":
		return swift.Run(rest, stdout, stderr)
	case "python", "py":
		return python.Run(rest, stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "sov gen: unknown language %q\n", sub)
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `sov gen — generate typed clients from a running sov gateway

Usage:
  sov gen <language> [flags]

Languages:
  all       Generate every supported client into one directory (recommended one-shot)
  ts        Generate a TypeScript client
  go        Generate a Go contract package
  kotlin    Generate a Kotlin client (OkHttp + kotlinx-serialization)
  swift     Generate a Swift client (URLSession + Codable)
  python    Generate a Python client (httpx + dataclasses)  [alias: py]

Run "sov gen <language> -h" for language-specific flags.`)
}
