package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/Toyz/sov/rpc"
)

// TypeDescriptor is the catalog entry for one Go type. Fields is the
// full ParamField list (including the new Position/Omitempty/Deprecated
// markers). UsedBy lists every appearance in the org's RPC surface.
type TypeDescriptor struct {
	Name      string           `json:"name"`
	Fields    []rpc.ParamField `json:"fields"`
	UsedBy    []TypeUse        `json:"used_by"`
	ShapeHash string           `json:"shape_hash"`
	// Owner is the service that PRODUCES this type — the one that returns
	// it (a "response"-role usage). Empty for request/nested-only types
	// (no producer) or when ownership is ambiguous, i.e. >1 producer (see
	// Owners + IntrospectReport.BoundaryWarnings). Data-boundary metadata,
	// inferred by convention; see inferOwnership.
	Owner string `json:"owner,omitempty"`
	// Owners is the full set of producing services (every service that
	// returns this type). One entry is the normal case (Owner mirrors it);
	// two or more is a data-boundary smell — overlapping producers — which
	// also raises a BoundaryWarning. Sorted, deduped. Surfaced so the
	// ambiguous case shows WHO the producers are, not just that it's
	// ambiguous.
	Owners []string `json:"owners,omitempty"`
	// Consumers are services that use this type without owning it (as a
	// request param or a nested reference). Sorted, deduped.
	Consumers []string `json:"consumers,omitempty"`
}

// TypeUse records one appearance of a TypeDescriptor on a method.
type TypeUse struct {
	Service string `json:"service"`
	Method  string `json:"method"`
	Role    string `json:"role"` // "request" | "response"
}

// TypeVariants groups same-named types that diverged in shape across
// services — drift detection. variants[0] is the canonical (sorted by
// ShapeHash), the rest are the divergences.
type TypeVariants struct {
	Name     string        `json:"name"`
	Variants []TypeVariant `json:"variants"`
}

// TypeVariant is one shape of a same-named type.
type TypeVariant struct {
	ShapeHash string           `json:"shape_hash"`
	Fields    []rpc.ParamField `json:"fields"`
	Services  []string         `json:"services"`
}

// BuildTypeCatalog walks every service's descriptors, extracts each
// param + response type, builds the flat Types map, and detects drift
// across same-named types into CrossRefs. Exported so plugin
// aggregators can rebuild the catalog after merging remote
// descriptors into report.Services.
func BuildTypeCatalog(report *IntrospectReport) { buildTypeCatalog(report) }

func buildTypeCatalog(report *IntrospectReport) {
	// Service names are processed in sorted order for deterministic output.
	serviceNames := make([]string, 0, len(report.Services))
	for name := range report.Services {
		serviceNames = append(serviceNames, name)
	}
	sort.Strings(serviceNames)

	// shapeBuckets keys a type name to (shape hash → variant data).
	// Same name appearing with the same hash twice = no drift, just
	// extra UsedBy entries on the canonical TypeDescriptor.
	shapeBuckets := map[string]map[string]*TypeVariant{}

	addType := func(serviceName, methodName, role string, typeName string, fields []rpc.ParamField) {
		if typeName == "" {
			return
		}
		hash := hashShape(fields)
		bucket, ok := shapeBuckets[typeName]
		if !ok {
			bucket = map[string]*TypeVariant{}
			shapeBuckets[typeName] = bucket
		}
		variant, ok := bucket[hash]
		if !ok {
			variant = &TypeVariant{
				ShapeHash: hash,
				Fields:    fields,
				Services:  []string{},
			}
			bucket[hash] = variant
		}
		// Service list stays deduped + sorted.
		if !slices.Contains(variant.Services, serviceName) {
			variant.Services = append(variant.Services, serviceName)
			sort.Strings(variant.Services)
		}

		// Update or insert in Types map.
		td, exists := report.Types[typeName]
		if !exists {
			td = TypeDescriptor{Name: typeName, Fields: fields, ShapeHash: hash}
		}
		td.UsedBy = append(td.UsedBy, TypeUse{Service: serviceName, Method: methodName, Role: role})
		report.Types[typeName] = td
	}

	for _, svcName := range serviceNames {
		for _, rd := range report.Services[svcName] {
			for _, md := range rd.Methods {
				if md.HasParams && len(md.Params) > 0 {
					// Use the params' first nested type-name as the type key
					// if every field is flat. Otherwise emit the params under
					// the generated key "{router}.{method}Params".
					typeName := fmt.Sprintf("%s.%sParams", rd.Router, capitalize(md.Method))
					addType(svcName, md.Method, "request", typeName, md.Params)
				}
				// Nested types referenced by Params (e.g. Authz.check's
				// *rpc.Claims) plus the reflected response type and its
				// children all live in md.NestedTypes. The top-level
				// response type (md.ResponseTypeName) is tagged role
				// "response" — that's the producer signal the ownership
				// convention keys on; everything else is "nested".
				for nestedName, nestedFields := range md.NestedTypes {
					role := "nested"
					if nestedName != "" && nestedName == md.ResponseTypeName {
						role = "response"
					}
					addType(svcName, md.Method, role, nestedName, nestedFields)
				}
			}
		}
	}

	// CrossRefs: emit only type names with >1 variant (true drift).
	for name, bucket := range shapeBuckets {
		if len(bucket) <= 1 {
			continue
		}
		variants := make([]TypeVariant, 0, len(bucket))
		for _, v := range bucket {
			sort.Strings(v.Services)
			variants = append(variants, *v)
		}
		sort.Slice(variants, func(i, j int) bool { return variants[i].ShapeHash < variants[j].ShapeHash })
		report.CrossRefs[name] = TypeVariants{Name: name, Variants: variants}
	}

	// Sort each TypeDescriptor.UsedBy for deterministic output.
	for name, td := range report.Types {
		sort.Slice(td.UsedBy, func(i, j int) bool {
			if td.UsedBy[i].Service != td.UsedBy[j].Service {
				return td.UsedBy[i].Service < td.UsedBy[j].Service
			}
			if td.UsedBy[i].Method != td.UsedBy[j].Method {
				return td.UsedBy[i].Method < td.UsedBy[j].Method
			}
			return td.UsedBy[i].Role < td.UsedBy[j].Role
		})
		report.Types[name] = td
	}

	inferOwnership(report)
}

// inferOwnership derives data-boundary metadata from each type's UsedBy
// records, by convention: the OWNER of a type is the service that RETURNS
// it (a "response"-role usage). Services that only take it as input or
// reference it nested are CONSUMERS. Request-only types (params) have no
// producer and stay unowned.
//
// When more than one service returns the same type name, ownership is
// ambiguous — that's a boundary smell (two services producing the same
// entity shape), surfaced as a BoundaryWarning rather than a hard error.
func inferOwnership(report *IntrospectReport) {
	report.BoundaryWarnings = nil
	for name, td := range report.Types {
		producers := map[string]struct{}{}
		consumers := map[string]struct{}{}
		for _, u := range td.UsedBy {
			if u.Role == "response" {
				producers[u.Service] = struct{}{}
			} else {
				consumers[u.Service] = struct{}{}
			}
		}
		owners := make([]string, 0, len(producers))
		for s := range producers {
			owners = append(owners, s)
		}
		sort.Strings(owners)
		td.Owners = owners
		switch len(owners) {
		case 0:
			td.Owner = "" // request/nested-only type — no producer by convention
		case 1:
			td.Owner = owners[0]
		default:
			td.Owner = "" // ambiguous single-owner; the full set lives in td.Owners
			report.BoundaryWarnings = append(report.BoundaryWarnings,
				fmt.Sprintf("type %q is returned by %d services (%s) — data ownership is ambiguous",
					name, len(owners), strings.Join(owners, ", ")))
		}
		// Consumers = users that don't PRODUCE the type (excludes every
		// producer, not just the single Owner — so an ambiguous type's
		// producers don't leak into its consumer list).
		cons := make([]string, 0, len(consumers))
		for s := range consumers {
			if _, isProducer := producers[s]; !isProducer {
				cons = append(cons, s)
			}
		}
		sort.Strings(cons)
		td.Consumers = cons
		report.Types[name] = td
	}
	sort.Strings(report.BoundaryWarnings)
}

// hashShape returns a deterministic hash of a field list. Sorts by
// JSONName so reorderings (Go-source-only) don't show as drift.
func hashShape(fields []rpc.ParamField) string {
	sorted := make([]rpc.ParamField, len(fields))
	copy(sorted, fields)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].JSONName < sorted[j].JSONName })
	h := sha256.New()
	for _, f := range sorted {
		fmt.Fprintf(h, "%s|%s|%t|%t\n", f.JSONName, f.SchemaType, f.Required, f.Omitempty)
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] -= 'a' - 'A'
	}
	return string(r)
}
