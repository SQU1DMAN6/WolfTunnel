package browsernet

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type resolveFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolveFunc) LookupNetIP(c context.Context, n, h string) ([]netip.Addr, error) {
	return f(c, n, h)
}
func TestEveryDialChecksAllAnswersAndPinsIP(t *testing.T) {
	var dials atomic.Int32
	p, e := New(context.Background(), Options{Resolver: resolveFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34"), netip.MustParseAddr("127.0.0.1")}, nil
	}), Dial: func(context.Context, string, string) (net.Conn, error) {
		dials.Add(1)
		return nil, errors.New("test dial")
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	for _, target := range []string{"example.test:80", "example.test:443", "127.0.0.1:80", "169.254.169.254:80", "168.63.129.16:80", "[::ffff:127.0.0.1]:80", "example.test:22"} {
		if c, e := p.Dial(context.Background(), "tcp", target); e == nil {
			c.Close()
			t.Fatal("prohibited connection accepted")
		}
	}
	if dials.Load() != 0 {
		t.Fatal("dialed mixed/private DNS answers")
	}
	p.resolve = resolveFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	})
	p.dial = func(c context.Context, n, a string) (net.Conn, error) {
		if a != "93.184.216.34:443" {
			t.Fatal("unchecked hostname was dialed")
		}
		return nil, errors.New("controlled stop")
	}
	_, _ = p.Dial(context.Background(), "tcp", "example.test:443")
}
func TestProxyMethodsSchemesAndNoForwardedClientIdentity(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Forwarded") != "" || r.Header.Get("X-Forwarded-For") != "" {
			t.Error("identity header leaked")
		}
		body, _ := io.ReadAll(r.Body)
		fmt.Fprintf(w, "%s:%s", r.Method, body)
	}))
	defer upstream.Close()
	p, e := New(context.Background(), Options{Resolver: resolveFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
	}), Dial: func(c context.Context, n, a string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, n, upstream.Listener.Addr().String())
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	for _, raw := range []string{"http://127.0.0.1/", "http://10.0.0.1/", "http://[::1]/", "file:///etc/passwd", "http://a:b@fixture.test/", "http://fixture.test:8080/"} {
		r := httptest.NewRequest("GET", raw, nil)
		w := httptest.NewRecorder()
		p.ServeHTTP(w, r)
		if w.Code != 403 {
			t.Fatalf("prohibited URL accepted: %s", raw)
		}
	}
	r := httptest.NewRequest("POST", "http://fixture.test/form", strings.NewReader("field=value"))
	r.Header.Set("X-Forwarded-For", "client-identity")
	r.Header.Set("Forwarded", "client-identity")
	w := httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 200 || w.Body.String() != "POST:field=value" {
		t.Fatalf("form handling failed: %d", w.Code)
	}
	r = httptest.NewRequest("POST", "http://fixture.test/upload", strings.NewReader("file"))
	r.Header.Set("Content-Type", "multipart/form-data; boundary=x")
	w = httptest.NewRecorder()
	p.ServeHTTP(w, r)
	if w.Code != 403 {
		t.Fatal("file upload accepted")
	}
	for _, raw := range []string{"ws://127.0.0.1/", "wss://169.254.169.254/", "wss://example.test:8080/"} {
		if Validate(raw) == nil {
			t.Fatal("WebSocket bypass")
		}
	}
}

func TestDNSRebindingRevalidatesEachConnection(t *testing.T) {
	var queries, dials atomic.Int32
	p, e := New(context.Background(), Options{Resolver: resolveFunc(func(context.Context, string, string) ([]netip.Addr, error) {
		if queries.Add(1) == 1 {
			return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
		}
		return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
	}), Dial: func(_ context.Context, _ string, address string) (net.Conn, error) {
		dials.Add(1)
		if address != "93.184.216.34:443" {
			t.Error("unchecked DNS address dialed")
		}
		return nil, errors.New("controlled no upstream")
	}})
	if e != nil {
		t.Fatal(e)
	}
	defer p.Close()
	_, _ = p.Dial(context.Background(), "tcp", "changing.test:443")
	if _, e = p.Dial(context.Background(), "tcp", "changing.test:443"); !errors.Is(e, ErrPolicy) {
		t.Fatal("rebound metadata address accepted")
	}
	if queries.Load() != 2 || dials.Load() != 1 {
		t.Fatal("resolution was cached or unchecked")
	}
}

func TestProxyFailureCategoriesAndRemoteStatuses(t *testing.T) {
	for _, test := range []struct {
		name   string
		err    error
		status int
		header string
	}{
		{"dns", errors.New("NODE_DNS_ERROR"), 502, "NODE_DNS_ERROR"},
		{"connect", errors.New("raw sensitive internal socket error"), 502, "NODE_UNREACHABLE"},
		{"policy", ErrPolicy, 403, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			p := &Proxy{}
			p.failure(w, test.err)
			if w.Code != test.status || w.Header().Get("X-WolfTunnel-Error") != test.header || strings.Contains(w.Body.String(), "sensitive") {
				t.Fatal("unsafe/incorrect proxy error")
			}
		})
	}
}

func TestTunnelBudgetStopsReadAndWriteAtSharedCap(t *testing.T) {
	for _, writing := range []bool{false, true} {
		a, b := net.Pipe()
		var total atomic.Int64
		total.Store(byteBudget - 3)
		c := &budgetConn{Conn: a, total: &total}
		done := make(chan struct{})
		go func() {
			defer close(done)
			defer b.Close()
			if writing {
				var p [10]byte
				_, _ = b.Read(p[:])
			} else {
				_, _ = b.Write([]byte("12345"))
			}
		}()
		var n int
		var e error
		if writing {
			n, e = c.Write([]byte("12345"))
		} else {
			n, e = c.Read(make([]byte, 10))
		}
		if n != 3 || e != nil || total.Load() != byteBudget {
			t.Fatal("upstream byte limit exceeded", n, e, total.Load())
		}
		if writing {
			n, e = c.Write([]byte("x"))
		} else {
			n, e = c.Read(make([]byte, 1))
		}
		if n != 0 || !errors.Is(e, ErrBudget) {
			t.Fatal("exhausted tunnel continued", n, e)
		}
		c.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("budget did not release connection")
		}
	}
}
