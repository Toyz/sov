// Package catalogcmd implements `sov catalog` — snapshot the introspect
// catalog to a golden JSON file, and diff a live catalog against that
// baseline to catch BREAKING wire-contract changes in CI (a removed method,
// a removed field, a narrowed type). Complements `drift` (cross-service
// divergence in one live catalog) and `conform` (a live pod vs the contract):
// this guards backward compatibility over TIME, from a committed baseline.
package catalogcmd

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/Toyz/sov/cmd/sov/internal/catalog"
	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

// Run dispatches `sov catalog <snapshot|diff>`.
func Run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		usage(stderr)
		return 2
	}
	switch argv[0] {
	case "snapshot":
		return runSnapshot(argv[1:], stdout, stderr)
	case "diff":
		return runDiff(argv[1:], stdout, stderr)
	case "-h", "--help", "help":
		usage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "sov catalog: unknown subcommand %q\n", argv[0])
		usage(stderr)
		return 2
	}
}

func usage(w io.Writer) {
	fmt.Fprintln(w, `sov catalog — snapshot + backward-compat diff of the wire contract

Usage:
  sov catalog snapshot --from <url> [--out catalog.json]
  sov catalog diff     --from <url> --baseline catalog.json

snapshot writes the current introspect catalog as a golden file (commit it).
diff fetches the current catalog and fails (exit 1) on any BREAKING change
versus the baseline — a removed method, a removed field, or a changed field
type. Additions are reported as compatible.`)
}

func fetch(fs *flag.FlagSet, from, execBin *string, execTimeout *time.Duration, headers catalog.StringSliceFlag, stderr io.Writer) (*gateway.IntrospectReport, int) {
	src, cleanup, err := catalog.ResolveSource(*from, *execBin, *execTimeout, stderr)
	if err != nil {
		if err == catalog.ErrSourceUsage {
			fmt.Fprintf(stderr, "sov catalog: %v\n", err)
			fs.Usage()
			return nil, 2
		}
		fmt.Fprintf(stderr, "sov catalog: %v\n", err)
		return nil, 1
	}
	defer cleanup()
	report, err := catalog.Fetch(src, headers)
	if err != nil {
		fmt.Fprintf(stderr, "sov catalog: fetch: %v\n", err)
		return nil, 1
	}
	return report, 0
}

func runSnapshot(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sov catalog snapshot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "gateway base URL; fetches {from}/rpc/_introspect")
	execBin := fs.String("exec", "", "path to a sov gateway binary to spawn instead of --from")
	execTimeout := fs.Duration("exec-timeout", 10*time.Second, "spawn readiness timeout")
	out := fs.String("out", "catalog.json", `output file; "-" for stdout`)
	var headers catalog.StringSliceFlag
	fs.Var(&headers, "header", "extra header on the fetch, K=V; repeatable")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	report, code := fetch(fs, from, execBin, execTimeout, headers, stderr)
	if report == nil {
		return code
	}
	body, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		fmt.Fprintf(stderr, "sov catalog: marshal: %v\n", err)
		return 1
	}
	body = append(body, '\n')
	if *out == "-" {
		stdout.Write(body)
		return 0
	}
	if err := os.WriteFile(*out, body, 0o644); err != nil {
		fmt.Fprintf(stderr, "sov catalog: write %s: %v\n", *out, err)
		return 1
	}
	fmt.Fprintf(stderr, "sov catalog: wrote %s (%d bytes)\n", *out, len(body))
	return 0
}

func runDiff(argv []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sov catalog diff", flag.ContinueOnError)
	fs.SetOutput(stderr)
	from := fs.String("from", "", "gateway base URL; fetches {from}/rpc/_introspect")
	execBin := fs.String("exec", "", "path to a sov gateway binary to spawn instead of --from")
	execTimeout := fs.Duration("exec-timeout", 10*time.Second, "spawn readiness timeout")
	baseline := fs.String("baseline", "catalog.json", "committed baseline catalog to diff against")
	var headers catalog.StringSliceFlag
	fs.Var(&headers, "header", "extra header on the fetch, K=V; repeatable")
	if err := fs.Parse(argv); err != nil {
		return 2
	}
	raw, err := os.ReadFile(*baseline)
	if err != nil {
		fmt.Fprintf(stderr, "sov catalog: read baseline %s: %v\n", *baseline, err)
		return 1
	}
	var base gateway.IntrospectReport
	if err := json.Unmarshal(raw, &base); err != nil {
		fmt.Fprintf(stderr, "sov catalog: baseline is not a catalog: %v\n", err)
		return 1
	}
	cur, code := fetch(fs, from, execBin, execTimeout, headers, stderr)
	if cur == nil {
		return code
	}

	changes := compareReports(&base, cur)
	breaking := 0
	for _, c := range changes {
		tag := "  ok "
		if c.breaking {
			tag = "BREAK"
			breaking++
		}
		fmt.Fprintf(stdout, "%s %s\n", tag, c.msg)
	}
	if breaking > 0 {
		fmt.Fprintf(stderr, "sov catalog: %d breaking change(s) vs %s\n", breaking, *baseline)
		return 1
	}
	fmt.Fprintln(stdout, "compatible")
	return 0
}

type change struct {
	breaking bool
	msg      string
}

// compareReports reports how cur differs from base. A removed method, removed
// type, removed field, or changed field type is BREAKING (it breaks a client
// generated against base). Additions are compatible.
func compareReports(base, cur *gateway.IntrospectReport) []change {
	var out []change

	baseMethods, curMethods := methodSet(base), methodSet(cur)
	for _, k := range sortedKeys(baseMethods) {
		if !curMethods[k] {
			out = append(out, change{true, "removed method: " + k})
		}
	}
	for _, k := range sortedKeys(curMethods) {
		if !baseMethods[k] {
			out = append(out, change{false, "added method: " + k})
		}
	}

	for _, name := range sortedTypeNames(base.Types) {
		bt := base.Types[name]
		ct, ok := cur.Types[name]
		if !ok {
			out = append(out, change{true, "removed type: " + name})
			continue
		}
		curFields := fieldMap(ct)
		for _, bf := range bt.Fields {
			cf, ok := curFields[bf.JSONName]
			if !ok {
				out = append(out, change{true, "removed field: " + name + "." + bf.JSONName})
				continue
			}
			if cf.SchemaType != bf.SchemaType || cf.TypeName != bf.TypeName {
				out = append(out, change{true, fmt.Sprintf("changed field type: %s.%s (%s%s -> %s%s)",
					name, bf.JSONName, bf.SchemaType, bf.TypeName, cf.SchemaType, cf.TypeName)})
			}
		}
	}
	for _, name := range sortedTypeNames(cur.Types) {
		if _, ok := base.Types[name]; !ok {
			out = append(out, change{false, "added type: " + name})
		}
	}
	return out
}

func methodSet(r *gateway.IntrospectReport) map[string]bool {
	out := map[string]bool{}
	for _, routers := range r.Services {
		for _, rt := range routers {
			for _, m := range rt.Methods {
				out[rt.Router+"."+m.Method] = true
			}
		}
	}
	return out
}

func fieldMap(t gateway.TypeDescriptor) map[string]rpc.ParamField {
	out := make(map[string]rpc.ParamField, len(t.Fields))
	for _, f := range t.Fields {
		out[f.JSONName] = f
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func sortedTypeNames(m map[string]gateway.TypeDescriptor) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
