package node

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"sync"
	"time"

	"wolftunnel/internal/audit"
	"wolftunnel/internal/auth"
	"wolftunnel/internal/fetch"
	"wolftunnel/internal/identity"
	"wolftunnel/internal/protocol"
	"wolftunnel/internal/storage"
)

type Config struct {
	Version        int           `json:"version"`
	Fetch          fetch.Config  `json:"fetch"`
	MaxConnections int           `json:"max_connections"`
	MaxFetches     int           `json:"max_fetches"`
	PerClient      int           `json:"per_client"`
	RatePerMinute  int           `json:"rate_per_minute"`
	Retention      time.Duration `json:"retention"`
}

func DefaultConfig() Config {
	return Config{Version: 2, Fetch: fetch.DefaultConfig(), MaxConnections: 64, MaxFetches: 16, PerClient: 2, RatePerMinute: 30, Retention: 7 * 24 * time.Hour}
}

type Fetcher interface {
	Get(context.Context, string) protocol.Envelope
}
type usage struct {
	active int
	count  int
	since  time.Time
}
type Server struct {
	Config      Config
	Certificate tls.Certificate
	Fingerprint string
	Auth        *auth.Store
	Fetcher     Fetcher
	Audit       *audit.Logger
	mu          sync.Mutex
	connections map[net.Conn]struct{}
	clients     map[string]*usage
	workers     chan struct{}
}

func Open(root string) (*Server, error) {
	if err := storage.Dir(root); err != nil {
		return nil, err
	}
	configPath := filepath.Join(root, "config.json")
	var c Config
	err := storage.Read(configPath, &c)
	fresh := errors.Is(err, os.ErrNotExist)
	if fresh {
		c = DefaultConfig()
	}
	if !fresh && err != nil {
		return nil, err
	}
	if !fresh {
		if _, err = os.Stat(filepath.Join(root, "identity")); err != nil {
			return nil, errors.New("existing node has missing identity; restore backup or perform explicit identity rotation")
		}
	}
	if c.Version != 2 || !c.Fetch.Valid() || c.MaxConnections < 1 || c.MaxConnections > 1024 || c.MaxFetches < 2 || c.MaxFetches > 128 || c.PerClient < 1 || c.PerClient >= c.MaxFetches || c.RatePerMinute < 1 || c.RatePerMinute > 10000 || c.Retention < time.Hour || c.Retention > 90*24*time.Hour {
		return nil, errors.New("invalid node configuration")
	}
	cert, err := identity.Node(root)
	if err != nil {
		return nil, err
	}
	if fresh {
		if err = storage.Write(configPath, c); err != nil {
			return nil, err
		}
	}
	a, err := auth.Open(root)
	if err != nil {
		return nil, err
	}
	logger, err := audit.New(filepath.Join(root, "logs"), c.Retention)
	if err != nil {
		return nil, err
	}
	return &Server{Config: c, Certificate: cert, Fingerprint: identity.Fingerprint(cert.Certificate[0]), Auth: a, Fetcher: fetch.New(c.Fetch), Audit: logger, connections: make(map[net.Conn]struct{}), clients: make(map[string]*usage), workers: make(chan struct{}, c.MaxFetches)}, nil
}
func (s *Server) TLSConfig() *tls.Config {
	return &tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{s.Certificate}}
}

// Serve owns the listener. Cancellation closes all accepted sockets and waits
// for handlers, propagating cancellation into their outbound HTTP requests.
func (s *Server) Serve(ctx context.Context, ln net.Listener) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		<-ctx.Done()
		ln.Close()
		s.mu.Lock()
		for c := range s.connections {
			c.Close()
		}
		s.mu.Unlock()
	}()
	s.Audit.Log(audit.Entry{Event: "node_startup", Status: "ready"})
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s.Audit.Purge()
			}
		}
	}()
	var serveErr error
	for {
		raw, err := ln.Accept()
		if err != nil {
			if ctx.Err() == nil {
				serveErr = err
			}
			break
		}
		s.mu.Lock()
		if ctx.Err() != nil || len(s.connections) >= s.Config.MaxConnections {
			s.mu.Unlock()
			raw.Close()
			s.Audit.Log(audit.Entry{Event: "resource_limit", Status: "connections"})
			continue
		}
		s.connections[raw] = struct{}{}
		s.mu.Unlock()
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() { raw.Close(); s.mu.Lock(); delete(s.connections, raw); s.mu.Unlock() }()
			s.handle(ctx, raw)
		}()
	}
	cancel()
	<-done
	wg.Wait()
	s.Audit.Log(audit.Entry{Event: "node_shutdown", Status: "clean"})
	return serveErr
}
func (s *Server) failure(c net.Conn, id, code string) {
	if !protocol.ValidID(id) {
		id = "protocol"
	}
	s.Audit.Log(audit.Entry{Event: "security_error", ID: id, Status: code})
	_ = protocol.WriteV2(c, protocol.Error, protocol.Envelope{RequestID: id, Code: code, Message: code})
}
func read(c net.Conn) (byte, protocol.Envelope, error) {
	m, err := protocol.ReadV2(c)
	if err != nil {
		return m.Type, protocol.Envelope{}, err
	}
	e, err := protocol.Decode(m)
	return m.Type, e, err
}
func (s *Server) read(c net.Conn) (byte, protocol.Envelope, error) {
	t, e, err := read(c)
	if err != nil {
		if errors.Is(err, protocol.ErrLegacyVersion) {
			typ := protocol.TypeAck
			if t == protocol.TypePing {
				typ = protocol.TypePong
			}
			payload, _ := json.Marshal(protocol.Response{Status: "unsupported_version", State: "WolfTunnel v0.2 requires a v2 client; upgrade the client", CanRespond: false})
			s.Audit.Log(audit.Entry{Event: "protocol_error", Status: "UNSUPPORTED_VERSION"})
			_ = protocol.Write(c, protocol.Message{Type: typ, Payload: payload})
			return t, e, err
		}
		code := "PROTOCOL_ERROR"
		if errors.Is(err, protocol.ErrVersion) {
			code = "UNSUPPORTED_VERSION"
		}
		s.failure(c, e.RequestID, code)
	}
	return t, e, err
}
func (s *Server) reserve(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	u := s.clients[id]
	if u == nil {
		u = &usage{}
		s.clients[id] = u
	}
	now := time.Now()
	if now.Sub(u.since) >= time.Minute {
		u.count = 0
		u.since = now
	}
	if u.active >= s.Config.PerClient || u.count >= s.Config.RatePerMinute {
		return false
	}
	u.active++
	u.count++
	return true
}
func (s *Server) release(id string) { s.mu.Lock(); s.clients[id].active--; s.mu.Unlock() }

func (s *Server) handle(parent context.Context, raw net.Conn) {
	_ = raw.SetDeadline(time.Now().Add(5 * time.Second))
	c := tls.Server(raw, s.TLSConfig())
	if c.HandshakeContext(parent) != nil {
		return
	}
	t, e, err := s.read(c)
	if err != nil {
		return
	}
	if t != protocol.Hello || len(e.Versions) != 1 || e.Versions[0] != 2 {
		s.failure(c, e.RequestID, "UNSUPPORTED_VERSION")
		return
	}
	if protocol.WriteV2(c, protocol.NodeIdentity, protocol.Envelope{RequestID: e.RequestID, Version: 2, Fingerprint: s.Fingerprint, Capabilities: []string{"get-metadata", "enrol", "renew", "cancel"}}) != nil {
		return
	}
	state := c.ConnectionState()
	binding, err := state.ExportKeyingMaterial("WolfTunnel-v2", nil, 32)
	if err != nil {
		return
	}
	t, e, err = s.read(c)
	if err != nil {
		return
	}
	if t == protocol.EnrolmentRequest {
		r, err := s.Auth.Enrol(e.PublicKey, e.Code)
		if err != nil {
			s.failure(c, e.RequestID, auth.EnrolmentCode(err))
			return
		}
		s.Audit.Log(audit.Entry{Event: "client_enrolled", ClientID: r.ClientID, ID: e.RequestID})
		_ = protocol.WriteV2(c, protocol.EnrolmentResult, protocol.Envelope{RequestID: e.RequestID, ClientID: r.ClientID, Expires: r.Expires})
		return
	}
	if t != protocol.Authenticate {
		s.failure(c, e.RequestID, "AUTH_REQUIRED")
		return
	}
	r, err := s.Auth.Verify(binding, t, e)
	if err != nil {
		s.failure(c, e.RequestID, "AUTH_FAILED")
		return
	}
	if !s.reserve(r.ClientID) {
		s.failure(c, e.RequestID, "RATE_LIMITED")
		return
	}
	defer s.release(r.ClientID)
	s.Audit.Log(audit.Entry{Event: "authentication_success", ClientID: r.ClientID, ID: e.RequestID})
	if protocol.WriteV2(c, protocol.AuthResult, protocol.Envelope{RequestID: e.RequestID, ClientID: r.ClientID, Expires: r.Expires}) != nil {
		return
	}
	t, e, err = s.read(c)
	if err != nil {
		return
	}
	if e.ClientID != r.ClientID {
		s.failure(c, e.RequestID, "AUTH_FAILED")
		return
	}
	if _, err = s.Auth.Verify(binding, t, e); err != nil {
		s.failure(c, e.RequestID, "AUTH_FAILED")
		return
	}
	switch t {
	case protocol.PingV2:
		_ = protocol.WriteV2(c, protocol.PongV2, protocol.Envelope{RequestID: e.RequestID, Version: 2, Fingerprint: s.Fingerprint})
	case protocol.Renew:
		renewed, err := s.Auth.Renew(r.ClientID)
		if err != nil {
			s.failure(c, e.RequestID, "AUTH_FAILED")
			return
		}
		s.Audit.Log(audit.Entry{Event: "credential_renewed", ClientID: r.ClientID, ID: e.RequestID})
		_ = protocol.WriteV2(c, protocol.RenewResult, protocol.Envelope{RequestID: e.RequestID, ClientID: r.ClientID, Expires: renewed.Expires})
	case protocol.HTTPFetch:
		if e.Method != "GET" || len(e.Target) == 0 || len(e.Target) > 8192 {
			s.failure(c, e.RequestID, "INVALID_REQUEST")
			return
		}
		select {
		case s.workers <- struct{}{}:
			defer func() { <-s.workers }()
		default:
			s.failure(c, e.RequestID, "NODE_BUSY")
			return
		}
		_ = c.SetDeadline(time.Now().Add(s.Config.Fetch.Timeout + 2*time.Second))
		ctx, cancel := context.WithTimeout(parent, s.Config.Fetch.Timeout)
		defer cancel()
		// The only further legal message is a signed cancel for this request.
		monitorDone := make(chan struct{})
		cancelCode := make(chan string, 1)
		go func() {
			defer close(monitorDone)
			typ, ce, err := read(c)
			code := "NODE_CANCELLED"
			if err == nil {
				if typ != protocol.Cancel || ce.ClientID != r.ClientID || ce.RequestID != e.RequestID {
					code = "PROTOCOL_ERROR"
				} else if _, err = s.Auth.Verify(binding, typ, ce); err != nil {
					code = "AUTH_FAILED"
				}
			} else if errors.Is(err, protocol.ErrPayload) || errors.Is(err, protocol.ErrInvalidHeader) || errors.Is(err, protocol.ErrVersion) {
				code = "PROTOCOL_ERROR"
			}
			cancelCode <- code
			cancel()
		}()
		defer func() { raw.Close(); <-monitorDone }()
		out := s.Fetcher.Get(ctx, e.Target)
		select {
		case code := <-cancelCode:
			if code == "AUTH_FAILED" || code == "PROTOCOL_ERROR" {
				s.failure(c, e.RequestID, code)
				return
			}
		default:
		}
		out.RequestID = e.RequestID
		out.Version = 2
		out.Fingerprint = s.Fingerprint
		s.Audit.Log(audit.Entry{Event: "http_fetch", ClientID: r.ClientID, ID: e.RequestID, Target: audit.HostOnly(e.Target), Status: fmt.Sprint(out.Status), Code: out.Code, DurationMS: out.DurationMS})
		_ = protocol.WriteV2(c, protocol.HTTPResult, out)
	default:
		s.failure(c, e.RequestID, "PROTOCOL_ERROR")
	}
}
