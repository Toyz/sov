package catalog

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/Toyz/sov/gateway"
)

// EmitterSpec describes one `sov gen <lang>` subcommand for RunEmitter.
// It carries the bits that differ per language; RunEmitter owns the
// shared scaffolding (flag set, source resolution, fetch, write-or-
// stdout, the "wrote %s (%d bytes)" log).
type EmitterSpec struct {
	// Name is the command label used in flag-set name and stderr
	// messages, e.g. "sovgen ts".
	Name string
	// DefaultOut is the default value for --out (e.g. "./client.ts").
	DefaultOut string
	// OutHelp / PackageHelp override the default --out / --package help
	// text when set (lets each language describe its own file ext).
	OutHelp string
	// DefaultPackage is the default value for --package.
	DefaultPackage string
	// PackageHelp is the help text for --package.
	PackageHelp string
	// ExtraFlags optionally registers language-specific flags on the
	// flag set before parsing and returns a closure read AFTER parse
	// (used by gen/go's --with-http-caller). May be nil.
	ExtraFlags func(fs *flag.FlagSet) func()
	// PreEmit optionally runs after a successful fetch and before Emit
	// (used by gen/ts's drift-warning loop). May be nil.
	PreEmit func(report *gateway.IntrospectReport, stderr io.Writer)
	// Emit writes the generated client for report under pkg to w.
	Emit func(w io.Writer, pkg string, report *gateway.IntrospectReport)
}

// RunEmitter is the shared Run() body for the `sov gen <lang>`
// subcommands. It registers the standard flag set (--from, --exec,
// --exec-timeout, --out, --package, --header plus any ExtraFlags),
// resolves the source via ResolveSource, fetches the introspect
// report, runs PreEmit, then Emit, and either copies to stdout (when
// --out is "" or "-") or writes the file and logs "wrote %s (%d
// bytes)". Returns the process exit code (2 usage, 1 runtime, 0 ok).
func RunEmitter(spec EmitterSpec, argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet(spec.Name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "gateway base URL; CLI fetches {from}/rpc/_introspect. Mutually exclusive with --exec.")
	execBin := fs.String("exec", "", "path to a sov gateway binary; sovgen spawns it on a free local port, fetches introspect, then kills it. Binary must honor SOV_LISTEN.")
	execTimeout := fs.Duration("exec-timeout", 10*time.Second, "how long to wait for the spawned binary to answer /rpc/_introspect")
	out := fs.String("out", spec.DefaultOut, spec.OutHelp)
	pkg := fs.String("package", spec.DefaultPackage, spec.PackageHelp)
	var readExtra func()
	if spec.ExtraFlags != nil {
		readExtra = spec.ExtraFlags(fs)
	}
	var headers StringSliceFlag
	fs.Var(&headers, "header", "extra header on the introspect fetch, K=V; repeatable")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	if readExtra != nil {
		readExtra()
	}

	src, cleanup, err := ResolveSource(*from, *execBin, *execTimeout, stderr)
	if err != nil {
		if errors.Is(err, ErrSourceUsage) {
			fmt.Fprintf(stderr, "%s: %v\n", spec.Name, err)
			fs.Usage()
			return 2
		}
		fmt.Fprintf(stderr, "%s: spawn %s: %v\n", spec.Name, *execBin, err)
		return 1
	}
	defer cleanup()

	report, err := Fetch(src, headers)
	if err != nil {
		fmt.Fprintf(stderr, "%s: fetch %s: %v\n", spec.Name, src, err)
		return 1
	}

	if spec.PreEmit != nil {
		spec.PreEmit(report, stderr)
	}

	buf := &bytes.Buffer{}
	spec.Emit(buf, *pkg, report)

	if *out == "" || *out == "-" {
		_, _ = io.Copy(stdout, buf)
		return 0
	}
	if err := os.WriteFile(*out, buf.Bytes(), 0644); err != nil {
		fmt.Fprintf(stderr, "%s: write %s: %v\n", spec.Name, *out, err)
		return 1
	}
	fmt.Fprintf(stderr, "%s: wrote %s (%d bytes)\n", spec.Name, *out, buf.Len())
	return 0
}
