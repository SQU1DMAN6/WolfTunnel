package node_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"wolftunnel/internal/node"
	"wolftunnel/internal/nodeclient"
	"wolftunnel/internal/protocol"
	"wolftunnel/internal/storage"
)

func start(t *testing.T, s *node.Server) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(3 * time.Second):
			t.Error("shutdown did not finish")
		}
	})
	return ln.Addr().String()
}
func newClient(t *testing.T, s *node.Server, address string) nodeclient.Client {
	t.Helper()
	c, err := nodeclient.Load(filepath.Join(t.TempDir(), "client"))
	if err != nil {
		t.Fatal(err)
	}
	host, port, _ := net.SplitHostPort(address)
	c.Config.Host = host
	_, _ = fmt.Sscan(port, &c.Config.Port)
	c.Config.Fingerprint = s.Fingerprint
	code, err := s.Auth.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Enrol(context.Background(), code); err != nil {
		t.Fatal(err)
	}
	return c
}

type fetchFunc func(context.Context, string) protocol.Envelope

func (f fetchFunc) Get(c context.Context, s string) protocol.Envelope { return f(c, s) }

func TestLocalClientNodeLifecycleAndIsolation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	s, err := node.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	address := start(t, s)
	c := newClient(t, s, address)
	other := newClient(t, s, address)
	if c.Config.ClientID == other.Config.ClientID {
		t.Fatal("shared identity")
	}
	if _, err = c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err = c.Renew(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Call(context.Background(), protocol.HTTPFetch, protocol.Envelope{RequestID: "unsupported-method", Method: "POST", Target: "https://public.test/"}); nodeclient.ErrorCode(err) != "INVALID_REQUEST" {
		t.Fatal("unsupported method accepted", err)
	}
	out, err := c.Fetch(context.Background(), "policy", "http://127.0.0.1/private?token=do-not-log")
	if err != nil || out.Code != "NODE_POLICY_REJECTED" || out.Status != 0 {
		t.Fatalf("policy: %+v %v", out, err)
	}
	if err = s.Auth.Revoke(c.Config.ClientID); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Ping(context.Background()); nodeclient.ErrorCode(err) != "AUTH_FAILED" {
		t.Fatalf("revoked: %v", err)
	}
	if _, err = other.Ping(context.Background()); err != nil {
		t.Fatal("revocation affected another client", err)
	}
	stolen := other
	stolen.Config.ClientID = c.Config.ClientID
	if _, err = stolen.Ping(context.Background()); nodeclient.ErrorCode(err) != "AUTH_FAILED" {
		t.Fatal("identity substitution accepted", err)
	}
	unverified := other
	unverified.Config.Fingerprint = ""
	if _, err = unverified.Ping(context.Background()); nodeclient.ErrorCode(err) != "NODE_IDENTITY_UNVERIFIED" {
		t.Fatal(err)
	}
	changed := other
	changed.Config.Fingerprint = strings.Repeat("0", 64)
	if _, err = changed.Ping(context.Background()); nodeclient.ErrorCode(err) != "NODE_IDENTITY_CHANGED" {
		t.Fatal(err)
	}
	observed, err := nodeclient.Discover(context.Background(), unverified.Config)
	if err != nil || observed != s.Fingerprint || unverified.Config.Fingerprint != "" {
		t.Fatal("discovery silently changed trust", err)
	}
	_, key, _ := ed25519.GenerateKey(rand.Reader)
	invalid := other
	invalid.Key = key
	if _, err = invalid.Ping(context.Background()); nodeclient.ErrorCode(err) != "AUTH_FAILED" {
		t.Fatal("invalid key accepted", err)
	}
	paths, err := filepath.Glob(filepath.Join(root, "logs", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range paths {
		b, _ := os.ReadFile(p)
		if strings.Contains(string(b), "do-not-log") || strings.Contains(string(b), "/private") {
			t.Fatalf("sensitive log content: %s", b)
		}
	}
}
func TestConcurrentClientsLimitsAndCancellation(t *testing.T) {
	s, err := node.Open(filepath.Join(t.TempDir(), "node"))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan string, 8)
	cancelled := make(chan string, 8)
	s.Fetcher = fetchFunc(func(ctx context.Context, target string) protocol.Envelope {
		entered <- target
		<-ctx.Done()
		cancelled <- target
		return protocol.Envelope{Code: "NODE_CANCELLED"}
	})
	address := start(t, s)
	a := newClient(t, s, address)
	b := newClient(t, s, address)
	var wg sync.WaitGroup
	cancelA := make([]context.CancelFunc, 0, 2)
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		cancelA = append(cancelA, cancel)
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = a.Fetch(ctx, nodeclient.ID(), "http://public.test/a") }()
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("fetch did not start")
		}
	}
	if _, err = a.Fetch(context.Background(), "limited", "http://public.test/limit"); nodeclient.ErrorCode(err) != "RATE_LIMITED" {
		t.Fatalf("per-client concurrency: %v", err)
	}
	ctxB, cancelB := context.WithCancel(context.Background())
	defer cancelB()
	wg.Add(1)
	go func() { defer wg.Done(); _, _ = b.Fetch(ctxB, nodeclient.ID(), "http://public.test/b") }()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("one client starved other client")
	}
	for _, cancel := range cancelA {
		cancel()
	}
	for i := 0; i < 2; i++ {
		select {
		case target := <-cancelled:
			if !strings.HasSuffix(target, "/a") {
				t.Fatal("cancel crossed clients")
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation did not reach node")
		}
	}
	cancelB()
	wg.Wait()
}
func TestRateLimitAndProtocolFailures(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	initial, err := node.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg := initial.Config
	cfg.RatePerMinute = 1
	if err = storage.Write(filepath.Join(root, "config.json"), cfg); err != nil {
		t.Fatal(err)
	}
	s, err := node.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	address := start(t, s)
	c := newClient(t, s, address)
	if _, err = c.Ping(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Ping(context.Background()); nodeclient.ErrorCode(err) != "RATE_LIMITED" {
		t.Fatal("rate limit", err)
	}
	for _, legacy := range []bool{true, false} {
		conn, err := tls.Dial("tcp", address, &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true})
		if err != nil {
			t.Fatal(err)
		}
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		if legacy {
			_ = protocol.Write(conn, protocol.Message{Type: protocol.TypePing})
			m, err := protocol.Read(conn)
			conn.Close()
			if err != nil {
				t.Fatal(err)
			}
			var out protocol.Response
			if err = json.Unmarshal(m.Payload, &out); err != nil || m.Type != protocol.TypePong || out.Status != "unsupported_version" || !strings.Contains(out.State, "upgrade") {
				t.Fatalf("legacy compatibility: %+v %v", out, err)
			}
			continue
		} else {
			_ = protocol.WriteV2(conn, protocol.Hello, protocol.Envelope{RequestID: "bad-version", Versions: []int{99}})
		}
		m, err := protocol.ReadV2(conn)
		conn.Close()
		if err != nil {
			t.Fatal(err)
		}
		out, err := protocol.Decode(m)
		if err != nil || m.Type != protocol.Error || out.Code != "UNSUPPORTED_VERSION" {
			t.Fatalf("compatibility: %+v %v", out, err)
		}
	}
}
func TestIdentityStateRecovery(t *testing.T) {
	for _, change := range []string{"restart", "missing-key", "missing-identity", "corrupt", "permissions", "bad-config"} {
		t.Run(change, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "state")
			s, err := node.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			p := filepath.Join(root, "identity", "node.json")
			switch change {
			case "missing-key":
				err = os.Remove(p)
			case "missing-identity":
				err = os.RemoveAll(filepath.Dir(p))
			case "corrupt":
				err = os.WriteFile(p, []byte("broken"), 0600)
			case "permissions":
				err = os.Chmod(p, 0644)
			case "bad-config":
				err = os.WriteFile(filepath.Join(root, "config.json"), []byte(`{"version":99}`), 0600)
			}
			if err != nil {
				t.Fatal(err)
			}
			again, err := node.Open(root)
			if change == "restart" {
				if err != nil || again.Fingerprint != s.Fingerprint {
					t.Fatalf("identity changed: %v", err)
				}
			} else if err == nil {
				t.Fatal("unsafe state accepted")
			}
		})
	}
}
func TestExpiredCredentialOverTLS(t *testing.T) {
	root := filepath.Join(t.TempDir(), "state")
	s, err := node.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	address := start(t, s)
	c := newClient(t, s, address)
	path := filepath.Join(root, "credentials", "clients", c.Config.ClientID+".json")
	var r struct {
		ClientID  string    `json:"client_id"`
		PublicKey []byte    `json:"public_key"`
		Expires   time.Time `json:"expires"`
	}
	b, _ := os.ReadFile(path)
	if err = json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	r.Expires = time.Now().Add(-time.Minute)
	if err = storage.Write(path, r); err != nil {
		t.Fatal(err)
	}
	if _, err = c.Ping(context.Background()); nodeclient.ErrorCode(err) != "AUTH_FAILED" {
		t.Fatal(err)
	}
}

func TestGlobalFetchLimitAndGracefulShutdown(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	initial, err := node.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	config := initial.Config
	config.MaxFetches = 2
	config.PerClient = 1
	if err = storage.Write(filepath.Join(root, "config.json"), config); err != nil {
		t.Fatal(err)
	}
	s, err := node.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 2)
	finished := make(chan struct{}, 2)
	s.Fetcher = fetchFunc(func(ctx context.Context, _ string) protocol.Envelope {
		entered <- struct{}{}
		<-ctx.Done()
		finished <- struct{}{}
		return protocol.Envelope{Code: "NODE_CANCELLED"}
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	a := newClient(t, s, ln.Addr().String())
	b := newClient(t, s, ln.Addr().String())
	c := newClient(t, s, ln.Addr().String())
	results := make(chan error, 2)
	for _, client := range []nodeclient.Client{a, b} {
		go func(c nodeclient.Client) {
			_, err := c.Fetch(context.Background(), nodeclient.ID(), "http://public.test/")
			results <- err
		}(client)
		select {
		case <-entered:
		case <-time.After(time.Second):
			t.Fatal("worker did not enter")
		}
	}
	if _, err = c.Fetch(context.Background(), "over-capacity", "http://public.test/"); nodeclient.ErrorCode(err) != "NODE_BUSY" {
		t.Fatalf("global limit: %v", err)
	}
	stop()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("shutdown stuck")
	}
	for i := 0; i < 2; i++ {
		select {
		case <-finished:
		case <-time.After(time.Second):
			t.Fatal("fetch not cleaned up")
		}
		select {
		case <-results:
		case <-time.After(time.Second):
			t.Fatal("client stuck on closed node")
		}
	}
}

func TestMalformedClientDoesNotBreakListener(t *testing.T) {
	s, err := node.Open(filepath.Join(t.TempDir(), "node"))
	if err != nil {
		t.Fatal(err)
	}
	address := start(t, s)
	good := newClient(t, s, address)
	for _, kind := range []string{"no-hello", "oversize", "unknown-field", "unauthenticated-fetch"} {
		t.Run(kind, func(t *testing.T) {
			conn, err := tls.Dial("tcp", address, &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Close()
			_ = conn.SetDeadline(time.Now().Add(time.Second))
			switch kind {
			case "no-hello":
				_ = protocol.WriteV2(conn, protocol.PingV2, protocol.Envelope{RequestID: "no-hello"})
			case "oversize":
				var h [21]byte
				copy(h[:12], protocol.IdentifierV2)
				binary.BigEndian.PutUint32(h[12:16], protocol.Magic)
				h[16] = protocol.Hello
				binary.BigEndian.PutUint32(h[17:], protocol.MaxPayloadV2+1)
				_, _ = conn.Write(h[:])
			case "unknown-field":
				payload := []byte(`{"request_id":"invalid","versions":[2],"token":"never-log-this"}`)
				var h [21]byte
				copy(h[:12], protocol.IdentifierV2)
				binary.BigEndian.PutUint32(h[12:16], protocol.Magic)
				h[16] = protocol.Hello
				binary.BigEndian.PutUint32(h[17:], uint32(len(payload)))
				_, _ = conn.Write(append(h[:], payload...))
			case "unauthenticated-fetch":
				_ = protocol.WriteV2(conn, protocol.Hello, protocol.Envelope{RequestID: "hello", Versions: []int{2}})
				if _, err = protocol.ReadV2(conn); err != nil {
					t.Fatal(err)
				}
				_ = protocol.WriteV2(conn, protocol.HTTPFetch, protocol.Envelope{RequestID: "no-auth", Method: "GET", Target: "http://public.test/"})
			}
			m, err := protocol.ReadV2(conn)
			if err != nil {
				t.Fatal(err)
			}
			e, err := protocol.Decode(m)
			if err != nil || m.Type != protocol.Error || e.Code == "" {
				t.Fatalf("malformed client: %+v %v", e, err)
			}
		})
	}
	if _, err = good.Ping(context.Background()); err != nil {
		t.Fatal("malformed client disrupted other clients", err)
	}
}
