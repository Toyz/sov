package rpc

// ParamField describes one JSON field on a method's params object.
// Used by Explorer / codegen / OpenAPI emission downstream.
type ParamField struct {
	JSONName     string `json:"jsonName"`
	SchemaType   string `json:"schemaType"`             // OpenAPI-shaped: string|number|boolean|array|object
	DesignerHint string `json:"designerHint,omitempty"` // short human label
	Required     bool   `json:"required"`               // false when omitempty was set
	Position     int    `json:"position"`               // -1 = no positional slot
	Omitempty    bool   `json:"omitempty,omitempty"`
	Deprecated   bool   `json:"deprecated,omitempty"`
	TypeName     string `json:"typeName,omitempty"` // Go type name when SchemaType=="object" — feeds the type catalog

	// Human-facing metadata from the sov tag `key=value` pairs.
	// Surfaced by the explorer UI + codegen JSDoc; ignored by dispatch.
	Title   string `json:"title,omitempty"`
	Desc    string `json:"desc,omitempty"`
	Doc     string `json:"doc,omitempty"`
	Example string `json:"example,omitempty"`
}

// MethodDescriptor is one exported router method.
type MethodDescriptor struct {
	Method             string       `json:"method"`   // wire name (camelCase) — URL segment
	Title              string       `json:"title"`    // product-facing label derived from goName
	PostPath           string       `json:"postPath"` // /rpc/{Router}/{method}
	HasParams          bool         `json:"hasParams"`
	Params             []ParamField `json:"params,omitempty"`
	RequestTypeScript  string       `json:"requestTypeScript"`
	ResponseTypeScript string       `json:"responseTypeScript"`
	// ResponseTypeName is the Go type name of the method's non-error
	// return when it's a named struct (possibly via pointer/slice).
	// Empty for primitive/map results. The type catalog uses it to tag
	// the type's usage role as "response" (data-ownership inference).
	ResponseTypeName string `json:"responseTypeName,omitempty"`
	// Internal marks a SOFT-hidden method: omitted from the default
	// introspect report, but present (with this flag set) in the full
	// payload served under the X-Sov-Introspect-Internal header so the
	// explorer's "show internal" toggle can reveal it.
	Internal bool `json:"internal,omitempty"`
	// HardHidden marks a method stripped from EVERY introspect payload —
	// the framework auth/authz hooks and any author HardHiddenMethods().
	// json:"-" because hard methods are removed before marshal; the flag
	// only needs to survive Describe() → the gateway's strip pass within a
	// single gateway and never crosses the wire.
	HardHidden bool `json:"-"`
	// NestedTypes are the named struct types referenced by Params
	// (transitively). Lets the IntrospectReport.Types catalog include
	// every type the generated client needs without losing reflect
	// access at catalog-build time. Keyed by the Go type's Name.
	NestedTypes map[string][]ParamField `json:"nestedTypes,omitempty"`
}

// RouterDescriptor describes one registered router.
type RouterDescriptor struct {
	Router  string             `json:"router"` // wire name (URL segment)
	Title   string             `json:"title"`  // group label for explorers
	Methods []MethodDescriptor `json:"methods"`
}
