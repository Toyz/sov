package rpc

import (
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// FieldMap is the boot-time-resolved layout of one params (or result)
// struct. It decouples wire shape from Go source shape so:
//
//   - Field source order can be changed for cache alignment or
//     readability without breaking clients.
//   - The same struct can decode from EITHER `args:[positional]` OR
//     `args:{named}` — clients pick the form that suits them.
//   - Fields can be renamed (Go) while the wire name stays stable, or
//     vice versa.
//   - Introspection emits per-field metadata (required, omitempty,
//     deprecated, position) so codegen and the explorer UI render the
//     right thing without re-reading struct tags at request time.
//
// FieldMap is built once per (Type) at Register time, validated, and
// cached on the methodEntry. Hot path is map / slice lookup, no
// reflection on struct tags per request.
type FieldMap struct {
	Type   reflect.Type
	Fields []FieldInfo    // source order
	ByName map[string]int // wire name → index into Fields
	ByPos  []int          // position → index into Fields (-1 if no field at that position)
	MaxPos int            // highest positional slot, or -1 if no positional fields
	// Internal / InternalHard are set by a blank sentinel field
	//   _ struct{} `sov:"internal"`       → Internal     (soft hide)
	//   _ struct{} `sov:"internal,hard"`  → InternalHard (hard hide)
	// marking the method that takes this params struct as hidden from
	// introspection. Method-level directive, not a wire field.
	Internal     bool
	InternalHard bool
	// Perm is the declarative per-method authz requirement (HELL-280), set
	// by a `perm=…` directive on the blank `_` sentinel field. OPAQUE to
	// the framework — the consumer's AuthzService interprets it; sov never
	// parses the string. Empty when undeclared. Declaring it more than once
	// on the sentinel is a build error.
	Perm string

	// HeaderFields lists indices into Fields that bind from a request header
	// (FieldInfo.HeaderSource != ""), so dispatch binds them in one pass
	// without scanning every field. Empty (nil) for the common all-body case,
	// so the hot path pays nothing.
	HeaderFields []int
}

// FieldInfo is the per-field resolution of the tag grammar.
type FieldInfo struct {
	GoName     string
	WireName   string // wire/JSON name
	StructIdx  int    // index into reflect.Type.Field
	Position   int    // -1 = no positional slot
	Required   bool
	Omitempty  bool
	Deprecated bool
	Type       reflect.Type

	// HeaderSource, when non-empty, binds this field from the named request
	// header (sov:"header=X-Tenant-Id") instead of the request body. Such a
	// field is NOT a body wire field: it has no WireName/Position, is excluded
	// from ByName/ByPos and every body schema, and is bound post-decode from
	// the context header getter. See docs/HEADER_PARAMS.md.
	HeaderSource string

	// Human-facing metadata from the sov tag `key=value` pairs. None
	// affect dispatch — they flow into Describe(), the explorer UI,
	// and codegen JSDoc.
	Title   string // short label, e.g. "Username"
	Desc    string // one-line hint shown as placeholder / helper text
	Doc     string // long-form documentation surfaced as tooltip / JSDoc body
	Example string // example value the explorer can pre-fill
}

// snakeIdent matches a valid snake_case JSON identifier.
var snakeIdent = regexp.MustCompile(`^[a-z_][a-z0-9_]*$`)

// splitSovTokens splits a sov tag value on commas, honoring `\,` as
// an escaped literal comma so kv values can carry punctuation.
// `\\,` becomes a literal `\,` token boundary (escape the escape).
func splitSovTokens(raw string) []string {
	var (
		out []string
		buf strings.Builder
	)
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if c == '\\' && i+1 < len(raw) && raw[i+1] == ',' {
			buf.WriteByte(',')
			i++
			continue
		}
		if c == ',' {
			out = append(out, buf.String())
			buf.Reset()
			continue
		}
		buf.WriteByte(c)
	}
	out = append(out, buf.String())
	return out
}

// BuildFieldMap parses `sov:` (with `json:` fallback) tags on t and
// returns a validated FieldMap. Errors are reported with full field
// context so callers can panic at boot with a clear message.
//
// t must be a struct type. Pointer-to-struct callers should pass
// t.Elem().
func BuildFieldMap(t reflect.Type) (*FieldMap, error) {
	if t == nil {
		return nil, fmt.Errorf("BuildFieldMap: nil type")
	}
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil, fmt.Errorf("BuildFieldMap: %s is not a struct", t)
	}

	fm := &FieldMap{
		Type:   t,
		Fields: make([]FieldInfo, 0, t.NumField()),
		ByName: make(map[string]int, t.NumField()),
		MaxPos: -1,
	}

	type pending struct {
		idx         int
		info        FieldInfo
		explicitPos bool
		hasSovTag   bool
	}
	var pendings []pending

	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		// Blank sentinel field carrying method-level directives, e.g.
		//   _ struct{} `sov:"internal"`       → soft hide
		//   _ struct{} `sov:"internal,hard"`  → hard hide
		// Read BEFORE the exported check (blank `_` is unexported). The
		// first token must be `internal`; an optional `hard` token raises
		// it to hard. The sentinel never becomes a wire field.
		if sf.Name == "_" {
			if sovRaw, ok := sf.Tag.Lookup("sov"); ok {
				// The blank `_` sentinel carries method-level directives, in
				// any order: `internal` (soft hide), `hard` (raise to hard
				// hide; requires `internal`), and `perm=<token>` (declarative
				// authz requirement, HELL-280 — opaque, never parsed here).
				var sawInternal bool
				for _, tok := range splitSovTokens(sovRaw) {
					tok = strings.TrimSpace(tok)
					switch {
					case tok == "":
						// trailing/empty token
					case tok == "internal":
						fm.Internal = true
						sawInternal = true
					case tok == "hard":
						fm.InternalHard = true
					case strings.HasPrefix(tok, "perm="):
						val := tok[len("perm="):]
						if val == "" {
							return nil, fmt.Errorf("%s: blank `_` field sov tag has empty perm= value", t.Name())
						}
						if fm.Perm != "" {
							return nil, fmt.Errorf("%s: blank `_` field sov tag declares perm= more than once", t.Name())
						}
						fm.Perm = val
					default:
						return nil, fmt.Errorf("%s: blank `_` field sov tag has unknown directive %q (allowed: internal, hard, perm=…)", t.Name(), tok)
					}
				}
				if fm.InternalHard && !sawInternal {
					return nil, fmt.Errorf("%s: blank `_` field sov tag 'hard' requires 'internal' (use `sov:\"internal,hard\"`)", t.Name())
				}
			}
			continue
		}
		if !sf.IsExported() {
			continue
		}
		sovRaw, hasSov := sf.Tag.Lookup("sov")
		// `sov:"-"` excludes the field from the wire entirely.
		if sovRaw == "-" {
			continue
		}

		info := FieldInfo{
			GoName:    sf.Name,
			StructIdx: i,
			Position:  -1,
			Type:      sf.Type,
		}

		var explicitName, explicitPos bool

		if hasSov && sovRaw != "" {
			parts := splitSovTokens(sovRaw)
			// A header= directive can appear anywhere in the tag; pull it out
			// first. A header-bound field takes its value from a request
			// header, not the body, so it has NO wire name/position — only
			// flags + human metadata apply.
			rest, hdr, herr := extractHeaderDirective(parts, t, sf)
			if herr != nil {
				return nil, herr
			}
			if hdr != "" {
				// A header is a single string, so a header= field must be a
				// scalar (or pointer to one). Reject struct/slice/map at boot
				// rather than deferring to a first-request 400.
				if !isScalarHeaderType(sf.Type) {
					return nil, fmt.Errorf("field %s.%s: sov header= requires a scalar field type (string/bool/int/uint/float, or a pointer to one), got %s", t.Name(), sf.Name, sf.Type)
				}
				info.HeaderSource = hdr
				if err := applyFieldFlags(&info, rest, t, sf); err != nil {
					return nil, err
				}
				// A field is body OR header, never both: an explicit json/sov
				// wire name alongside header= is ambiguous.
				if jt, ok := sf.Tag.Lookup("json"); ok {
					if jn, _, _ := strings.Cut(jt, ","); jn != "" && jn != "-" {
						return nil, fmt.Errorf("field %s.%s: header= field must not also declare a json wire name %q (a field is body OR header, not both)", t.Name(), sf.Name, jn)
					}
				}
			} else {
				// parts[0] = name (optional), parts[1] = position (optional), parts[2:] = flags
				if parts[0] != "" {
					if !snakeIdent.MatchString(parts[0]) {
						return nil, fmt.Errorf("field %s.%s: sov tag name %q is not a valid snake_case identifier", t.Name(), sf.Name, parts[0])
					}
					info.WireName = parts[0]
					explicitName = true
				}
				if len(parts) >= 2 && parts[1] != "" {
					p, err := strconv.Atoi(parts[1])
					if err != nil {
						return nil, fmt.Errorf("field %s.%s: sov tag position %q is not an integer: %w", t.Name(), sf.Name, parts[1], err)
					}
					if p < 0 {
						return nil, fmt.Errorf("field %s.%s: sov tag position %d must be >= 0", t.Name(), sf.Name, p)
					}
					info.Position = p
					explicitPos = true
				}
				if len(parts) > 2 {
					if err := applyFieldFlags(&info, parts[2:], t, sf); err != nil {
						return nil, err
					}
				}
			}
		}

		if info.Required && info.Omitempty {
			return nil, fmt.Errorf("field %s.%s: sov tag has both 'required' and 'omitempty' — pick one", t.Name(), sf.Name)
		}

		// JSON tag fallback for wire name. Header fields have no wire name.
		if !explicitName && info.HeaderSource == "" {
			if jt, ok := sf.Tag.Lookup("json"); ok {
				jname := strings.Split(jt, ",")[0]
				if jname == "-" {
					continue
				}
				if jname != "" {
					if !snakeIdent.MatchString(jname) {
						return nil, fmt.Errorf("field %s.%s: json tag name %q is not a valid snake_case identifier (used as sov wire name fallback)", t.Name(), sf.Name, jname)
					}
					info.WireName = jname
					explicitName = true
				}
				// Honor json:",omitempty" as an omitempty hint when sov tag absent.
				if strings.Contains(jt, "omitempty") && !info.Required {
					info.Omitempty = true
				}
			}
		}

		// Snake-case the Go field name if no explicit wire name. Header
		// fields are not body wire fields, so they get no name.
		if !explicitName && info.HeaderSource == "" {
			info.WireName = snakeCase(sf.Name)
		}

		_ = explicitName // explicitName is reflected in info.WireName; no further use
		pendings = append(pendings, pending{
			idx:         len(fm.Fields),
			info:        info,
			explicitPos: explicitPos,
			hasSovTag:   hasSov && sovRaw != "",
		})
		fm.Fields = append(fm.Fields, info)
	}

	// Per-field auto-position rule:
	//   - Field has explicit position via sov tag → respect it.
	//   - Field has sov tag WITHOUT a position → stay named-only (Position=-1).
	//   - Field has no sov tag at all → auto-position by source order.
	//
	// This makes `sov:"x"` mean "named only" (per PLAN line 661–672)
	// while keeping the tag-free 80% case purely positional + named
	// at the same source order.
	// Auto-position untagged fields into the lowest positional slots NOT taken
	// by an explicit position, in source order. Tagged (named-only) and header
	// fields consume NO slot, so an interspersed header= or `sov:"name"` field
	// no longer punches a gap that fails the contiguity check below.
	usedPos := map[int]bool{}
	for i := range fm.Fields {
		if pendings[i].explicitPos {
			usedPos[fm.Fields[i].Position] = true
		}
	}
	nextPos := 0
	for i, p := range pendings {
		if p.explicitPos || p.hasSovTag {
			continue
		}
		for usedPos[nextPos] {
			nextPos++
		}
		fm.Fields[i].Position = nextPos
		usedPos[nextPos] = true
	}

	// Build ByName + ByPos with validation. Header fields are bound from a
	// request header, not the body, so they are collected into HeaderFields
	// and kept OUT of the body wire maps.
	seenHeader := map[string]bool{}
	for i, f := range fm.Fields {
		if f.HeaderSource != "" {
			// Two fields binding from the same header is a silent
			// mis-declaration — reject it, mirroring the body duplicate-name
			// check. Case-insensitive: HTTP header names are.
			key := strings.ToLower(f.HeaderSource)
			if seenHeader[key] {
				return nil, fmt.Errorf("field %s.%s: duplicate sov header= %q", t.Name(), f.GoName, f.HeaderSource)
			}
			seenHeader[key] = true
			fm.HeaderFields = append(fm.HeaderFields, i)
			continue
		}
		if _, dup := fm.ByName[f.WireName]; dup {
			return nil, fmt.Errorf("field %s.%s: duplicate wire name %q", t.Name(), f.GoName, f.WireName)
		}
		fm.ByName[f.WireName] = i
		if f.Position > fm.MaxPos {
			fm.MaxPos = f.Position
		}
	}

	if fm.MaxPos >= 0 {
		fm.ByPos = make([]int, fm.MaxPos+1)
		for i := range fm.ByPos {
			fm.ByPos[i] = -1
		}
		for i, f := range fm.Fields {
			if f.Position < 0 {
				continue
			}
			if fm.ByPos[f.Position] != -1 {
				other := fm.Fields[fm.ByPos[f.Position]].GoName
				return nil, fmt.Errorf("field %s.%s: duplicate sov tag position %d (also on %s)", t.Name(), f.GoName, f.Position, other)
			}
			fm.ByPos[f.Position] = i
		}
		// When positions are mixed with named-only fields, the
		// positional contiguity rule applies only to the positional
		// slots that exist — gaps are explicit errors.
		for i, idx := range fm.ByPos {
			if idx == -1 {
				return nil, fmt.Errorf("type %s: positional slot %d has no field — positions must be contiguous 0..N-1", t.Name(), i)
			}
		}
	}

	return fm, nil
}

// extractHeaderDirective pulls a header=NAME token out of the sov tag parts,
// returning the remaining tokens and the header name ("" if none). A field is
// body-bound unless it carries header=. Declaring header= twice, or with an
// empty name, is a build error.
func extractHeaderDirective(parts []string, t reflect.Type, sf reflect.StructField) ([]string, string, error) {
	hdr := ""
	rest := make([]string, 0, len(parts))
	for _, p := range parts {
		if name, ok := strings.CutPrefix(strings.TrimSpace(p), "header="); ok {
			name = strings.TrimSpace(name) // so " X-Sov-*" can't slip past the reserved check
			if hdr != "" {
				return nil, "", fmt.Errorf("field %s.%s: sov tag declares header= more than once", t.Name(), sf.Name)
			}
			if name == "" {
				return nil, "", fmt.Errorf("field %s.%s: sov tag header= has an empty header name", t.Name(), sf.Name)
			}
			// The X-Sov-* namespace carries VERIFIED claims injected between
			// trusted nodes (see gateway ClaimsFromHeaders / TrustUpstreamClaims).
			// A user param must never bind from that channel — reject it loudly
			// at boot so a header= can't shadow or read the claim path.
			if strings.HasPrefix(strings.ToLower(name), "x-sov-") {
				return nil, "", fmt.Errorf("field %s.%s: sov tag header=%q is in the reserved X-Sov-* claims namespace (verified claims travel there, not user params)", t.Name(), sf.Name, name)
			}
			hdr = name
			continue
		}
		rest = append(rest, p)
	}
	return rest, hdr, nil
}

// applyFieldFlags parses the flag/kv tail of a sov tag (omitempty, required,
// deprecated; title=, desc=, doc=, example=) onto info. Shared by the body
// form (name,pos,FLAGS) and the header form (header=,FLAGS).
func applyFieldFlags(info *FieldInfo, opts []string, t reflect.Type, sf reflect.StructField) error {
	seenKV := map[string]bool{}
	for _, opt := range opts {
		opt = strings.TrimSpace(opt)
		switch opt {
		case "":
			// allow trailing comma
		case "omitempty":
			info.Omitempty = true
		case "required":
			info.Required = true
		case "deprecated":
			info.Deprecated = true
		default:
			i := strings.IndexByte(opt, '=')
			if i <= 0 {
				return fmt.Errorf("field %s.%s: unknown sov tag option %q (flags: omitempty, required, deprecated; kv: title=, desc=, doc=, example=)", t.Name(), sf.Name, opt)
			}
			key, value := opt[:i], opt[i+1:]
			if value == "" {
				return fmt.Errorf("field %s.%s: empty value for sov tag key %q", t.Name(), sf.Name, key)
			}
			if seenKV[key] {
				return fmt.Errorf("field %s.%s: duplicate sov tag key %q", t.Name(), sf.Name, key)
			}
			seenKV[key] = true
			switch key {
			case "title":
				info.Title = value
			case "desc":
				info.Desc = value
			case "doc":
				info.Doc = value
			case "example":
				info.Example = value
			default:
				return fmt.Errorf("field %s.%s: unknown sov tag key %q (allowed: title, desc, doc, example)", t.Name(), sf.Name, key)
			}
		}
	}
	return nil
}

// isScalarHeaderType reports whether t is a valid header= field type: a scalar
// (string/bool/int/uint/float) or a pointer to one. Mirrors the kinds
// setScalarFromString can bind — a header is a single string, so struct, slice,
// and map are not header-bindable.
func isScalarHeaderType(t reflect.Type) bool {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.String, reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	}
	return false
}

// tagHasHeader reports whether a sov struct tag carries a header= directive.
func tagHasHeader(sovRaw string) bool {
	if sovRaw == "" || sovRaw == "-" {
		return false
	}
	for _, tok := range splitSovTokens(sovRaw) {
		if strings.HasPrefix(strings.TrimSpace(tok), "header=") {
			return true
		}
	}
	return false
}

// headerStructType returns the struct type reachable from t for nested-header
// scanning: it dereferences pointers and unwraps slice/array/map element types
// to a struct, or nil when there is no struct to descend into.
func headerStructType(t reflect.Type) reflect.Type {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	switch t.Kind() {
	case reflect.Struct:
		return t
	case reflect.Slice, reflect.Array, reflect.Map:
		return headerStructType(t.Elem())
	}
	return nil
}

// RejectNestedHeaders fails if any struct REACHABLE from a top-level params
// type (but not the top-level struct's own direct fields) carries a header=
// tag. A header= is only bound on the top-level params struct; a nested one is
// never bound AND — because nested structs are decoded by plain json.Unmarshal
// — would be silently settable from the request body while the published schema
// shows it absent. So it must be a boot-time error, not a live spoofing vector.
// Callers pass the handler's params struct type.
func RejectNestedHeaders(top reflect.Type) error {
	seen := map[reflect.Type]bool{}
	var visit func(t reflect.Type, isTop bool) error
	visit = func(t reflect.Type, isTop bool) error {
		for t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		if t.Kind() != reflect.Struct || seen[t] {
			return nil
		}
		seen[t] = true
		for i := 0; i < t.NumField(); i++ {
			sf := t.Field(i)
			if sf.Name == "_" || !sf.IsExported() {
				continue
			}
			if !isTop && tagHasHeader(sf.Tag.Get("sov")) {
				return fmt.Errorf("field %s.%s: sov header= is only valid on a direct field of the top-level params struct, not on a nested or embedded struct field — sov does not flatten embedded structs, so such a field is never bound and would be spoofable from the request body; declare header= fields directly on each params struct", t.Name(), sf.Name)
			}
			if et := headerStructType(sf.Type); et != nil {
				if err := visit(et, false); err != nil {
					return err
				}
			}
		}
		return nil
	}
	return visit(top, true)
}

// snakeCase converts a Go-style identifier to snake_case. Conservative:
// inserts an underscore before an upper-case rune that follows a
// lower-case or digit. Acronyms (ABC) stay together until the boundary.
func snakeCase(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 4)
	runes := []rune(s)
	for i, r := range runes {
		if i > 0 && unicode.IsUpper(r) {
			prev := runes[i-1]
			if !unicode.IsUpper(prev) || (i+1 < len(runes) && !unicode.IsUpper(runes[i+1])) {
				b.WriteByte('_')
			}
		}
		if unicode.IsUpper(r) {
			b.WriteRune(unicode.ToLower(r))
		} else {
			b.WriteRune(r)
		}
	}
	return b.String()
}
