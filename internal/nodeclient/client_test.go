package nodeclient

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
	v3 "wolftunnel/internal/protocol/v3"
	"wolftunnel/internal/transport"

	"wolftunnel/internal/identity"

	"wolftunnel/internal/node"
	"wolftunnel/internal/protocol"
	"wolftunnel/internal/storage"
)

func TestRevocationBetweenAuthenticationAndOperation(t *testing.T) {
	s, err := node.Open(filepath.Join(t.TempDir(), "node"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	c, err := Load(filepath.Join(t.TempDir(), "client"))
	if err != nil {
		t.Fatal(err)
	}
	c.Config.Port = ln.Addr().(*net.TCPAddr).Port
	c.Config.Fingerprint = s.Fingerprint
	code, err := s.Auth.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Enrol(ctx, code); err != nil {
		t.Fatal(err)
	}
	deadline, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	session, err := c.connect(deadline)
	if err != nil {
		t.Fatal(err)
	}
	defer session.conn.Close()
	if err = session.send(protocol.Authenticate, protocol.Envelope{RequestID: "auth", ClientID: c.Config.ClientID}, c.Key); err != nil {
		t.Fatal(err)
	}
	if _, err = session.receive(protocol.AuthResult, "auth"); err != nil {
		t.Fatal(err)
	}
	if err = s.Auth.Revoke(c.Config.ClientID); err != nil {
		t.Fatal(err)
	}
	if err = session.send(protocol.PingV2, protocol.Envelope{RequestID: "after-revocation", ClientID: c.Config.ClientID}, c.Key); err != nil {
		t.Fatal(err)
	}
	if _, err = session.receive(protocol.PongV2, "after-revocation"); ErrorCode(err) != "AUTH_FAILED" {
		t.Fatalf("revocation checked only at connection establishment: %v", err)
	}
}

func TestEnrolmentRetryRecoveryAndSecretFreeLogs(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	s, err := node.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	clientRoot := filepath.Join(t.TempDir(), "client")
	c, err := Load(clientRoot)
	if err != nil {
		t.Fatal(err)
	}
	c.Config.Port = ln.Addr().(*net.TCPAddr).Port
	c.Config.Fingerprint = s.Fingerprint
	if err = c.Enrol(ctx, ""); ErrorCode(err) != "ENROLMENT_CODE_REQUIRED" {
		t.Fatal("missing code not diagnosed", ErrorCode(err))
	}
	code, err := s.Auth.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Enrol(ctx, strings.ToUpper("  "+code+"\n")); err != nil {
		t.Fatal(err)
	}
	clientID := c.Config.ClientID
	record, err := s.Auth.Lookup(clientID)
	if err != nil {
		t.Fatal(err)
	}
	record.Expires = time.Now().Add(24 * time.Hour)
	if err = storage.Write(filepath.Join(root, "credentials", "clients", clientID+".json"), record); err != nil {
		t.Fatal(err)
	}
	// Simulate the node approving but the client crashing before it saved config.
	c.Config.ClientID = ""
	c.Config.Expires = time.Time{}
	if err = c.Enrol(ctx, code); err != nil || c.Config.ClientID != clientID {
		t.Fatal("retry with consumed code failed", err)
	}
	if time.Until(c.Config.Expires) < 29*24*time.Hour {
		t.Fatal("credential nearing expiry was not renewed automatically")
	}
	c.Config.ClientID = ""
	if err = c.Enrol(ctx, ""); err != nil || c.Config.ClientID != clientID {
		t.Fatal("key-only recovery failed", err)
	}
	bad := c
	_, bad.Key, _ = ed25519.GenerateKey(rand.Reader)
	bad.Config.ClientID = ""
	if err = bad.Enrol(ctx, code); ErrorCode(err) != "ENROLMENT_CODE_INVALID" {
		t.Fatal("another key recovered consumed approval")
	}
	if err = s.Auth.Revoke(clientID); err != nil {
		t.Fatal(err)
	}
	if err = c.Recover(ctx); ErrorCode(err) != "AUTH_FAILED" {
		t.Fatal("recovery bypassed revocation")
	}
	for _, base := range []string{root, clientRoot} {
		paths, _ := filepath.Glob(filepath.Join(base, "logs", "*.jsonl"))
		if len(paths) == 0 {
			t.Fatal("diagnostic logs missing")
		}
		for _, p := range paths {
			b, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(b), code) || strings.Contains(string(b), "signature\"") || strings.Contains(string(b), "private_key") {
				t.Fatal("secret leaked into diagnostics")
			}
		}
	}
	if safeCode(code) != "PROTOCOL_ERROR" {
		t.Fatal("untrusted diagnostic was accepted")
	}
}
func TestClientStatePreservationAndCorruption(t *testing.T) {
	root := filepath.Join(t.TempDir(), "client")
	c, err := Load(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Save(root); err != nil {
		t.Fatal(err)
	}
	again, err := Load(root)
	if err != nil || !c.Key.Equal(again.Key) {
		t.Fatal("client identity not preserved", err)
	}
	for _, data := range []string{"null", "{}", `{"host":"127.0.0.1","token":"unexpected"}`} {
		if err = os.WriteFile(filepath.Join(root, "config.json"), []byte(data), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err = Load(root); err == nil {
			t.Fatal("invalid client configuration accepted")
		}
	}
	if err = c.Save(root); err != nil {
		t.Fatal(err)
	}
	c.Config.ClientID = identity.ClientID(c.Key.Public().(ed25519.PublicKey))
	if err = c.Save(root); err != nil {
		t.Fatal(err)
	}
	if err = os.Remove(filepath.Join(root, "client-key.json")); err != nil {
		t.Fatal(err)
	}
	if _, err = Load(root); err == nil {
		t.Fatal("missing enrolled key silently regenerated")
	}
}

type cancelledFetch struct{ entered chan struct{} }

func (f cancelledFetch) Get(ctx context.Context, _ string) protocol.Envelope {
	close(f.entered)
	<-ctx.Done()
	return protocol.Envelope{Code: "NODE_CANCELLED"}
}
func TestUnsignedCancellationRejected(t *testing.T) {
	s, err := node.Open(filepath.Join(t.TempDir(), "node"))
	if err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{})
	s.Fetcher = cancelledFetch{entered: entered}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	c, err := Load(filepath.Join(t.TempDir(), "client"))
	if err != nil {
		t.Fatal(err)
	}
	c.Config.Port = ln.Addr().(*net.TCPAddr).Port
	c.Config.Fingerprint = s.Fingerprint
	code, err := s.Auth.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Enrol(ctx, code); err != nil {
		t.Fatal(err)
	}
	deadline, stop := context.WithTimeout(ctx, 2*time.Second)
	defer stop()
	session, err := c.connect(deadline)
	if err != nil {
		t.Fatal(err)
	}
	defer session.conn.Close()
	if err = session.send(protocol.Authenticate, protocol.Envelope{RequestID: "auth", ClientID: c.Config.ClientID}, c.Key); err != nil {
		t.Fatal(err)
	}
	if _, err = session.receive(protocol.AuthResult, "auth"); err != nil {
		t.Fatal(err)
	}
	if err = session.send(protocol.HTTPFetch, protocol.Envelope{RequestID: "running", ClientID: c.Config.ClientID, Method: "GET", Target: "https://public.test"}, c.Key); err != nil {
		t.Fatal(err)
	}
	select {
	case <-entered:
	case <-deadline.Done():
		t.Fatal("fetch did not start")
	}
	if err = session.send(protocol.Cancel, protocol.Envelope{RequestID: "running", ClientID: c.Config.ClientID}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err = session.receive(protocol.HTTPResult, "running"); ErrorCode(err) != "AUTH_FAILED" {
		t.Fatalf("unsigned CANCEL accepted: %v", err)
	}
}

func TestPastedEndpointsAndSafeNetworkDetails(t *testing.T) {
	for _, raw := range []string{"127.0.0.1:13300", " https://node.example:13300/ ", "[::1]:13300"} {
		h, p, e := NormalizeEndpoint(raw, 999)
		if e != nil || h == "" || p != 13300 {
			t.Fatal(raw, h, p, e)
		}
	}
	for _, raw := range []string{"host:bad", "https://user:secret@node.example/", "https://node.example/private?token=secret"} {
		if _, _, e := NormalizeEndpoint(raw, 13300); e == nil {
			t.Fatal("malformed endpoint accepted")
		}
	}
	err := &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}
	if !strings.Contains(FailureDetail(err), "refused") || strings.Contains(FailureDetail(errors.New("secret-token")), "secret-token") {
		t.Fatal("network diagnostics unsafe or unhelpful")
	}
}

func TestConnectionRefusalReachesStableLog(t *testing.T) {
	root := filepath.Join(t.TempDir(), "client")
	c, e := Load(root)
	if e != nil {
		t.Fatal(e)
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	c.Config.Port = port
	c.Config.Fingerprint = strings.Repeat("a", 64)
	if e = c.Enrol(context.Background(), strings.Repeat("b", 64)); e == nil {
		t.Fatal("missing connection failure")
	}
	data, e := os.ReadFile(filepath.Join(root, "logs", "client.log"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(data), "refused") || !strings.Contains(string(data), "enrolment_result") || strings.Contains(string(data), strings.Repeat("b", 64)) {
		t.Fatal("failure absent or unsafe in stable log", string(data))
	}
}

type discoveryConnector struct{}

func (discoveryConnector) Connect(ctx context.Context, e transport.Endpoint, a *transport.Attempt) (*tls.Conn, error) {
	e.Discovery = true
	return (transport.Dialer{}).Connect(ctx, e, a)
}
func TestConnectorCannotBypassApplicationPinAndProtocolStage(t *testing.T) {
	s, e := node.Open(filepath.Join(t.TempDir(), "node"))
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	defer func() { cancel(); <-done }()
	c, e := Load(filepath.Join(t.TempDir(), "client"))
	if e != nil {
		t.Fatal(e)
	}
	c.Config.Port = ln.Addr().(*net.TCPAddr).Port
	c.Config.Fingerprint = strings.Repeat("0", 64)
	c.Connector = discoveryConnector{}
	if conn, e := c.dial(ctx, c.attempt("control")); ErrorCode(e) != "NODE_IDENTITY_CHANGED" {
		if conn != nil {
			conn.Close()
		}
		t.Fatal("connector bypassed application pin", e)
	}
	c.Config.Fingerprint = s.Fingerprint
	code, _ := s.Auth.IssueCode()
	if e = c.Enrol(ctx, code); e != nil {
		t.Fatal(e)
	}
	// A node without a renderer fails at browser initialization, not TCP/TLS.
	_, e = c.OpenBrowser(ctx, BrowserOptions{})
	var failure *transport.Failure
	if !errors.As(e, &failure) || failure.Stage != transport.InitialisingBrowser || ErrorCode(e) != "BROWSER_UNAVAILABLE" {
		t.Fatal("protocol failure stage", e, failure)
	}
}

func TestInvalidVersionAndInterruptedPixelStream(t *testing.T) {
	s, e := node.Open(filepath.Join(t.TempDir(), "node"))
	if e != nil {
		t.Fatal(e)
	}
	for _, mode := range []string{"version", "pixel", "stalled-pixel"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 18*time.Second)
			defer cancel()
			ln, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			defer ln.Close()
			serverDone := make(chan struct{})
			go func() {
				defer close(serverDone)
				raw, e := ln.Accept()
				if e != nil {
					return
				}
				defer raw.Close()
				peer := tls.Server(raw, s.TLSConfig())
				peer.SetDeadline(time.Now().Add(17 * time.Second))
				stop := context.AfterFunc(ctx, func() { raw.Close() })
				defer stop()
				if peer.Handshake() != nil {
					return
				}
				if mode == "version" {
					if _, err := v3.Read(peer, false); err != nil {
						return
					}
					var buf bytes.Buffer
					v3.Write(&buf, v3.Frame{Type: v3.NodeIdentity, Sequence: 1, Payload: v3.Encode(v3.Control{Version: 3, Fingerprint: s.Fingerprint})})
					p := buf.Bytes()
					p[12] = 99
					peer.Write(p)
				} else if mode == "stalled-pixel" {
					<-ctx.Done()
				} else {
					time.Sleep(30 * time.Millisecond)
				}
			}()
			c, e := Load(filepath.Join(t.TempDir(), "client"))
			if e != nil {
				t.Fatal(e)
			}
			c.Config.Port = ln.Addr().(*net.TCPAddr).Port
			c.Config.Fingerprint = s.Fingerprint
			c.Config.ClientID = "fixture"
			if mode == "version" {
				_, e = c.OpenBrowser(ctx, BrowserOptions{})
				var f *transport.Failure
				if ErrorCode(e) != "UNSUPPORTED_VERSION" || !errors.As(e, &f) || f.Stage != transport.Negotiating {
					t.Fatal("version diagnosis", e, f)
				}
			} else {
				conn, e := c.dial(ctx, c.attempt("pixel"))
				if e != nil {
					t.Fatal(e)
				}
				defer conn.Close()
				b := &Browser{ctx: ctx, cancel: cancel, pixel: &browserPeer{conn: conn}, Events: make(chan BrowserEvent, 1)}
				at := time.Now()
				b.wg.Add(1)
				go b.readPixels()
				b.wg.Wait()
				if mode == "stalled-pixel" && (time.Since(at) < 14*time.Second || time.Since(at) > 17*time.Second) {
					t.Fatal("pixel stall deadline is not bounded at 15 seconds")
				}
				select {
				case event := <-b.Events:
					if event.Control.Code != "PIXEL_STREAM_FAILED" {
						t.Fatal(event.Control.Code)
					}
				default:
					t.Fatal("display failure not reported")
				}
			}
			<-serverDone
		})
	}
}

func TestNodeRestartUsesSavedCredentialsAndFreshConnection(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	s, e := node.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	c, e := Load(filepath.Join(t.TempDir(), "client"))
	if e != nil {
		t.Fatal(e)
	}
	var address string
	for n := 0; n < 2; n++ {
		bind := "127.0.0.1:0"
		if address != "" {
			bind = address
		}
		ln, e := net.Listen("tcp", bind)
		if e != nil {
			t.Fatal(e)
		}
		address = ln.Addr().String()
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- s.Serve(ctx, ln) }()
		c.Config.Port = ln.Addr().(*net.TCPAddr).Port
		c.Config.Fingerprint = s.Fingerprint
		if n == 0 {
			code, _ := s.Auth.IssueCode()
			if e = c.Enrol(ctx, code); e != nil {
				cancel()
				t.Fatal(e)
			}
		}
		if _, e = c.Ping(ctx); e != nil {
			cancel()
			t.Fatal(e)
		}
		cancel()
		if e = <-done; e != nil {
			t.Fatal(e)
		}
		if n == 0 {
			s, e = node.Open(root)
			if e != nil {
				t.Fatal(e)
			}
		}
	}
	// No transparent replay of a prior session or browser action after restart.
	if _, e = c.Ping(context.Background()); ErrorCode(e) != "CONNECTION_REFUSED" {
		t.Fatal(e)
	}
}

func TestAuthenticationTimeoutRetainsStageAndCause(t *testing.T) {
	s, e := node.Open(filepath.Join(t.TempDir(), "node"))
	if e != nil {
		t.Fatal(e)
	}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		raw, err := ln.Accept()
		if err != nil {
			return
		}
		defer raw.Close()
		peer := tls.Server(raw, s.TLSConfig())
		peer.SetDeadline(time.Now().Add(2 * time.Second))
		m, err := protocol.ReadV2(peer)
		if err != nil {
			return
		}
		p, err := protocol.Decode(m)
		if err != nil {
			return
		}
		if protocol.WriteV2(peer, protocol.NodeIdentity, protocol.Envelope{RequestID: p.RequestID, Version: 2, Fingerprint: s.Fingerprint}) != nil {
			return
		}
		protocol.ReadV2(peer)
		var pbyte [1]byte
		peer.Read(pbyte[:])
	}()
	c, e := Load(filepath.Join(t.TempDir(), "client"))
	if e != nil {
		t.Fatal(e)
	}
	c.Config.Port = ln.Addr().(*net.TCPAddr).Port
	c.Config.Fingerprint = s.Fingerprint
	c.Config.ClientID = "fixture"
	c.Config.Timeout = time.Second
	_, e = c.Ping(context.Background())
	var f *transport.Failure
	if ErrorCode(e) != "NODE_TIMEOUT" || !errors.As(e, &f) || f.Stage != transport.Authenticating {
		t.Fatal("auth timeout diagnosis lost", e, f)
	}
	<-done
}
