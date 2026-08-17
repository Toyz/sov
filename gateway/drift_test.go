package gateway_test

import (
	"testing"

	. "github.com/Toyz/sov/gateway"
	"github.com/Toyz/sov/rpc"
)

func pf(name, ty string) rpc.ParamField { return rpc.ParamField{JSONName: name, SchemaType: ty} }

func craftedReport(aFields, bFields []rpc.ParamField) *IntrospectReport {
	return &IntrospectReport{
		Services: map[string][]rpc.RouterDescriptor{
			"A": {{Router: "A", Methods: []rpc.MethodDescriptor{{
				Method: "get", ResponseTypeName: "Addr",
				NestedTypes: map[string][]rpc.ParamField{"Addr": aFields},
			}}}},
			"B": {{Router: "B", Methods: []rpc.MethodDescriptor{{
				Method: "track", ResponseTypeName: "Addr",
				NestedTypes: map[string][]rpc.ParamField{"Addr": bFields},
			}}}},
		},
		Types:     map[string]TypeDescriptor{},
		CrossRefs: map[string]TypeVariants{},
	}
}

// Divergent shapes for the same type name DRIFT, and the diff spells out which
// fields differ (that is the "better visibility" — not just "shapes differ").
func TestDrift_DivergentShapeProducesFieldDiff(t *testing.T) {
	rpt := craftedReport(
		[]rpc.ParamField{pf("street", "string"), pf("zip", "string")},
		[]rpc.ParamField{pf("street", "string"), pf("country", "string")},
	)
	BuildTypeCatalog(rpt)

	tv, ok := rpt.CrossRefs["Addr"]
	if !ok {
		t.Fatalf("Addr should drift; cross_refs=%v", rpt.CrossRefs)
	}
	if len(tv.Variants) != 2 {
		t.Fatalf("want 2 variants, got %d", len(tv.Variants))
	}
	byField := map[string]FieldDivergence{}
	for _, d := range tv.Diff {
		byField[d.Field] = d
	}
	// street is in BOTH with the same type → common (not diverging).
	if d, ok := byField["street"]; !ok || d.Diverges {
		t.Errorf("street should be common across variants, got %+v", d)
	}
	// zip and country each appear in only one variant → diverging.
	if d := byField["zip"]; !d.Diverges {
		t.Errorf("zip should diverge (absent in one variant): %+v", d)
	}
	if d := byField["country"]; !d.Diverges {
		t.Errorf("country should diverge: %+v", d)
	}
	// A drifted type is NOT marked Shared.
	if rpt.Types["Addr"].Shared {
		t.Errorf("a drifted type must not be Shared")
	}
	if len(rpt.BoundaryWarnings) != 0 {
		t.Errorf("drift is signalled via CrossRefs, not BoundaryWarnings: %v", rpt.BoundaryWarnings)
	}
}

// Identical shapes for the same name from two producers = a shared model: no
// drift, marked Shared, no warning.
func TestDrift_IdenticalShapeIsSharedNotDrift(t *testing.T) {
	same := []rpc.ParamField{pf("amount", "number"), pf("currency", "string")}
	rpt := craftedReport(same, same)
	BuildTypeCatalog(rpt)

	if _, drifted := rpt.CrossRefs["Addr"]; drifted {
		t.Errorf("identical shapes must not drift")
	}
	if !rpt.Types["Addr"].Shared {
		t.Errorf("identical-shape multi-owner should be Shared")
	}
	if len(rpt.BoundaryWarnings) != 0 {
		t.Errorf("shared type must not warn: %v", rpt.BoundaryWarnings)
	}
}
