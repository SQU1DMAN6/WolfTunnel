package nodeclient

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wolftunnel/internal/audit"
	"wolftunnel/internal/enrolment"
	"wolftunnel/internal/identity"
	"wolftunnel/internal/protocol"
	v3 "wolftunnel/internal/protocol/v3"
	"wolftunnel/internal/storage"
)

const Google = "https://www.google.com/search?q={query}"
const DuckDuckGo = "https://duckduckgo.com/?q={query}"

type Config struct {
	Host           string        `json:"host"`
	Port           int           `json:"port"`
	Fingerprint    string        `json:"fingerprint"`
	SearchTemplate string        `json:"search_template"`
	Timeout        time.Duration `json:"timeout"`
	Details        bool          `json:"details"`
	ClientID       string        `json:"client_id,omitempty"`
	Expires        time.Time     `json:"expires,omitzero"`
}

func DefaultConfig() Config {
	return Config{Host: "127.0.0.1", Port: 13300, SearchTemplate: DuckDuckGo, Timeout: 20 * time.Second, Details: false}
}
func (c Config) Address() string { return net.JoinHostPort(c.Host, fmt.Sprint(c.Port)) }
func (c Config) Validate() error {
	if c.Host == "" || strings.ContainsAny(c.Host, "/ \t\r\n?@#\\") || c.Port < 1 || c.Port > 65535 || c.Timeout < time.Second || c.Timeout > time.Minute {
		return &Error{Code: "INVALID_CONFIGURATION"}
	}
	if c.SearchTemplate != Google && c.SearchTemplate != DuckDuckGo {
		return &Error{Code: "INVALID_SEARCH_TEMPLATE"}
	}
	if c.Fingerprint != "" {
		if b, err := hex.DecodeString(c.Fingerprint); err != nil || len(b) != 32 {
			return &Error{Code: "INVALID_FINGERPRINT"}
		}
	}
	return nil
}

type Client struct {
	Config Config
	Key    ed25519.PrivateKey
	Log    *audit.Logger
}
type Error struct {
	Code  string
	Cause error
}

func (e *Error) Error() string { return e.Code }
func (e *Error) Unwrap() error { return e.Cause }
func ErrorCode(err error) string {
	if err == nil {
		return ""
	}
	if errors.Is(err, enrolment.ErrInvitation) {
		return "INVALID_INVITATION"
	}
	if errors.Is(err, protocol.ErrVersion) || errors.Is(err, v3.ErrVersion) {
		return "UNSUPPORTED_VERSION"
	}
	if errors.Is(err, protocol.ErrPayload) || errors.Is(err, protocol.ErrInvalidHeader) || errors.Is(err, v3.ErrFrame) {
		return "PROTOCOL_ERROR"
	}
	var e *Error
	if errors.As(err, &e) {
		return e.Code
	}
	if errors.Is(err, context.Canceled) {
		return "NODE_CANCELLED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "NODE_TIMEOUT"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "NODE_TIMEOUT"
	}
	return "NODE_UNREACHABLE"
}
func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure randomness unavailable")
	}
	return hex.EncodeToString(b[:])
}
func Load(root string) (Client, error) {
	if err := storage.Dir(root); err != nil {
		return Client{}, err
	}
	c := Client{}
	err := storage.Read(filepath.Join(root, "config.json"), &c.Config)
	if errors.Is(err, os.ErrNotExist) {
		c.Config = DefaultConfig()
	}
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Client{}, err
	}
	host, port, normalizeErr := NormalizeEndpoint(c.Config.Host, c.Config.Port)
	if normalizeErr != nil {
		return Client{}, normalizeErr
	}
	c.Config.Host, c.Config.Port = host, port
	if c.Config.ClientID != "" {
		if _, err := os.Lstat(filepath.Join(root, "client-key.json")); err != nil {
			return Client{}, &Error{Code: "CLIENT_KEY_MISSING"}
		}
	}
	key, err := identity.Client(root)
	if err != nil {
		return Client{}, err
	}
	c.Key = key
	if err = c.Config.Validate(); err != nil {
		return Client{}, err
	}
	if c.Config.ClientID != "" && c.Config.ClientID != identity.ClientID(key.Public().(ed25519.PublicKey)) {
		return Client{}, &Error{Code: "CLIENT_IDENTITY_MISMATCH"}
	}
	logDir, err := storage.ClientLogDir(root)
	if err != nil {
		return Client{}, err
	}
	c.Log, err = audit.NewClient(logDir, 7*24*time.Hour)
	if err != nil {
		return Client{}, err
	}
	if err = c.Save(root); err != nil {
		return Client{}, err
	}
	c.trace("client_ready", "", "ready", time.Now())
	return c, nil
}
func (c Client) trace(event, id, code string, start time.Time) {
	if c.Log != nil {
		endpoint := "[invalid]"
		if c.Config.Validate() == nil {
			endpoint = c.Config.Address()
		}
		c.Log.Log(audit.Entry{Component: "client", Event: event, ID: id, Endpoint: endpoint, ClientID: c.Config.ClientID, Code: code, DurationMS: time.Since(start).Milliseconds()})
	}
}
func (c Client) Save(root string) error {
	if err := c.Config.Validate(); err != nil {
		return err
	}
	return storage.Write(filepath.Join(root, "config.json"), c.Config)
}

// Discover retrieves only the public TLS identity. It never authenticates,
// enrols or saves a fingerprint; the caller must compare it out of band.
func Discover(ctx context.Context, c Config) (string, error) {
	if err := c.Validate(); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true}} // public discovery only
	raw, err := d.DialContext(ctx, "tcp", c.Address())
	if err != nil {
		return "", &Error{Code: ErrorCode(err)}
	}
	defer raw.Close()
	return identity.Fingerprint(raw.(*tls.Conn).ConnectionState().PeerCertificates[0].Raw), nil
}

type session struct {
	conn    *tls.Conn
	binding []byte
	mu      sync.Mutex
	log     *audit.Logger
}

func messageName(typ byte) string {
	names := map[byte]string{protocol.Hello: "HELLO", protocol.NodeIdentity: "NODE_IDENTITY", protocol.EnrolmentRequest: "ENROLMENT_REQUEST", protocol.EnrolmentResult: "ENROLMENT_RESULT", protocol.Authenticate: "AUTHENTICATE", protocol.AuthResult: "AUTH_RESULT", protocol.HTTPFetch: "HTTP_FETCH", protocol.HTTPResult: "HTTP_RESULT", protocol.PingV2: "PING", protocol.PongV2: "PONG", protocol.Cancel: "CANCEL", protocol.Error: "ERROR", protocol.Renew: "RENEW", protocol.RenewResult: "RENEW_RESULT"}
	if name := names[typ]; name != "" {
		return name
	}
	return "UNKNOWN"
}

func (s *session) send(typ byte, e protocol.Envelope, key ed25519.PrivateKey) error {
	if key != nil {
		e.Signature = ed25519.Sign(key, protocol.SigningBytes(s.binding, typ, e))
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	err := protocol.WriteV2(s.conn, typ, e)
	if s.log != nil {
		s.log.Log(audit.Entry{Component: "client", Event: "protocol_send", ID: e.RequestID, Status: messageName(typ), Code: ErrorCode(err)})
	}
	return err
}
func (s *session) receive(typ byte, id string) (protocol.Envelope, error) {
	m, err := protocol.ReadV2(s.conn)
	if err != nil {
		code := "NODE_UNREACHABLE"
		if errors.Is(err, protocol.ErrVersion) {
			code = "UNSUPPORTED_VERSION"
		} else if errors.Is(err, protocol.ErrPayload) || errors.Is(err, protocol.ErrInvalidHeader) {
			code = "PROTOCOL_ERROR"
		}
		if s.log != nil {
			s.log.Log(audit.Entry{Component: "client", Event: "protocol_receive_failed", ID: id, Status: messageName(typ), Code: code})
		}
		return protocol.Envelope{}, &Error{Code: code}
	}
	e, err := protocol.Decode(m)
	if err != nil || e.RequestID != id {
		return e, &Error{Code: "PROTOCOL_ERROR"}
	}
	e.Code = safeCode(e.Code)
	if s.log != nil {
		s.log.Log(audit.Entry{Component: "client", Event: "protocol_receive", ID: e.RequestID, Status: messageName(m.Type), Code: e.Code})
	}
	if m.Type == protocol.Error {
		if e.Code == "" {
			return e, &Error{Code: "PROTOCOL_ERROR"}
		}
		return e, &Error{Code: e.Code}
	}
	if m.Type != typ {
		return e, &Error{Code: "PROTOCOL_ERROR"}
	}
	return e, nil
}

// Never log or display arbitrary peer-supplied error text as a diagnostic code.
func safeCode(code string) string {
	switch code {
	case "", "ENROLMENT_DENIED", "ENROLMENT_CODE_REQUIRED", "ENROLMENT_CODE_INVALID", "ENROLMENT_CODE_EXPIRED", "ENROLMENT_ALREADY_ENROLLED", "ENROLMENT_CLIENT_REVOKED", "ENROLMENT_STORAGE_ERROR", "ENROLMENT_CAPACITY", "ENROLMENT_INVALID_KEY", "AUTH_FAILED", "AUTH_REQUIRED", "RATE_LIMITED", "PROTOCOL_ERROR", "UNSUPPORTED_VERSION", "INVALID_REQUEST", "NODE_BUSY", "NODE_TIMEOUT", "NODE_DNS_ERROR", "NODE_POLICY_REJECTED", "NODE_RESPONSE_TOO_LARGE", "NODE_REDIRECT_LIMIT", "NODE_CANCELLED", "NODE_FETCH_ERROR":
		return code
	default:
		return "PROTOCOL_ERROR"
	}
}
func (c Client) connect(ctx context.Context) (*session, error) {
	start := time.Now()
	c.trace("tls_connect_start", "", "pending", start)
	if err := c.Config.Validate(); err != nil {
		return nil, err
	}
	if c.Config.Fingerprint == "" {
		return nil, &Error{Code: "NODE_IDENTITY_UNVERIFIED"}
	}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 || identity.Fingerprint(cs.PeerCertificates[0].Raw) != c.Config.Fingerprint {
			return &Error{Code: "NODE_IDENTITY_CHANGED"}
		}
		cert := cs.PeerCertificates[0]
		if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
			return &Error{Code: "NODE_IDENTITY_EXPIRED"}
		}
		return nil
	}}}
	raw, err := d.DialContext(ctx, "tcp", c.Config.Address())
	if err != nil {
		c.trace("tls_connect_failed", "", ErrorCode(err), start)
		return nil, &Error{Code: ErrorCode(err), Cause: err}
	}
	conn := raw.(*tls.Conn)
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	}
	state := conn.ConnectionState()
	binding, err := state.ExportKeyingMaterial("WolfTunnel-v2", nil, 32)
	if err != nil {
		conn.Close()
		return nil, err
	}
	c.trace("tls_identity_verified", "", "ok", start)
	s := &session{conn: conn, binding: binding, log: c.Log}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	id := ID()
	if err = s.send(protocol.Hello, protocol.Envelope{RequestID: id, Versions: []int{2}}, nil); err != nil {
		conn.Close()
		return nil, &Error{Code: "NODE_UNREACHABLE"}
	}
	e, err := s.receive(protocol.NodeIdentity, id)
	if err != nil {
		conn.Close()
		return nil, err
	}
	if e.Version != 2 || e.Fingerprint != c.Config.Fingerprint {
		conn.Close()
		return nil, &Error{Code: "PROTOCOL_ERROR"}
	}
	c.trace("protocol_negotiated", id, "ok", start)
	return s, nil
}
func (c *Client) Enrol(ctx context.Context, code string) (result error) {
	start := time.Now()
	defer func() { c.reportOperation("enrolment_result", start, result) }()
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(code)), "wolftunnel://") {
		inv, err := enrolment.Parse(code)
		if err != nil {
			return &Error{Code: "INVALID_INVITATION"}
		}
		if c.Config.Fingerprint != "" && c.Config.Fingerprint != inv.Fingerprint {
			return &Error{Code: "NODE_IDENTITY_CHANGED"}
		}
		if c.Config.Host != inv.Host || c.Config.Port != inv.Port {
			c.Config.ClientID = ""
			c.Config.Expires = time.Time{}
		}
		c.Config.Host, c.Config.Port, c.Config.Fingerprint = inv.Host, inv.Port, inv.Fingerprint
		code = inv.Code
	}
	code = strings.ToLower(strings.TrimSpace(code))
	if len(c.Key) != ed25519.PrivateKeySize {
		return &Error{Code: "INVALID_CLIENT_KEY"}
	}
	if c.Config.ClientID != "" || code == "" {
		if err := c.Recover(ctx); err == nil {
			return c.renewIfNeeded(ctx)
		} else if ErrorCode(err) != "AUTH_FAILED" || (c.Config.ClientID != "" && code == "") {
			return err
		}
	}
	if code == "" {
		c.trace("enrolment_failed", "", "ENROLMENT_CODE_REQUIRED", start)
		return &Error{Code: "ENROLMENT_CODE_REQUIRED"}
	}
	if b, err := hex.DecodeString(code); err != nil || len(b) != 32 {
		return &Error{Code: "ENROLMENT_CODE_INVALID"}
	}
	ctx, cancel := context.WithTimeout(ctx, c.Config.Timeout)
	defer cancel()
	s, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer s.conn.Close()
	stop := context.AfterFunc(ctx, func() { s.conn.Close() })
	defer stop()
	id := ID()
	if err = s.send(protocol.EnrolmentRequest, protocol.Envelope{RequestID: id, PublicKey: c.Key.Public().(ed25519.PublicKey), Code: code}, nil); err != nil {
		return &Error{Code: "NODE_UNREACHABLE"}
	}
	out, err := s.receive(protocol.EnrolmentResult, id)
	if err != nil {
		c.trace("enrolment_failed", id, ErrorCode(err), start)
		if strings.HasPrefix(ErrorCode(err), "ENROLMENT_") {
			if recovered := c.Recover(ctx); recovered == nil {
				return c.renewIfNeeded(ctx)
			}
		}
		return err
	}
	if out.ClientID != identity.ClientID(c.Key.Public().(ed25519.PublicKey)) || !out.Expires.After(time.Now()) {
		return &Error{Code: "PROTOCOL_ERROR"}
	}
	c.Config.ClientID = out.ClientID
	c.Config.Expires = out.Expires
	c.trace("enrolment_completed", id, "ok", start)
	return nil
}

func (c *Client) renewIfNeeded(ctx context.Context) error {
	if time.Until(c.Config.Expires) < 7*24*time.Hour {
		return c.Renew(ctx)
	}
	return nil
}

// Recover proves possession of the installation key. It restores lost local
// enrolment metadata without reusing a code or re-enrolling/reviving a record.
func (c *Client) Recover(ctx context.Context) error {
	if len(c.Key) != ed25519.PrivateKeySize {
		return &Error{Code: "INVALID_CLIENT_KEY"}
	}
	ctx, cancel := context.WithTimeout(ctx, c.Config.Timeout)
	defer cancel()
	start := time.Now()
	s, err := c.connect(ctx)
	if err != nil {
		return err
	}
	defer s.conn.Close()
	stop := context.AfterFunc(ctx, func() { s.conn.Close() })
	defer stop()
	clientID := identity.ClientID(c.Key.Public().(ed25519.PublicKey))
	id := ID()
	if err = s.send(protocol.Authenticate, protocol.Envelope{RequestID: id, ClientID: clientID}, c.Key); err != nil {
		return &Error{Code: ErrorCode(err)}
	}
	out, err := s.receive(protocol.AuthResult, id)
	if err != nil {
		c.trace("credential_recovery_failed", id, ErrorCode(err), start)
		return err
	}
	if out.ClientID != clientID || !out.Expires.After(time.Now()) {
		return &Error{Code: "PROTOCOL_ERROR"}
	}
	pingID := ID()
	if err = s.send(protocol.PingV2, protocol.Envelope{RequestID: pingID, ClientID: clientID}, c.Key); err != nil {
		return &Error{Code: ErrorCode(err)}
	}
	pong, err := s.receive(protocol.PongV2, pingID)
	if err != nil {
		return err
	}
	if pong.Fingerprint != c.Config.Fingerprint || pong.Version != 2 {
		return &Error{Code: "PROTOCOL_ERROR"}
	}
	c.Config.ClientID = clientID
	c.Config.Expires = out.Expires
	c.trace("credential_recovered", id, "ok", start)
	return nil
}
func (c Client) Call(ctx context.Context, typ byte, e protocol.Envelope) (protocol.Envelope, error) {
	if len(c.Key) != ed25519.PrivateKeySize || c.Config.ClientID == "" {
		return protocol.Envelope{}, &Error{Code: "NOT_ENROLLED"}
	}
	ctx, cancel := context.WithTimeout(ctx, c.Config.Timeout)
	defer cancel()
	s, err := c.connect(ctx)
	if err != nil {
		return protocol.Envelope{}, err
	}
	defer s.conn.Close()
	var sent atomic.Bool
	if e.RequestID == "" {
		e.RequestID = ID()
	}
	e.ClientID = c.Config.ClientID
	stop := context.AfterFunc(ctx, func() {
		if sent.Load() && typ == protocol.HTTPFetch {
			_ = s.conn.SetWriteDeadline(time.Now().Add(100 * time.Millisecond))
			_ = s.send(protocol.Cancel, protocol.Envelope{RequestID: e.RequestID, ClientID: e.ClientID}, c.Key)
		}
		s.conn.Close()
	})
	defer stop()
	id := ID()
	if err = s.send(protocol.Authenticate, protocol.Envelope{RequestID: id, ClientID: c.Config.ClientID}, c.Key); err != nil {
		return protocol.Envelope{}, &Error{Code: "NODE_UNREACHABLE"}
	}
	if _, err = s.receive(protocol.AuthResult, id); err != nil {
		return protocol.Envelope{}, err
	}
	if err = s.send(typ, e, c.Key); err != nil {
		return protocol.Envelope{}, &Error{Code: "NODE_UNREACHABLE"}
	}
	sent.Store(true)
	expected := map[byte]byte{protocol.HTTPFetch: protocol.HTTPResult, protocol.PingV2: protocol.PongV2, protocol.Renew: protocol.RenewResult}[typ]
	out, err := s.receive(expected, e.RequestID)
	if ctx.Err() != nil {
		return out, &Error{Code: ErrorCode(ctx.Err())}
	}
	if err == nil && (typ == protocol.HTTPFetch || typ == protocol.PingV2) && (out.Version != 2 || out.Fingerprint != c.Config.Fingerprint) {
		return out, &Error{Code: "PROTOCOL_ERROR"}
	}
	return out, err
}
func (c Client) Ping(ctx context.Context) (time.Duration, error) {
	start := time.Now()
	_, err := c.Call(ctx, protocol.PingV2, protocol.Envelope{})
	return time.Since(start), err
}
func (c Client) Fetch(ctx context.Context, id, target string) (protocol.Envelope, error) {
	return c.Call(ctx, protocol.HTTPFetch, protocol.Envelope{RequestID: id, Method: "GET", Target: target})
}
func (c *Client) Renew(ctx context.Context) error {
	out, err := c.Call(ctx, protocol.Renew, protocol.Envelope{})
	if err == nil {
		c.Config.Expires = out.Expires
	}
	return err
}
