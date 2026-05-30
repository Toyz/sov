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
				toks := splitSovTokens(sovRaw)
				if len(toks) == 0 || strings.TrimSpace(toks[0]) != "internal" {
					return nil, fmt.Errorf("%s: blank `_` field sov tag %q must start with 'internal' (the method-level hide directive)", t.Name(), sovRaw)
				}
				fm.Internal = true
				for _, tok := range toks[1:] {
					switch strings.TrimSpace(tok) {
					case "hard":
						fm.InternalHard = true
					case "":
						// trailing comma
					default:
						return nil, fmt.Errorf("%s: blank `_` field sov tag has unknown directive %q (allowed: internal, hard)", t.Name(), tok)
					}
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
				seenKV := map[string]bool{}
				for _, opt := range parts[2:] {
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
						if i := strings.IndexByte(opt, '='); i > 0 {
							key := opt[:i]
							value := opt[i+1:]
							if value == "" {
								return nil, fmt.Errorf("field %s.%s: empty value for sov tag key %q", t.Name(), sf.Name, key)
							}
							if seenKV[key] {
								return nil, fmt.Errorf("field %s.%s: duplicate sov tag key %q", t.Name(), sf.Name, key)
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
								return nil, fmt.Errorf("field %s.%s: unknown sov tag key %q (allowed: title, desc, doc, example)", t.Name(), sf.Name, key)
							}
							continue
						}
						return nil, fmt.Errorf("field %s.%s: unknown sov tag option %q (flags: omitempty, required, deprecated; kv: title=, desc=, doc=, example=)", t.Name(), sf.Name, opt)
					}
				}
			}
		}

		if info.Required && info.Omitempty {
			return nil, fmt.Errorf("field %s.%s: sov tag has both 'required' and 'omitempty' — pick one", t.Name(), sf.Name)
		}

		// JSON tag fallback for wire name.
		if !explicitName {
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

		// Snake-case the Go field name if no explicit wire name.
		if !explicitName {
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
	for i, p := range pendings {
		if p.explicitPos || p.hasSovTag {
			continue
		}
		fm.Fields[i].Position = i
	}

	// Build ByName + ByPos with validation.
	for i, f := range fm.Fields {
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
