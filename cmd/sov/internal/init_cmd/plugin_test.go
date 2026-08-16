package initcmd

import (
	"go/parser"
	"go/token"
	"strings"
	"testing"
)

func TestRenderPlugin_ValidGoWithAsserts(t *testing.T) {
	hooks := []string{"ConfigApplier", "ResponseInterceptor", "HeaderParser"}
	src := renderPlugin("tenant-guard", "tenantguard", hooks)

	// Must be valid, parseable Go.
	if _, err := parser.ParseFile(token.NewFileSet(), "p.go", src, parser.AllErrors); err != nil {
		t.Fatalf("generated source does not parse: %v\n%s", err, src)
	}

	// Plugin + PluginDoc always asserted, plus each requested hook. Match
	// loosely on "gateway.<iface> = (*Plugin)(nil)" — gofmt aligns the `=`
	// column, so exact inner spacing varies.
	assertFor := func(iface string) bool {
		for _, line := range strings.Split(src, "\n") {
			f := strings.Fields(line) // ["_", "gateway.X", "=", "(*Plugin)(nil)"]
			if len(f) == 4 && f[0] == "_" && f[1] == "gateway."+iface && f[2] == "=" && f[3] == "(*Plugin)(nil)" {
				return true
			}
		}
		return false
	}
	for _, iface := range []string{"Plugin", "PluginDoc", "ConfigApplier", "ResponseInterceptor", "HeaderParser"} {
		if !assertFor(iface) {
			t.Errorf("generated source missing compile-assert for gateway.%s\n---\n%s", iface, src)
		}
	}
	for _, w := range []string{
		`func (p *Plugin) PluginName() string { return "tenant-guard" }`,
		`"github.com/Toyz/sov/rpc"`, // HeaderParser pulls rpc
		"package tenantguard",
	} {
		if !strings.Contains(src, w) {
			t.Errorf("generated source missing %q\n---\n%s", w, src)
		}
	}
}

func TestResolveHooks_Errors(t *testing.T) {
	if _, err := resolveHooks("Bogus"); err == nil {
		t.Error("expected error for unknown hook")
	}
	if _, err := resolveHooks(""); err == nil {
		t.Error("expected error for empty hooks (a plugin with no hooks won't bind)")
	}
	got, err := resolveHooks("ResponseInterceptor, ConfigApplier ,ConfigApplier")
	if err != nil {
		t.Fatal(err)
	}
	// Deduped + sorted.
	if len(got) != 2 || got[0] != "ConfigApplier" || got[1] != "ResponseInterceptor" {
		t.Errorf("resolveHooks = %v, want [ConfigApplier ResponseInterceptor]", got)
	}
}
