package gateway_test

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"math/big"
	"net"
	"net/http"
	"testing"
	"time"

	. "github.com/Toyz/sov/gateway"
)

func selfSignedCert(t *testing.T) tls.Certificate {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     []string{"localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
}

// A supplied TLSConfig must make the server actually serve HTTPS — the
// escape hatch SECURITY.md documents. Before the fix, ListenAndServe ignored
// TLSConfig and served plaintext.
func TestNetHTTP_TLSConfigServesHTTPS(t *testing.T) {
	cert := selfSignedCert(t)
	srv := NewNetHTTPServer(NetHTTPOptions{TLSConfig: &tls.Config{Certificates: []tls.Certificate{cert}}})
	srv.Handle(func(_ context.Context, _ *Request) *Response {
		return &Response{Status: 200, Body: []byte(`{"ok":true}`)}
	})

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go srv.ListenAndServe(ctx, addr)

	client := &http.Client{Transport: &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}}}
	var resp *http.Response
	for i := 0; i < 100; i++ {
		resp, err = client.Get("https://" + addr + "/anything")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("HTTPS GET never succeeded: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("HTTPS status = %d", resp.StatusCode)
	}

	// Plaintext HTTP to a TLS listener must NOT reach our handler — Go replies
	// 400 "Client sent an HTTP request to an HTTPS server" (proves it's TLS).
	plain := &http.Client{Timeout: 2 * time.Second}
	if pr, perr := plain.Get("http://" + addr + "/"); perr == nil {
		pr.Body.Close()
		if pr.StatusCode == 200 {
			t.Error("plaintext HTTP got 200 from a TLS server — TLS not enforced")
		}
	}
}
