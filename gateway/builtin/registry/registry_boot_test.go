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

func bootResult(t *testing.T, p *registry.Plugin, configure func(*gateway.Gateway)) ([]string, error) {
	t.Helper()
	gw := gateway.New()
	rec := &recLogger{}
	gw.MustUse(rec) // first Logger → gw.Log() returns it
	if configure != nil {
		configure(gw)
	}
	err := p.ValidateBoot(gw)
	return rec.warns, err
}

func TestRegistry_RefusesOpenBoot(t *testing.T) {
	// No gate at all → REFUSE to boot (not just a warning): open _register is an
	// SSRF + credential-forwarding + traffic-hijack vector.
	if _, err := bootResult(t, registry.New(registry.Config{}), nil); err == nil {
		t.Error("open _register (no gate) must refuse to boot, got nil error")
	}

	// AllowOpenRegister → boots, but still warns loudly.
	if w, err := bootResult(t, registry.New(registry.Config{AllowOpenRegister: true}), nil); err != nil {
		t.Errorf("AllowOpenRegister must boot, got: %v", err)
	} else if len(w) == 0 {
		t.Error("AllowOpenRegister should still warn that _register is open")
	}

	// A join-gate plugin lets it boot silently.
	for name, configure := range map[string]func(*gateway.Gateway){
		"registertoken": func(gw *gateway.Gateway) {
			gw.MustUse(registertoken.New(registertoken.Config{Token: []byte("t")}))
		},
		"meshsecret": func(gw *gateway.Gateway) {
			gw.MustUse(meshsecret.New(meshsecret.Config{Secret: []byte("s")}))
		},
	} {
		if w, err := bootResult(t, registry.New(registry.Config{}), configure); err != nil || len(w) != 0 {
			t.Errorf("%s gate: must boot silently, err=%v warns=%v", name, err, w)
		}
	}

	// AllowedNames allowlist → boots silently.
	if w, err := bootResult(t, registry.New(registry.Config{AllowedNames: []string{"Chirp"}}), nil); err != nil || len(w) != 0 {
		t.Errorf("AllowedNames gate: must boot silently, err=%v warns=%v", err, w)
	}
}
