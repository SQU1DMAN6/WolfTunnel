package nodeclient

import (
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"strings"
	"time"

	"wolftunnel/internal/protocol"
)

type Config struct {
	Host         string `json:"host"`
	Port         int    `json:"port"`
	Fingerprint  string `json:"fingerprint"`
	Token        string `json:"token"`
	SearchEngine string `json:"search_engine"`
}

type Result struct {
	RequestID  string `json:"request_id,omitempty"`
	Status     string `json:"status"`
	State      string `json:"state"`
	CanRespond bool   `json:"can_respond"`
}

func (c Config) Address() string { return net.JoinHostPort(c.Host, fmt.Sprint(c.Port)) }

func (c Config) validate() error {
	if strings.TrimSpace(c.Host) == "" {
		return fmt.Errorf("Wolf Node host is required")
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if len(strings.TrimSpace(c.Token)) < 32 {
		return fmt.Errorf("authentication token must be at least 32 characters")
	}
	fp := normaliseFingerprint(c.Fingerprint)
	if len(fp) != 64 {
		return fmt.Errorf("certificate SHA-256 fingerprint must contain 64 hexadecimal characters")
	}
	if _, err := hex.DecodeString(fp); err != nil {
		return fmt.Errorf("invalid certificate fingerprint")
	}
	return nil
}

func normaliseFingerprint(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, ":", "")
	s = strings.ReplaceAll(s, " ", "")
	return s
}

func connect(c Config) (*tls.Conn, error) {
	if err := c.validate(); err != nil {
		return nil, err
	}
	want, _ := hex.DecodeString(normaliseFingerprint(c.Fingerprint))
	d := &net.Dialer{Timeout: 5 * time.Second}
	tlsCfg := &tls.Config{
		MinVersion: tls.VersionTLS12,
		// Verification is performed below using an explicitly configured SHA-256 pin.
		// This is not an unverified connection: a mismatch is rejected before use.
		InsecureSkipVerify: true, //nolint:gosec
		VerifyConnection: func(cs tls.ConnectionState) error {
			if len(cs.PeerCertificates) == 0 {
				return fmt.Errorf("Wolf Node supplied no certificate")
			}
			got := sha256.Sum256(cs.PeerCertificates[0].Raw)
			if subtle.ConstantTimeCompare(got[:], want) != 1 {
				return fmt.Errorf("Wolf Node certificate fingerprint mismatch")
			}
			return nil
		},
	}
	conn, err := tls.DialWithDialer(d, "tcp", c.Address(), tlsCfg)
	if err != nil {
		return nil, err
	}
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	return conn, nil
}

func Ping(c Config) (Result, error) {
	conn, err := connect(c)
	if err != nil {
		return Result{}, err
	}
	defer conn.Close()
	p, _ := json.Marshal(protocol.Ping{Token: c.Token})
	if err := protocol.Write(conn, protocol.Message{Type: protocol.TypePing, Payload: p}); err != nil {
		return Result{}, err
	}
	m, err := protocol.Read(conn)
	if err != nil {
		return Result{}, err
	}
	if m.Type != protocol.TypePong {
		return Result{}, fmt.Errorf("unexpected response message type %d", m.Type)
	}
	var r Result
	if err := json.Unmarshal(m.Payload, &r); err != nil {
		return Result{}, err
	}
	if r.Status != "ok" {
		return r, fmt.Errorf("Wolf Node: %s", r.State)
	}
	return r, nil
}

func SendRequest(c Config, id, method, target string) (Result, error) {
	conn, err := connect(c)
	if err != nil {
		return Result{}, err
	}
	defer conn.Close()
	req := protocol.Request{RequestID: id, Token: c.Token, Method: method, Target: target}
	p, err := json.Marshal(req)
	if err != nil {
		return Result{}, err
	}
	if err := protocol.Write(conn, protocol.Message{Type: protocol.TypeRequest, Payload: p}); err != nil {
		return Result{}, err
	}
	m, err := protocol.Read(conn)
	if err != nil {
		return Result{}, err
	}
	if m.Type != protocol.TypeAck {
		return Result{}, fmt.Errorf("unexpected response message type %d", m.Type)
	}
	var r Result
	if err := json.Unmarshal(m.Payload, &r); err != nil {
		return Result{}, err
	}
	if r.Status != "ok" {
		return r, fmt.Errorf("Wolf Node: %s", r.State)
	}
	return r, nil
}
