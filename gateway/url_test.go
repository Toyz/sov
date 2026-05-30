package gateway_test

import (
	"testing"

	. "github.com/Toyz/sov/gateway"
)

func TestNormalizeUpstreamURL(t *testing.T) {
	cases := []struct {
		in   string
		want string
		err  bool
	}{
		{"http://team-a:8080", "http://team-a:8080", false},
		{"http://Team-A:8080/", "http://team-a:8080", false},
		{"HTTP://team-a:8080/rpc/_introspect", "http://team-a:8080", false},
		{"http://Prime.Internal", "http://prime.internal:80", false},
		{"https://Prime.Internal/health", "https://prime.internal:443", false},
		{"http://user:pw@team-a:8080", "http://team-a:8080", false},
		{"http://team-a:8080?x=1#frag", "http://team-a:8080", false},
		{"http://[::1]:9100", "http://[::1]:9100", false},
		{"  http://team-a:8080  ", "http://team-a:8080", false},
		// errors
		{"", "", true},
		{"ftp://team-a:8080", "", true},
		{"http://", "", true},
		{"::not-a-url::", "", true},
	}
	for _, c := range cases {
		got, err := NormalizeUpstreamURL(c.in)
		if c.err {
			if err == nil {
				t.Errorf("NormalizeUpstreamURL(%q) = %q, want error", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("NormalizeUpstreamURL(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("NormalizeUpstreamURL(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
