package transport

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
	"wolftunnel/internal/testsite"

	"wolftunnel/internal/identity"
)

func testTLS(t *testing.T) (string, int, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "node")
	if e := os.Mkdir(root, 0700); e != nil {
		t.Fatal(e)
	}
	cert, e := identity.Node(root)
	if e != nil {
		t.Fatal(e)
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	var wg sync.WaitGroup
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for {
			raw, e := ln.Accept()
			if e != nil {
				return
			}
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer raw.Close()
				stop := context.AfterFunc(ctx, func() { raw.Close() })
				defer stop()
				c := tls.Server(raw, &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}})
				c.SetDeadline(time.Now().Add(2 * time.Second))
				if c.Handshake() == nil {
					io.Copy(io.Discard, c)
				}
			}()
		}
	}()
	t.Cleanup(func() { cancel(); ln.Close(); <-accepted; wg.Wait() })
	return "127.0.0.1", ln.Addr().(*net.TCPAddr).Port, identity.Fingerprint(cert.Certificate[0])
}

type resolverFunc func(context.Context, string, string) ([]netip.Addr, error)

func (f resolverFunc) LookupNetIP(c context.Context, n, h string) ([]netip.Addr, error) {
	return f(c, n, h)
}
func TestDirectTLSStagesAndMandatoryPin(t *testing.T) {
	host, port, pin := testTLS(t)
	for _, correct := range []bool{true, false} {
		p := pin
		if !correct {
			p = strings.Repeat("0", 64)
		}
		var events []Event
		a := NewAttempt("control", "tls-tcp", func(e Event) { events = append(events, e) })
		c, e := (Dialer{}).Connect(context.Background(), Endpoint{Host: host, Port: port, Pin: p}, a)
		a.Finish(e)
		if correct {
			if e != nil {
				t.Fatal(e)
			}
			c.Close()
			for _, state := range []State{Resolving, Connecting, TLSHandshake, VerifyingIdentity, Connected} {
				found := false
				for _, ev := range events {
					found = found || ev.State == state
				}
				if !found {
					t.Fatal("stage absent", state)
				}
			}
		} else if Code(e) != "NODE_IDENTITY_CHANGED" {
			t.Fatal("wrong pin accepted", e)
		}
	}
}
func TestDNSHandshakeTimeoutAndCancellation(t *testing.T) {
	fail := Dialer{Resolver: resolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
		return nil, &net.DNSError{Err: "fixture", IsNotFound: true}
	})}
	if _, e := fail.Connect(context.Background(), Endpoint{Host: "invalid.test", Port: 1, Pin: strings.Repeat("0", 64)}, NewAttempt("control", "", nil)); Code(e) != "NODE_DNS_ERROR" {
		t.Fatal(e)
	}
	started := make(chan struct{})
	blocked := Dialer{Resolver: resolverFunc(func(ctx context.Context, _, _ string) ([]netip.Addr, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, e := blocked.Connect(ctx, Endpoint{Host: "blocked.test", Port: 1, Pin: strings.Repeat("0", 64)}, NewAttempt("control", "", nil))
		done <- e
	}()
	<-started
	cancel()
	select {
	case e := <-done:
		if !errors.Is(e, context.Canceled) {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("DNS cancellation stalled")
	}
	for _, closeEarly := range []bool{false, true} {
		ln, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		serverDone := make(chan struct{})
		go func() {
			defer close(serverDone)
			c, e := ln.Accept()
			if e != nil {
				return
			}
			defer c.Close()
			if !closeEarly {
				io.Copy(io.Discard, c)
			}
		}()
		begin := time.Now()
		_, e = (Dialer{StageTimeout: 60 * time.Millisecond}).Connect(context.Background(), Endpoint{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Pin: strings.Repeat("0", 64)}, NewAttempt("control", "", nil))
		ln.Close()
		<-serverDone
		if Code(e) != "TLS_HANDSHAKE_FAILED" || time.Since(begin) > time.Second {
			t.Fatal("handshake failure not bounded", e)
		}
	}
}
func TestRetryBudgetAndSecurityFailuresArePermanent(t *testing.T) {
	var attempts, waits atomic.Int32
	d := Dialer{DialContext: func(context.Context, string, string) (net.Conn, error) {
		attempts.Add(1)
		return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
	}, Wait: func(context.Context, time.Duration) error { waits.Add(1); return nil }}
	_, e := d.Connect(context.Background(), Endpoint{Host: "127.0.0.1", Port: 1, Pin: strings.Repeat("0", 64), Retries: 2}, NewAttempt("control", "", nil))
	if Code(e) != "CONNECTION_REFUSED" || attempts.Load() != 3 || waits.Load() != 2 {
		t.Fatal("unbounded/missing retry", e, attempts.Load(), waits.Load())
	}
	for _, code := range []string{"NODE_IDENTITY_CHANGED", "NODE_IDENTITY_EXPIRED", "PROXY_REJECTED", "PROXY_AUTH_REQUIRED", "UNSUPPORTED_TRANSPORT", "INVALID_PROXY"} {
		if Retryable(At(VerifyingIdentity, code, nil)) {
			t.Fatal("security/config error retried", code)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := waitContext(ctx, time.Hour); !errors.Is(e, context.Canceled) {
		t.Fatal("retry wait ignored cancel")
	}
}
func TestExplicitConnectProxyAndRejection(t *testing.T) {
	host, port, pin := testTLS(t)
	target := net.JoinHostPort(host, strconv.Itoa(port))
	for _, status := range []int{200, 201, 403, 407} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			var connsMu sync.Mutex
			var conns []net.Conn
			proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodConnect || r.Host != target {
					t.Error("wrong CONNECT target")
					w.WriteHeader(403)
					return
				}
				if status < 200 || status >= 300 {
					w.WriteHeader(status)
					io.WriteString(w, "private-secret-response")
					return
				}
				peer, _, e := w.(http.Hijacker).Hijack()
				if e != nil {
					return
				}
				up, e := net.Dial("tcp", target)
				if e != nil {
					peer.Close()
					return
				}
				connsMu.Lock()
				conns = append(conns, peer, up)
				connsMu.Unlock()
				defer peer.Close()
				defer up.Close()
				io.WriteString(peer, "HTTP/1.1 "+strconv.Itoa(status)+" Connection Established\r\n\r\n")
				go io.Copy(up, peer)
				io.Copy(peer, up)
			}))
			defer func() {
				proxy.Close()
				connsMu.Lock()
				defer connsMu.Unlock()
				for _, c := range conns {
					c.Close()
				}
			}()
			c, e := (Dialer{}).Connect(context.Background(), Endpoint{Host: host, Port: port, Pin: pin, Mode: "http-connect", Proxy: proxy.URL}, NewAttempt("pixel", "http-connect", nil))
			if status >= 200 && status < 300 {
				if e != nil {
					t.Fatal(e)
				}
				c.Close()
			} else {
				want := "PROXY_REJECTED"
				if status == 407 {
					want = "PROXY_AUTH_REQUIRED"
				}
				if Code(e) != want {
					t.Fatal(e)
				}
			}
		})
	}
}
func TestProxyConfigAndHeadersAreBounded(t *testing.T) {
	for _, proxy := range []string{"socks5://localhost:1", "http://user:secret@localhost:1", "http://localhost:0", "http://localhost/path", "http://localhost/?secret=1"} {
		if Validate("http-connect", proxy, 0) == nil {
			t.Fatal("invalid/secret proxy accepted", proxy)
		}
	}
	if Validate("unknown", "", 0) == nil || Validate("tls-tcp", "http://localhost:1", 0) == nil {
		t.Fatal("silent transport switch")
	}
	for _, raw := range []string{"HTTP/1.1 200 OK\r\n" + strings.Repeat("X", 20000), "HTTP/9.9 200 OK\r\n\r\n", "HTTP/1.1 200 OK\r\n" + strings.Repeat("X: 1\r\n", 65) + "\r\n"} {
		if _, e := readConnect(bufio.NewReaderSize(strings.NewReader(raw), 4096)); e == nil {
			t.Fatal("unsafe proxy response")
		}
	}
}

func TestHTTPSConnectAndInspectionRejection(t *testing.T) {
	host, port, pin := testTLS(t)
	relay := &testsite.ConnectProxy{Target: net.JoinHostPort(host, strconv.Itoa(port))}
	proxy := httptest.NewTLSServer(relay)
	defer proxy.Close()
	defer relay.Close()
	roots := x509.NewCertPool()
	roots.AddCert(proxy.Certificate())
	endpoint := Endpoint{Host: host, Port: port, Pin: pin, Mode: "http-connect", Proxy: proxy.URL}
	c, e := (Dialer{ProxyTLS: &tls.Config{RootCAs: roots}}).Connect(context.Background(), endpoint, NewAttempt("control", "http-connect", nil))
	if e != nil {
		t.Fatal(e)
	}
	c.Close()
	if _, e = (Dialer{}).Connect(context.Background(), endpoint, NewAttempt("control", "http-connect", nil)); Code(e) != "PROXY_TLS_FAILED" {
		t.Fatal("untrusted outer proxy accepted", e)
	}
	// A proxy-approved connection to a replacement TLS endpoint still cannot
	// replace the original node identity, even with valid TLS at the proxy.
	_, _, differentPin := testTLS(t)
	endpoint.Pin = differentPin
	if _, e = (Dialer{ProxyTLS: &tls.Config{RootCAs: roots}}).Connect(context.Background(), endpoint, NewAttempt("control", "http-connect", nil)); Code(e) != "NODE_IDENTITY_CHANGED" {
		t.Fatal("TLS inspection/replacement accepted", e)
	}
}

func TestRetryRecoveryAndProxyTimeout(t *testing.T) {
	host, port, pin := testTLS(t)
	var attempts int
	d := Dialer{DialContext: func(ctx context.Context, n, a string) (net.Conn, error) {
		attempts++
		if attempts == 1 {
			return nil, syscall.ECONNREFUSED
		}
		return (&net.Dialer{}).DialContext(ctx, n, a)
	}, Wait: func(context.Context, time.Duration) error { return nil }}
	c, e := d.Connect(context.Background(), Endpoint{Host: host, Port: port, Pin: pin, Retries: 1}, NewAttempt("control", "tls-tcp", nil))
	if e != nil || attempts != 2 {
		t.Fatal("service recovery", attempts, e)
	}
	c.Close()
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() }))
	defer proxy.Close()
	started := time.Now()
	_, e = (Dialer{StageTimeout: 50 * time.Millisecond}).Connect(context.Background(), Endpoint{Host: host, Port: port, Pin: pin, Mode: "http-connect", Proxy: proxy.URL}, NewAttempt("control", "http-connect", nil))
	if Code(e) != "PROXY_UNAVAILABLE" || time.Since(started) > time.Second {
		t.Fatal("proxy timeout", e)
	}
}

func TestRetryDeadlineIsNotUserCancellation(t *testing.T) {
	for _, cancelled := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		var final Event
		a := NewAttempt("control", "tls-tcp", func(e Event) { final = e })
		d := Dialer{DialContext: func(context.Context, string, string) (net.Conn, error) { return nil, syscall.ECONNREFUSED }, Wait: func(ctx context.Context, _ time.Duration) error {
			if cancelled {
				cancel()
			}
			<-ctx.Done()
			return ctx.Err()
		}}
		_, e := d.Connect(ctx, Endpoint{Host: "127.0.0.1", Port: 1, Pin: strings.Repeat("0", 64), Retries: 1}, a)
		cancel()
		a.Finish(e)
		want, wantState := "CONNECTION_TIMEOUT", Failed
		if cancelled {
			want, wantState = "NODE_CANCELLED", Cancelled
		}
		if Code(e) != want || final.State != wantState || final.Stage != RetryWait {
			t.Fatal("retry deadline misclassified", e, final)
		}
	}
}
