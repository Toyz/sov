package gateway

import (
	"strings"
	"testing"
)

// dbgRec is a Logger plugin (also a ConfigApplier so it's a valid plugin)
// that records Debug calls, so we can assert the boot binding summary
// actually emits what each plugin bound.
type dbgRec struct{ debugs []string }

func (r *dbgRec) PluginName() string     { return "dbg-rec" }
func (r *dbgRec) Apply(_ *Gateway) error { return nil }
func (r *dbgRec) Debug(msg string, args ...any) {
	r.debugs = append(r.debugs, msg+" "+sprintArgs(args))
}
func (r *dbgRec) Info(string, ...any)  {}
func (r *dbgRec) Warn(string, ...any)  {}
func (r *dbgRec) Error(string, ...any) {}

func sprintArgs(args []any) string {
	var b strings.Builder
	for _, a := range args {
		b.WriteString(strings.TrimSpace(strings.Trim(stringify(a), "[]")))
		b.WriteByte(' ')
	}
	return b.String()
}

func stringify(a any) string {
	switch v := a.(type) {
	case string:
		return v
	case []string:
		return strings.Join(v, ",")
	default:
		return ""
	}
}

func TestLogPluginBindings_EmitsBoundHooks(t *testing.T) {
	g := New()
	rec := &dbgRec{}
	g.MustUse(rec) // becomes the logger AND a logged plugin (ConfigApplier)

	g.logPluginBindings()

	joined := strings.Join(rec.debugs, "\n")
	if !strings.Contains(joined, "gateway: plugin wired") {
		t.Fatalf("no plugin-wired debug line emitted:\n%s", joined)
	}
	if !strings.Contains(joined, "dbg-rec") {
		t.Errorf("binding log missing plugin name dbg-rec:\n%s", joined)
	}
	if !strings.Contains(joined, "ConfigApplier") {
		t.Errorf("binding log missing the bound hook ConfigApplier:\n%s", joined)
	}
}
