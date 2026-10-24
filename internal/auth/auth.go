package auth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"time"

	"wolftunnel/internal/identity"
	"wolftunnel/internal/protocol"
	"wolftunnel/internal/storage"
)

var ErrAuth = errors.New("AUTH_FAILED")

type EnrolmentError struct{ Code string }

func (e *EnrolmentError) Error() string { return e.Code }
func (e *EnrolmentError) Unwrap() error { return ErrAuth }
func EnrolmentCode(err error) string {
	var e *EnrolmentError
	if errors.As(err, &e) {
		return e.Code
	}
	return "ENROLMENT_DENIED"
}
func denied(code string) error { return &EnrolmentError{Code: code} }

type Record struct {
	ClientID  string    `json:"client_id"`
	PublicKey []byte    `json:"public_key"`
	Expires   time.Time `json:"expires"`
}
type invitation struct {
	Expires time.Time `json:"expires"`
}
type Store struct {
	mu   sync.Mutex
	root string
}

func Open(root string) (*Store, error) {
	for _, d := range []string{"credentials", "credentials/clients", "credentials/enrolments", "credentials/revoked"} {
		if e := storage.Dir(filepath.Join(root, d)); e != nil {
			return nil, e
		}
	}
	return &Store{root: filepath.Join(root, "credentials")}, nil
}
func (s *Store) clientPath(id string) string { return filepath.Join(s.root, "clients", id+".json") }
func (s *Store) IssueCode() (string, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	code := hex.EncodeToString(b[:])
	return code, storage.Write(filepath.Join(s.root, "enrolments", identity.Fingerprint([]byte(code))+".json"), invitation{Expires: time.Now().Add(30 * 24 * time.Hour)})
}
func (s *Store) Enrol(pub []byte, code string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(pub) != ed25519.PublicKeySize {
		return Record{}, denied("ENROLMENT_INVALID_KEY")
	}
	if code == "" {
		return Record{}, denied("ENROLMENT_CODE_REQUIRED")
	}
	if b, err := hex.DecodeString(code); err != nil || len(b) != 32 {
		return Record{}, denied("ENROLMENT_CODE_INVALID")
	}
	path := filepath.Join(s.root, "enrolments", identity.Fingerprint([]byte(code))+".json")
	var invite invitation
	if err := storage.Read(path, &invite); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Record{}, denied("ENROLMENT_CODE_INVALID")
		}
		return Record{}, denied("ENROLMENT_STORAGE_ERROR")
	}
	if !time.Now().Before(invite.Expires) {
		return Record{}, denied("ENROLMENT_CODE_EXPIRED")
	}
	id := identity.ClientID(pub)
	if _, err := os.Lstat(filepath.Join(s.root, "revoked", id+".json")); err == nil {
		return Record{}, denied("ENROLMENT_CLIENT_REVOKED")
	} else if !errors.Is(err, os.ErrNotExist) {
		return Record{}, denied("ENROLMENT_STORAGE_ERROR")
	}
	if _, err := os.Lstat(s.clientPath(id)); !errors.Is(err, os.ErrNotExist) {
		if err == nil {
			return Record{}, denied("ENROLMENT_ALREADY_ENROLLED")
		}
		return Record{}, denied("ENROLMENT_STORAGE_ERROR")
	}
	entries, err := os.ReadDir(filepath.Join(s.root, "clients"))
	if err != nil {
		return Record{}, denied("ENROLMENT_STORAGE_ERROR")
	}
	if len(entries) >= 1024 {
		return Record{}, denied("ENROLMENT_CAPACITY")
	}
	// Consume first: a crash must never allow reuse of an invitation.
	if os.Remove(path) != nil {
		return Record{}, denied("ENROLMENT_CODE_INVALID")
	}
	r := Record{ClientID: id, PublicKey: append([]byte(nil), pub...), Expires: time.Now().UTC().Add(90 * 24 * time.Hour)}
	if storage.Write(s.clientPath(id), r) != nil {
		return Record{}, denied("ENROLMENT_STORAGE_ERROR")
	}
	return r, nil
}
func (s *Store) Lookup(id string) (Record, error) {
	if len(id) != 64 || !protocol.ValidID(id) {
		return Record{}, ErrAuth
	}
	if _, err := os.Lstat(filepath.Join(s.root, "revoked", id+".json")); !errors.Is(err, os.ErrNotExist) {
		return Record{}, ErrAuth
	}
	var r Record
	if storage.Read(s.clientPath(id), &r) != nil || r.ClientID != id || len(r.PublicKey) != ed25519.PublicKeySize || identity.ClientID(r.PublicKey) != id || !time.Now().Before(r.Expires) {
		return Record{}, ErrAuth
	}
	return r, nil
}
func (s *Store) Verify(binding []byte, typ byte, e protocol.Envelope) (Record, error) {
	r, err := s.Lookup(e.ClientID)
	if err != nil || !ed25519.Verify(r.PublicKey, protocol.SigningBytes(binding, typ, e), e.Signature) {
		return Record{}, ErrAuth
	}
	return r, nil
}
func (s *Store) Renew(id string) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, err := s.Lookup(id)
	if err != nil {
		return Record{}, err
	}
	r.Expires = time.Now().UTC().Add(90 * 24 * time.Hour)
	if err = storage.Write(s.clientPath(id), r); err != nil {
		return Record{}, ErrAuth
	}
	return r, nil
}
func (s *Store) Revoke(id string) error {
	if len(id) != 64 || !protocol.ValidID(id) {
		return ErrAuth
	}
	// Separate marker cannot be overwritten by an in-flight renewal.
	return storage.Write(filepath.Join(s.root, "revoked", id+".json"), struct {
		Revoked bool `json:"revoked"`
	}{true})
}
