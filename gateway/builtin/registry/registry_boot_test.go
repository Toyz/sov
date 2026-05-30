package registry_test

import (
	"testing"

	"github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/gateway/builtin/meshsecret"
	"github.com/Toyz/sov/gateway/builtin/registertoken"
	"github.com/Toyz/sov/gateway/builtin/registry"
)

// recLogger is a Logger plugin that records Warn messages so the test can
// assert the open-_register boot warning fired (or didn't).
type recLogger struct{ warns []string }

func (r *recLogger) PluginName() string             { return "rec-logger" }
func (r *recLogger) Apply(_ *gateway.Gateway) error { return nil } // ConfigApplier → valid plugin
func (r *recLogger) Debug(string, ...any)           {}
func (r *recLogger) Info(string, ...any)            {}
func (r *recLogger) Warn(msg string, _ ...any)      { r.warns = append(r.warns, msg) }
func (r *recLogger) Error(string, ...any)           {}

func bootWarns(t *testing.T, p *registry.Plugin, configure func(*gateway.Gateway)) []string {
	t.Helper()
	gw := gateway.New()
	rec := &recLogger{}
	gw.MustUse(rec) // first Logger → gw.Log() returns it
	if configure != nil {
		configure(gw)
	}
	if err := p.ValidateBoot(gw); err != nil {
		t.Fatalf("ValidateBoot: %v", err)
	}
	return rec.warns
}

func TestRegistry_BootWarnsWhenRegisterOpen(t *testing.T) {
	// No gate at all → must warn.
	if w := bootWarns(t, registry.New(registry.Config{}), nil); len(w) == 0 {
		t.Error("open _register (no gate) produced no boot warning — should yell")
	}

	// registertoken sibling present → silent.
	if w := bootWarns(t, registry.New(registry.Config{}), func(gw *gateway.Gateway) {
		gw.MustUse(registertoken.New(registertoken.Config{Token: []byte("t")}))
	}); len(w) != 0 {
		t.Errorf("registertoken gate present but warned: %v", w)
	}

	// meshsecret sibling present → silent.
	if w := bootWarns(t, registry.New(registry.Config{}), func(gw *gateway.Gateway) {
		gw.MustUse(meshsecret.New(meshsecret.Config{Secret: []byte("s")}))
	}); len(w) != 0 {
		t.Errorf("meshsecret gate present but warned: %v", w)
	}

	// AllowedNames allowlist → silent.
	if w := bootWarns(t, registry.New(registry.Config{AllowedNames: []string{"Chirp"}}), nil); len(w) != 0 {
		t.Errorf("AllowedNames gate present but warned: %v", w)
	}
}
