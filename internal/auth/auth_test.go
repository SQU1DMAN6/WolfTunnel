package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wolftunnel/internal/identity"
	"wolftunnel/internal/protocol"
	"wolftunnel/internal/storage"
)

func TestEnrolAuthenticateRenewRevoke(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	if _, err = s.Enrol(pub, strings.Repeat("0", 64)); err == nil {
		t.Fatal("enrolment enabled without code")
	}
	code, err := s.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.Enrol(pub, code)
	if err != nil {
		t.Fatal(err)
	}
	pub2, _, _ := ed25519.GenerateKey(rand.Reader)
	if _, err = s.Enrol(pub2, code); err == nil {
		t.Fatal("reused single-use code")
	}
	binding := make([]byte, 32)
	e := protocol.Envelope{RequestID: "id", ClientID: r.ClientID}
	e.Signature = ed25519.Sign(key, protocol.SigningBytes(binding, protocol.Authenticate, e))
	if _, err = s.Verify(binding, protocol.Authenticate, e); err != nil {
		t.Fatal(err)
	}
	otherBinding := make([]byte, 32)
	otherBinding[0] = 1
	if _, err = s.Verify(otherBinding, protocol.Authenticate, e); err == nil {
		t.Fatal("replay across TLS channels")
	}
	if _, err = s.Verify(binding, protocol.PingV2, e); err == nil {
		t.Fatal("message substitution")
	}
	e.RequestID = "tampered"
	if _, err = s.Verify(binding, protocol.Authenticate, e); err == nil {
		t.Fatal("request ID substitution")
	}
	renewed, err := s.Renew(r.ClientID)
	if err != nil || !renewed.Expires.After(r.Expires) {
		t.Fatal("renewal", err)
	}
	if err = s.Revoke(r.ClientID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Lookup(r.ClientID); err == nil {
		t.Fatal("revoked credential accepted")
	}
	if _, err = s.Renew(r.ClientID); err == nil {
		t.Fatal("revoked credential renewed")
	}
	if _, err = s.Lookup("../../identity"); err == nil {
		t.Fatal("path traversal")
	}
}
func TestExpiredAndInvalidCredentials(t *testing.T) {
	root := t.TempDir()
	s, _ := Open(root)
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	code, _ := s.IssueCode()
	path := filepath.Join(s.root, "enrolments", identity.Fingerprint([]byte(code))+".json")
	if err := storage.Write(path, invitation{Expires: time.Now().Add(-time.Second)}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Enrol(pub, code); err == nil {
		t.Fatal("expired code accepted")
	}
	code, _ = s.IssueCode()
	r, err := s.Enrol(pub, code)
	if err != nil {
		t.Fatal(err)
	}
	r.Expires = time.Now().Add(-time.Second)
	if err = storage.Write(s.clientPath(r.ClientID), r); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Lookup(r.ClientID); err == nil {
		t.Fatal("expired credential accepted")
	}
	if _, err = s.Lookup(strings.Repeat("0", 64)); err == nil {
		t.Fatal("missing credential accepted")
	}
}

func TestEnrolmentFailureReasons(t *testing.T) {
	s, err := Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pub, _, _ := ed25519.GenerateKey(rand.Reader)
	for _, tc := range []struct{ code, want string }{{"", "ENROLMENT_CODE_REQUIRED"}, {"short", "ENROLMENT_CODE_INVALID"}, {strings.Repeat("0", 64), "ENROLMENT_CODE_INVALID"}} {
		_, err := s.Enrol(pub, tc.code)
		if EnrolmentCode(err) != tc.want {
			t.Fatalf("wanted %s, got %s", tc.want, EnrolmentCode(err))
		}
	}
	code, _ := s.IssueCode()
	p := filepath.Join(s.root, "enrolments", identity.Fingerprint([]byte(code))+".json")
	if err = storage.Write(p, invitation{Expires: time.Now().Add(-time.Minute)}); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Enrol(pub, code); EnrolmentCode(err) != "ENROLMENT_CODE_EXPIRED" {
		t.Fatal("expiry not diagnosed")
	}
	code, _ = s.IssueCode()
	record, err := s.Enrol(pub, code)
	if err != nil {
		t.Fatal(err)
	}
	fresh, _ := s.IssueCode()
	if _, err = s.Enrol(pub, fresh); EnrolmentCode(err) != "ENROLMENT_ALREADY_ENROLLED" {
		t.Fatal("existing client not diagnosed")
	}
	if err = s.Revoke(record.ClientID); err != nil {
		t.Fatal(err)
	}
	if _, err = s.Enrol(pub, fresh); EnrolmentCode(err) != "ENROLMENT_CLIENT_REVOKED" {
		t.Fatal("revocation not diagnosed")
	}
}
