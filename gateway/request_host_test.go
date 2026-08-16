package gateway_test

import (
	"context"
	"net"
	"net/http"
	"testing"
	"time"

	. "github.com/Toyz/sov/gateway"
)

// net/http lifts Host out of the header map into http.Request.Host, so a plugin
// reading req.Header would never see it. The adapter must surface it as the
// first-class req.Host field.
func TestNetHTTP_PopulatesRequestHost(t *testing.T) {
	hostCh := make(chan string, 1)
	srv := NewNetHTTPServer(NetHTTPOptions{})
	srv.Handle(func(_ context.Context, req *Request) *Response {
		select {
		case hostCh <- req.Host:
		default:
		}
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

	client := &http.Client{Timeout: 2 * time.Second}
	var resp *http.Response
	for i := 0; i < 100; i++ {
		hreq, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/x", nil)
		hreq.Host = "tenant-a.example.com" // vhost the caller addressed
		resp, err = client.Do(hreq)
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET never succeeded: %v", err)
	}
	resp.Body.Close()

	select {
	case got := <-hostCh:
		if got != "tenant-a.example.com" {
			t.Fatalf("req.Host = %q, want tenant-a.example.com", got)
		}
	case <-time.After(time.Second):
		t.Fatal("handler never saw the request")
	}
}
