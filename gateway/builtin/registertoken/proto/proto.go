// Package proto is the wire format for the static register-token join
// gate. Pure helpers, no gateway dependency: the registertoken plugin
// uses Verify on the registry side; the pod-side heartbeat
// (gateway/mesh.go) stamps RegisterTokenHeader on every register POST.
//
// This is the SIMPLE join tier — a shared bearer token (kubeadm /
// Consul-gossip style), not the HMAC-over-body signature the meshsecret
// plugin provides. It is replayable by design: capture the token and you
// can register. Treat it as a network-isolated bootstrap credential and
// rotate it. It gates WHO may join the mesh; it says nothing about
// per-request identity (that's the X-Sov-* data plane).
package proto

import "crypto/subtle"

// RegisterTokenHeader carries the shared join token on /rpc/_register.
const RegisterTokenHeader = "X-Sov-Register-Token"

// Verify reports whether the presented token matches the configured one,
// in constant time. Both empty is treated as no-match so a misconfigured
// (empty) server token can never be satisfied by an empty header.
func Verify(want, presented []byte) bool {
	if len(want) == 0 || len(presented) == 0 {
		return false
	}
	return subtle.ConstantTimeCompare(want, presented) == 1
}
