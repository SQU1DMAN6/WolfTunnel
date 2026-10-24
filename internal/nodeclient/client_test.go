package nodeclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

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
