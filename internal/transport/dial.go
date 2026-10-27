package transport

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/hex"
	"errors"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"syscall"
	"time"

	"wolftunnel/internal/identity"
)

// Only documented transports are selectable; environment proxies and automatic
// fallback are deliberately absent. A proxy never changes the inner node pin.
type Endpoint struct {
	Host             string
	Port             int
	Pin, Mode, Proxy string
	Retries          int
	Discovery        bool
	Timeout          time.Duration
}
type Connector interface {
	Connect(context.Context, Endpoint, *Attempt) (*tls.Conn, error)
}
type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type Dialer struct {
	Resolver     Resolver
	DialContext  func(context.Context, string, string) (net.Conn, error)
	StageTimeout time.Duration
	// ProxyTLS is a test/embedding hook for approved proxy trust roots. Node pin
	// verification is fixed below and cannot be overridden through this option.
	ProxyTLS *tls.Config
	Wait     func(context.Context, time.Duration) error
}

func Validate(mode, proxy string, retries int) error {
	if mode != "" && mode != "tls-tcp" && mode != "http-connect" {
		return At(Disconnected, "UNSUPPORTED_TRANSPORT", nil)
	}
	if retries < 0 || retries > 3 {
		return At(Disconnected, "INVALID_CONFIGURATION", nil)
	}
	if proxy == "" {
		if mode == "http-connect" {
			return At(ProxyConnect, "INVALID_PROXY", nil)
		}
		return nil
	}
	if mode != "http-connect" {
		return At(ProxyConnect, "INVALID_PROXY", nil)
	}
	_, err := parseProxy(proxy)
	return err
}
func parseProxy(raw string) (*url.URL, error) {
	u, e := url.Parse(raw)
	if e != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || strings.ContainsAny(raw, "\r\n\t ") {
		return nil, At(ProxyConnect, "INVALID_PROXY", nil)
	}
	if u.Port() != "" {
		n, e := strconv.Atoi(u.Port())
		if e != nil || n < 1 || n > 65535 {
			return nil, At(ProxyConnect, "INVALID_PROXY", nil)
		}
	}
	return u, nil
}
func (d Dialer) Connect(ctx context.Context, e Endpoint, a *Attempt) (*tls.Conn, error) {
	timeout := e.Timeout
	if timeout <= 0 {
		timeout = 20 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if err := Validate(e.Mode, e.Proxy, e.Retries); err != nil {
		return nil, err
	}
	if e.Host == "" || strings.ContainsAny(e.Host, "/ \t\r\n?@#\\") || e.Port < 1 || e.Port > 65535 {
		return nil, At(Disconnected, "INVALID_CONFIGURATION", nil)
	}
	if !e.Discovery {
		if b, err := hex.DecodeString(e.Pin); err != nil || len(b) != 32 {
			return nil, At(VerifyingIdentity, "NODE_IDENTITY_UNVERIFIED", nil)
		}
	}
	for n := 0; ; n++ {
		c, err := d.connectOnce(ctx, e, a)
		if err == nil {
			return c, nil
		}
		if n >= e.Retries || !Retryable(err) || ctx.Err() != nil {
			return nil, err
		}
		a.Retry(n + 1)
		delay := time.Duration(float64(200*time.Millisecond<<n) * (0.8 + rand.Float64()*0.4))
		wait := d.Wait
		if wait == nil {
			wait = waitContext
		}
		if err = wait(ctx, delay); err != nil {
			return nil, stageError(RetryWait, "CONNECTION_TIMEOUT", err)
		}
	}
}
func waitContext(ctx context.Context, delay time.Duration) error {
	t := time.NewTimer(delay)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
func Retryable(err error) bool {
	switch Code(err) {
	case "CONNECTION_REFUSED", "CONNECTION_TIMEOUT", "CONNECTION_INTERRUPTED", "TLS_HANDSHAKE_FAILED", "PROXY_UNAVAILABLE":
		return true
	}
	return false
}
func (d Dialer) budget() time.Duration {
	if d.StageTimeout > 0 {
		return d.StageTimeout
	}
	return 5 * time.Second
}
func stageError(stage State, code string, err error) error {
	if errors.Is(err, context.Canceled) {
		code = "NODE_CANCELLED"
	}
	return At(stage, code, err)
}
func (d Dialer) tcp(ctx context.Context, host, port string, a *Attempt) (net.Conn, error) {
	a.Set(Resolving)
	dnsCtx, cancel := context.WithTimeout(ctx, d.budget())
	var ips []netip.Addr
	var err error
	if ip, e := netip.ParseAddr(host); e == nil {
		ips = []netip.Addr{ip}
	} else {
		r := d.Resolver
		if r == nil {
			r = net.DefaultResolver
		}
		ips, err = r.LookupNetIP(dnsCtx, "ip", host)
	}
	cancel()
	if err != nil || len(ips) == 0 {
		return nil, stageError(Resolving, "NODE_DNS_ERROR", err)
	}
	a.Set(Connecting)
	dialCtx, stop := context.WithTimeout(ctx, d.budget())
	defer stop()
	dial := d.DialContext
	if dial == nil {
		dial = (&net.Dialer{KeepAlive: 15 * time.Second}).DialContext
	}
	for _, ip := range ips[:min(8, len(ips))] {
		var c net.Conn
		c, err = dial(dialCtx, "tcp", net.JoinHostPort(ip.String(), port))
		if err == nil {
			return c, nil
		}
		if dialCtx.Err() != nil {
			break
		}
	}
	code := "CONNECTION_REFUSED"
	var timeout net.Error
	if errors.As(err, &timeout) && timeout.Timeout() {
		code = "CONNECTION_TIMEOUT"
	} else if !errors.Is(err, syscall.ECONNREFUSED) {
		code = "CONNECTION_INTERRUPTED"
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	return nil, stageError(Connecting, code, err)
}
func (d Dialer) connectOnce(ctx context.Context, e Endpoint, a *Attempt) (verified *tls.Conn, result error) {
	host, port := e.Host, strconv.Itoa(e.Port)
	var proxy *url.URL
	if e.Mode == "http-connect" {
		var err error
		proxy, err = parseProxy(e.Proxy)
		if err != nil {
			return nil, err
		}
		host = proxy.Hostname()
		port = proxy.Port()
		if port == "" {
			port = "80"
			if proxy.Scheme == "https" {
				port = "443"
			}
		}
	}
	raw, err := d.tcp(ctx, host, port, a)
	if err != nil {
		if proxy != nil && Retryable(err) {
			return nil, At(Connecting, "PROXY_UNAVAILABLE", err)
		}
		return nil, err
	}
	base := raw
	defer func() {
		if result != nil {
			base.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { base.Close() })
	defer stop()
	if proxy != nil {
		a.Set(ProxyConnect)
		stage, done := context.WithTimeout(ctx, d.budget())
		deadline, _ := stage.Deadline()
		raw.SetDeadline(deadline)
		if proxy.Scheme == "https" {
			config := &tls.Config{MinVersion: tls.VersionTLS12, ServerName: host}
			if d.ProxyTLS != nil {
				config = d.ProxyTLS.Clone()
				config.ServerName = host
				config.InsecureSkipVerify = false
				if config.MinVersion < tls.VersionTLS12 {
					config.MinVersion = tls.VersionTLS12
				}
			}
			outer := tls.Client(raw, config)
			if err = outer.HandshakeContext(stage); err != nil {
				done()
				return nil, stageError(ProxyConnect, "PROXY_TLS_FAILED", err)
			}
			raw = outer
		}
		authority := net.JoinHostPort(e.Host, strconv.Itoa(e.Port))
		request := &http.Request{Method: http.MethodConnect, URL: &url.URL{Opaque: authority}, Host: authority, Header: make(http.Header)}
		if err = request.Write(raw); err != nil {
			done()
			return nil, stageError(ProxyConnect, "PROXY_UNAVAILABLE", err)
		}
		reader := bufio.NewReaderSize(raw, 4096)
		// Only read bounded CONNECT headers; never consume/log the proxy response body.
		status, err := readConnect(reader)
		done()
		if err != nil {
			code := "PROXY_RESPONSE_INVALID"
			var timeout net.Error
			if errors.As(err, &timeout) && timeout.Timeout() {
				code = "PROXY_UNAVAILABLE"
			}
			if ctx.Err() != nil {
				err = ctx.Err()
			}
			return nil, stageError(ProxyConnect, code, err)
		}
		if status < 200 || status >= 300 {
			code := "PROXY_REJECTED"
			if status == 407 {
				code = "PROXY_AUTH_REQUIRED"
			}
			return nil, At(ProxyConnect, code, nil)
		}
		if reader.Buffered() > 0 {
			raw = &bufferedConn{Conn: raw, r: reader}
		}
	}
	a.Set(TLSHandshake)
	stage, done := context.WithTimeout(ctx, d.budget())
	defer done()
	deadline, _ := stage.Deadline()
	raw.SetDeadline(deadline)
	config := &tls.Config{MinVersion: tls.VersionTLS13, ServerName: e.Host, InsecureSkipVerify: true}
	config.VerifyConnection = func(cs tls.ConnectionState) error {
		a.Set(VerifyingIdentity)
		if len(cs.PeerCertificates) == 0 {
			return At(VerifyingIdentity, "NODE_IDENTITY_CHANGED", nil)
		}
		if e.Discovery {
			return nil
		}
		cert := cs.PeerCertificates[0]
		if identity.Fingerprint(cert.Raw) != strings.ToLower(e.Pin) {
			return At(VerifyingIdentity, "NODE_IDENTITY_CHANGED", nil)
		}
		if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
			return At(VerifyingIdentity, "NODE_IDENTITY_EXPIRED", nil)
		}
		return nil
	}
	c := tls.Client(raw, config)
	if err = c.HandshakeContext(stage); err != nil {
		var typed *Failure
		if errors.As(err, &typed) {
			return nil, typed
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, stageError(TLSHandshake, "TLS_HANDSHAKE_FAILED", err)
	}
	c.SetDeadline(time.Time{})
	return c, nil
}

type bufferedConn struct {
	net.Conn
	r io.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
func readConnect(reader *bufio.Reader) (int, error) {
	total := 0
	status := 0
	for lineNo := 0; lineNo < 64; lineNo++ {
		line, err := reader.ReadSlice('\n')
		total += len(line)
		if err != nil {
			return 0, err
		}
		if total > 16384 {
			return 0, errors.New("bounded proxy headers required")
		}
		if lineNo == 0 {
			parts := strings.Fields(string(line))
			if len(parts) < 2 || (parts[0] != "HTTP/1.1" && parts[0] != "HTTP/1.0") {
				return 0, errors.New("invalid proxy status")
			}
			status, _ = strconv.Atoi(parts[1])
			if status < 100 || status > 599 {
				return 0, errors.New("invalid proxy status")
			}
		}
		if string(line) == "\r\n" || string(line) == "\n" {
			return status, nil
		}
	}
	return 0, errors.New("too many proxy headers")
}
