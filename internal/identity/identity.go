package identity

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"wolftunnel/internal/storage"
)

type Material struct {
	Key         []byte `json:"key"`
	Certificate []byte `json:"certificate"`
}

func Fingerprint(der []byte) string         { s := sha256.Sum256(der); return hex.EncodeToString(s[:]) }
func ClientID(pub ed25519.PublicKey) string { return Fingerprint(pub) }

// An identity directory is installed with one rename, so a partial identity is
// never treated as a new installation. Existing invalid material is not replaced.
func Node(root string) (tls.Certificate, error) {
	dir := filepath.Join(root, "identity")
	_, err := os.Lstat(dir)
	if errors.Is(err, os.ErrNotExist) {
		tmp, e := os.MkdirTemp(root, ".identity-*")
		if e != nil {
			return tls.Certificate{}, e
		}
		defer os.RemoveAll(tmp)
		pub, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return tls.Certificate{}, e
		}
		serial, e := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if e != nil {
			return tls.Certificate{}, e
		}
		now := time.Now()
		template := &x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "WolfTunnel Node"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, e := x509.CreateCertificate(rand.Reader, template, template, pub, key)
		if e != nil {
			return tls.Certificate{}, e
		}
		pk, e := x509.MarshalPKCS8PrivateKey(key)
		if e != nil {
			return tls.Certificate{}, e
		}
		if e = storage.Write(filepath.Join(tmp, "node.json"), Material{Key: pk, Certificate: der}); e != nil {
			return tls.Certificate{}, e
		}
		if e = os.Rename(tmp, dir); e != nil {
			return tls.Certificate{}, e
		}
	} else if err != nil {
		return tls.Certificate{}, err
	}
	if err = storage.Dir(dir); err != nil {
		return tls.Certificate{}, err
	}
	var m Material
	if err = storage.Read(filepath.Join(dir, "node.json"), &m); err != nil {
		return tls.Certificate{}, err
	}
	pair, err := tls.X509KeyPair(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: m.Certificate}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: m.Key}))
	if err != nil {
		return pair, errors.New("node identity invalid; restore from protected backup or explicitly re-enrol clients after rotation")
	}
	cert, err := x509.ParseCertificate(m.Certificate)
	if err != nil || time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
		return pair, errors.New("node certificate invalid or expired; explicit rotation required")
	}
	if _, ok := cert.PublicKey.(ed25519.PublicKey); !ok {
		return pair, errors.New("incompatible node identity")
	}
	return pair, nil
}

func Client(root string) (ed25519.PrivateKey, error) {
	path := filepath.Join(root, "client-key.json")
	var key []byte
	err := storage.Read(path, &key)
	if errors.Is(err, os.ErrNotExist) {
		_, key, err = ed25519.GenerateKey(rand.Reader)
		if err == nil {
			err = storage.Create(path, key)
			if errors.Is(err, os.ErrExist) {
				err = storage.Read(path, &key)
			}
		}
	}
	if err != nil {
		return nil, err
	}
	if len(key) != ed25519.PrivateKeySize || !ed25519.PrivateKey(key).Equal(ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])) {
		return nil, errors.New("invalid client key")
	}
	return ed25519.PrivateKey(key), nil
}
