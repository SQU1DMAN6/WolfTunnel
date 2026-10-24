package fetch

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(c context.Context, n, h string) ([]netip.Addr, error) {
	return f(c, n, h)
}
func publicDNS(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}
func controlled(t *testing.T, h http.Handler) (*Fetcher, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(h)
	t.Cleanup(server.Close)
	f := New(DefaultConfig())
	f.resolver = resolverFunc(publicDNS)
	f.dial = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != "93.184.216.34:80" {
			return nil, fmt.Errorf("unexpected validated address %s", address)
		}
		return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
	}
	return f, server
}
func TestDestinationPolicy(t *testing.T) {
	for _, raw := range []string{"file:///tmp/a", "javascript:alert(1)", "ftp://example.com", "https://u:p@example.com", "http://127.0.0.1", "http://10.0.0.1", "http://172.16.0.1", "http://192.168.1.1", "http://169.254.169.254/latest/meta-data/", "http://100.100.100.200", "http://localhost.", "http://x.localhost", "http://metadata.google.internal", "http://example.com:8080", "https://example.com:80", "http://[::1]", "http://[::ffff:127.0.0.1]", "http://[fc00::1]", "http://[fe80::1%25eth0]", "http://[ff02::1]", "http://[64:ff9b::a00:1]", "http://[2002:7f00:1::]", "http://[2001:db8::1]", "http://224.0.0.1", "http://0.0.0.0", "http://example.com/\r\n"} {
		t.Run(raw, func(t *testing.T) {
			if _, err := Validate(raw); err == nil {
				t.Fatal("prohibited URL accepted")
			}
		})
	}
	for _, raw := range []string{"https://example.com", "http://example.com:80/path?q=a", "https://example.com:443/", "https://[2606:4700:4700::1111]/"} {
		if _, err := Validate(raw); err != nil {
			t.Fatalf("public URL rejected: %s", raw)
		}
	}
}
func TestHTTPStatusesMetadataAndRedaction(t *testing.T) {
	for _, status := range []int{200, 403, 404, 502} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			f, _ := controlled(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("Cookie") != "" || r.Header.Get("Authorization") != "" {
					t.Error("unexpected outbound credentials")
				}
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Set-Cookie", "secret=private")
				w.Header().Set("WWW-Authenticate", "secret")
				w.WriteHeader(status)
				fmt.Fprint(w, "hello")
			}))
			out := f.Get(context.Background(), "http://public.test/path?token=secret#frag")
			if out.Status != status || out.Code != "" || out.BodyLength != 5 || out.FinalURL != "http://public.test/path" || out.Headers["Set-Cookie"] != "" || out.Headers["Www-Authenticate"] != "" {
				t.Fatalf("metadata: %+v", out)
			}
		})
	}
}
func TestDNSValidationAndRebinding(t *testing.T) {
	for _, ip := range []string{"127.0.0.1", "10.0.0.1", "169.254.169.254", "100.64.0.1", "192.0.2.1", "::1", "fd00::1", "::ffff:10.0.0.1"} {
		t.Run(ip, func(t *testing.T) {
			f := New(DefaultConfig())
			f.resolver = resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
				return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr(ip)}, nil
			})
			f.dial = func(context.Context, string, string) (net.Conn, error) {
				t.Error("dialed mixed DNS answer")
				return nil, errors.New("bad dial")
			}
			out := f.Get(context.Background(), "http://public.test")
			if out.Code != "NODE_POLICY_REJECTED" || out.Status != 0 {
				t.Fatalf("%+v", out)
			}
		})
	}
	f, _ := controlled(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://next.test/final", http.StatusSeeOther)
	}))
	var calls atomic.Int32
	f.resolver = resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		if calls.Add(1) == 1 {
			return publicDNS(context.Background(), "", "")
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	})
	if out := f.Get(context.Background(), "http://public.test"); out.Code != "NODE_POLICY_REJECTED" {
		t.Fatalf("rebinding: %+v", out)
	}
	f.resolver = resolverFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("private error details")
	})
	if out := f.Get(context.Background(), "http://public.test"); out.Code != "NODE_DNS_ERROR" || out.Status != 0 {
		t.Fatalf("DNS error: %+v", out)
	}
}
func TestRedirects(t *testing.T) {
	for _, tc := range []struct {
		name, target, want string
		max                int
	}{{"private", "http://127.0.0.1/", "NODE_POLICY_REJECTED", 5}, {"scheme", "file:///tmp/private", "NODE_POLICY_REJECTED", 5}, {"port", "http://public.test:8080/", "NODE_POLICY_REJECTED", 5}, {"loop", "http://public.test/start", "NODE_REDIRECT_LIMIT", 5}, {"limit", "/next", "NODE_REDIRECT_LIMIT", 0}, {"success", "/final?token=hidden", "", 5}} {
		t.Run(tc.name, func(t *testing.T) {
			f, _ := controlled(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/final" {
					if r.Header.Get("Referer") != "" || r.Header.Get("Cookie") != "" {
						t.Error("redirect leaked header")
					}
					w.WriteHeader(200)
					return
				}
				w.Header().Set("Set-Cookie", "secret=1")
				http.Redirect(w, r, tc.target, 303)
			}))
			f.Config.MaxRedirects = tc.max
			out := f.Get(context.Background(), "http://public.test/start")
			if out.Code != tc.want {
				t.Fatalf("%+v", out)
			}
			if tc.want == "" && (out.Status != 200 || out.Redirects != 1 || strings.Contains(out.FinalURL, "token")) {
				t.Fatalf("%+v", out)
			}
		})
	}
}
func TestBoundsTimeoutCancellation(t *testing.T) {
	t.Run("oversize", func(t *testing.T) {
		f, _ := controlled(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", 1024)) }))
		f.Config.MaxBytes = 100
		out := f.Get(context.Background(), "http://public.test")
		if out.Code != "NODE_RESPONSE_TOO_LARGE" || out.BodyLength != 101 || out.Status != 200 {
			t.Fatalf("%+v", out)
		}
	})
	for _, cancelled := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelled), func(t *testing.T) {
			started := make(chan struct{})
			f, _ := controlled(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(started); <-r.Context().Done() }))
			f.Config.Timeout = 50 * time.Millisecond
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if cancelled {
				go func() { <-started; cancel() }()
			}
			out := f.Get(ctx, "http://public.test")
			want := "NODE_TIMEOUT"
			if cancelled {
				want = "NODE_CANCELLED"
			}
			if out.Code != want || out.Status != 0 {
				t.Fatalf("%+v", out)
			}
		})
	}
}
func FuzzURLAndRedirectValidation(f *testing.F) {
	for _, s := range []string{"https://example.com", "http://127.0.0.1", "file:///tmp", "http://[::ffff:127.0.0.1]", "//redirect.test/x"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, s string) {
		u, err := Validate(s)
		if err == nil {
			if u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
				t.Fatal("unsafe URL accepted")
			}
			base, _ := Validate("https://example.com")
			_, _ = Validate(base.ResolveReference(u).String())
		}
	})
}
