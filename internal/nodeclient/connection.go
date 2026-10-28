package nodeclient

import (
	"context"
	"crypto/tls"
	"errors"
	"strings"
	"time"
	"wolftunnel/internal/audit"
	"wolftunnel/internal/identity"
	"wolftunnel/internal/transport"
)

func (c Client) attempt(channel string) *transport.Attempt {
	return transport.NewAttempt(channel, c.Config.Transport, func(e transport.Event) {
		e.Correlation = c.correlation
		if c.Log != nil {
			c.Log.Log(audit.Entry{Component: "client", Event: "connection_state", Correlation: c.correlation, ID: e.Attempt, Status: string(e.State), Stage: string(e.Stage), Code: e.Code, Transport: e.Transport, Channel: e.Channel, Retry: e.Retry, DurationMS: e.DurationMS})
		}
		if c.Observer != nil {
			c.Observer(e)
		}
	})
}
func (c Client) dial(ctx context.Context, a *transport.Attempt) (*tls.Conn, error) {
	d := c.Connector
	if d == nil {
		d = transport.Dialer{}
	}
	conn, err := d.Connect(ctx, transport.Endpoint{Host: c.Config.Host, Port: c.Config.Port, Pin: c.Config.Fingerprint, Mode: c.Config.Transport, Proxy: c.Config.Proxy, Retries: c.Config.Retries, Timeout: c.Config.Timeout}, a)
	if err != nil {
		return nil, err
	}
	if conn == nil {
		return nil, transport.At(transport.Connecting, "CONNECTION_INTERRUPTED", nil)
	}
	// The application boundary verifies every connector result too. A new or
	// injected transport cannot remove pinning, TLS 1.3 or certificate validity.
	state := conn.ConnectionState()
	if !state.HandshakeComplete || state.Version < tls.VersionTLS13 || len(state.PeerCertificates) == 0 || identity.Fingerprint(state.PeerCertificates[0].Raw) != strings.ToLower(c.Config.Fingerprint) {
		conn.Close()
		return nil, transport.At(transport.VerifyingIdentity, "NODE_IDENTITY_CHANGED", nil)
	}
	cert := state.PeerCertificates[0]
	if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
		conn.Close()
		return nil, transport.At(transport.VerifyingIdentity, "NODE_IDENTITY_EXPIRED", nil)
	}
	return conn, nil
}
func stageFailure(a *transport.Attempt, err error) error {
	if err == nil {
		return nil
	}
	if a == nil {
		return err
	}
	if transport.Code(err) != "" {
		a.Finish(err)
		return err
	}
	code := ErrorCode(err)
	if errors.Is(err, context.Canceled) {
		code = "NODE_CANCELLED"
	}
	wrapped := a.Wrap(err).(*transport.Failure)
	wrapped.Code = code
	a.Finish(wrapped)
	return wrapped
}
