package rpc

import (
	"fmt"
	"sort"
)

// Describe returns one RouterDescriptor per registered router, in
// registration order. Each descriptor includes method signatures rendered
// as TypeScript-shaped previews and JSON-tagged params expanded to
// ParamField records.
//
// The gateway's /rpc/_introspect endpoint stitches this output across
// every service in the resolver chain into one org-wide catalog.
func (e *Engine) Describe() []RouterDescriptor {
	e.mu.RLock()
	defer e.mu.RUnlock()

	out := make([]RouterDescriptor, 0, len(e.routerOrder))
	for _, routerName := range e.routerOrder {
		methods := e.routers[routerName]
		names := make([]string, 0, len(methods))
		for w := range methods {
			names = append(names, w)
		}
		sort.Strings(names)

		soft := sliceSet(e.hiddenList[routerName])
		hard := sliceSet(e.hardHiddenList[routerName])

		mds := make([]MethodDescriptor, 0, len(names))
		for _, w := range names {
			ent := methods[w]
			md := MethodDescriptor{
				Method:    ent.wireName,
				Title:     OperationTitle(ent.goName),
				PostPath:  fmt.Sprintf("/rpc/%s/%s", routerName, ent.wireName),
				HasParams: ent.hasParams,
			}
			// Visibility: marker-method (router-wide) OR sov sentinel
			// (per-method) declarations both feed the flags; hard wins.
			if hard[ent.wireName] || ent.internalHard {
				md.HardHidden = true
			} else if soft[ent.wireName] || ent.internal {
				md.Internal = true
			}
			if ent.hasParams {
				md.Params = describeFieldMap(ent.fieldMap)
				if ent.paramType != nil {
					nested := map[string][]ParamField{}
					// Walk children of the params struct, NOT the
					// params struct itself — its fields already live in
					// md.Params under the generated "{Router}.{Method}Params"
					// name. We only want types referenced BY params (e.g.
					// AuthzCheckParams.Claims → Claims gets a TypeDescriptor).
					for i := 0; i < ent.paramType.NumField(); i++ {
						collectNestedTypes(ent.paramType.Field(i).Type, nested)
					}
					if len(nested) > 0 {
						md.NestedTypes = nested
					}
				}
			}
			md.RequestTypeScript, md.ResponseTypeScript = TSPreviewForMethod(ent)
			// Reflect the response type into the catalog too, so the
			// type ownership convention (owner = the service that RETURNS
			// a type) has a signal. Named struct results (and their
			// nested types) land in NestedTypes; ResponseTypeName marks
			// which entry is the top-level response so the catalog can
			// tag it role="response". Primitive/map/slice-of-primitive
			// results have no named type and contribute nothing.
			if ent.resultType != nil {
				if md.NestedTypes == nil {
					md.NestedTypes = map[string][]ParamField{}
				}
				collectNestedTypes(ent.resultType, md.NestedTypes)
				md.ResponseTypeName = nestedTypeName(ent.resultType)
				if len(md.NestedTypes) == 0 {
					md.NestedTypes = nil
				}
			}
			mds = append(mds, md)
		}

		out = append(out, RouterDescriptor{
			Router:  routerName,
			Title:   RouterTitle(routerName),
			Methods: mds,
		})
	}
	return out
}

// sliceSet turns a wire-name list into a lookup set.
func sliceSet(names []string) map[string]bool {
	if len(names) == 0 {
		return nil
	}
	s := make(map[string]bool, len(names))
	for _, n := range names {
		s[n] = true
	}
	return s
}
