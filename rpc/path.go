package rpc

import "strings"

// SplitRPCPath parses a request path of the form /rpc/{router}/{method}
// and returns the router + method segments, or ok=false on anything
// else. Rejects paths where method contains a `/` (defense against
// service-level _X smuggling — the gateway treats /rpc/Foo/_x as a
// distinct, refused branch).
//
// One canonical implementation; gateway, signing middleware, and any
// future transport adapter consume it instead of forking three nearly
// identical helpers.
func SplitRPCPath(p string) (router, method string, ok bool) {
	const prefix = "/rpc/"
	if !strings.HasPrefix(p, prefix) {
		return "", "", false
	}
	rest := strings.TrimPrefix(p, prefix)
	i := strings.IndexByte(rest, '/')
	if i <= 0 || i == len(rest)-1 {
		return "", "", false
	}
	router = rest[:i]
	method = rest[i+1:]
	if strings.ContainsRune(method, '/') {
		return "", "", false
	}
	return router, method, true
}
