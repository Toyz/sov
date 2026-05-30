// Package all implements the `sov gen all` subcommand: fetch the
// gateway catalog once (or spawn the gateway binary once) and emit
// every supported language client into a single output directory.
//
// This is the one-shot DX path — `sov gen all -from <url>` lands TS,
// Go, Kotlin, Swift, and Python clients in ./gen/ with sensible
// per-lang filenames so downstreams can wire them straight into
// `tsc`, `go vet`, `kotlinc`, `swiftc`, `py_compile` without
// per-language plumbing.
//
// Implementation note: this package does NOT shell out to the
// per-lang subcommands. Each lang package exposes a public Emit
// function; `all` calls them directly so a single Fetch (or Spawn)
// services all five emitters.
package all

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/Toyz/sov/cmd/sov/gen/golang"
	"github.com/Toyz/sov/cmd/sov/gen/kotlin"
	"github.com/Toyz/sov/cmd/sov/gen/python"
	"github.com/Toyz/sov/cmd/sov/gen/swift"
	"github.com/Toyz/sov/cmd/sov/gen/ts"
	"github.com/Toyz/sov/cmd/sov/internal/catalog"
	"github.com/Toyz/sov/gateway"
)

// langSpec describes one emitter the all-subcommand drives.
type langSpec struct {
	name     string                                                     // log label
	filename string                                                     // file written under -out-dir
	pkg      string                                                     // per-lang package/namespace default
	emit     func(w io.Writer, pkg string, r *gateway.IntrospectReport) // shared closure shape
}

// Run executes `sov gen all`. Returns the exit code; never calls
// os.Exit so callers (and tests) can drive it.
func Run(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sov gen all", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "gateway base URL; CLI fetches {from}/rpc/_introspect. Mutually exclusive with --exec.")
	execBin := fs.String("exec", "", "path to a sov gateway binary; sovgen spawns it on a free local port, fetches introspect, then kills it. Binary must honor SOV_LISTEN.")
	execTimeout := fs.Duration("exec-timeout", 10*time.Second, "how long to wait for the spawned binary to answer /rpc/_introspect")
	outDir := fs.String("out-dir", "./gen", "output directory; one file per language is written here")
	var headers catalog.StringSliceFlag
	fs.Var(&headers, "header", "extra header on the introspect fetch, K=V; repeatable")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	src, cleanup, err := catalog.ResolveSource(*from, *execBin, *execTimeout, stderr)
	if err != nil {
		if errors.Is(err, catalog.ErrSourceUsage) {
			fmt.Fprintf(stderr, "sov gen all: %v\n", err)
			fs.Usage()
			return 2
		}
		fmt.Fprintf(stderr, "sov gen all: spawn %s: %v\n", *execBin, err)
		return 1
	}
	defer cleanup()

	report, err := catalog.Fetch(src, headers)
	if err != nil {
		fmt.Fprintf(stderr, "sov gen all: fetch %s: %v\n", *from, err)
		return 1
	}

	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fmt.Fprintf(stderr, "sov gen all: mkdir %s: %v\n", *outDir, err)
		return 1
	}

	// Per-lang specs. Go is emitted WITHOUT the optional HTTPCaller
	// bundle — keep `gen all` output minimal; consumers who want the
	// caller can re-run `sov gen go -with-http-caller`.
	specs := []langSpec{
		{name: "ts", filename: "client.ts", pkg: "sov", emit: ts.Emit},
		{name: "go", filename: "client.go", pkg: "sovclient",
			emit: func(w io.Writer, pkg string, r *gateway.IntrospectReport) {
				golang.Emit(w, pkg, r, false)
			}},
		{name: "kotlin", filename: "Client.kt", pkg: "com.example.sov", emit: kotlin.Emit},
		{name: "swift", filename: "Client.swift", pkg: "Sov", emit: swift.Emit},
		{name: "python", filename: "client.py", pkg: "sovclient", emit: python.Emit},
	}

	for _, s := range specs {
		buf := &bytes.Buffer{}
		s.emit(buf, s.pkg, report)
		path := filepath.Join(*outDir, s.filename)
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			fmt.Fprintf(stderr, "sov gen all: write %s: %v\n", path, err)
			return 1
		}
		fmt.Fprintf(stderr, "sov gen all: wrote %s (%d bytes)\n", path, buf.Len())
	}
	return 0
}
