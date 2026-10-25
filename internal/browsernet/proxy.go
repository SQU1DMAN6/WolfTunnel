// Package browsernet is the browser's only path out of its network namespace.
package browsernet

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"wolftunnel/internal/fetch"
)

var ErrPolicy = errors.New("NODE_POLICY_REJECTED")
var ErrBudget = errors.New("RESOURCE_LIMIT")

const byteBudget int64 = 256 << 20

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type Options struct {
	Resolver Resolver
	Dial     func(context.Context, string, string) (net.Conn, error)
}
type Proxy struct {
	Dir, Socket string
	server      *http.Server
	listener    net.Listener
	ctx         context.Context
	cancel      context.CancelFunc
	resolve     Resolver
	dial        func(context.Context, string, string) (net.Conn, error)
	slots       chan struct{}
	mu          sync.Mutex
	conns       map[net.Conn]bool
	bytes       atomic.Int64
	Rejected    atomic.Uint64
}

func New(ctx context.Context, o Options) (*Proxy, error) {
	dir, e := os.MkdirTemp("", "wolf-egress-")
	if e != nil {
		return nil, e
	}
	ctx, cancel := context.WithCancel(ctx)
	p := &Proxy{Dir: dir, Socket: filepath.Join(dir, "proxy.sock"), ctx: ctx, cancel: cancel, resolve: o.Resolver, dial: o.Dial, slots: make(chan struct{}, 64), conns: make(map[net.Conn]bool)}
	if p.resolve == nil {
		p.resolve = net.DefaultResolver
	}
	if p.dial == nil {
		p.dial = (&net.Dialer{Timeout: 5 * time.Second}).DialContext
	}
	p.listener, e = net.Listen("unix", p.Socket)
	if e != nil {
		cancel()
		os.RemoveAll(dir)
		return nil, e
	}
	if e = os.Chmod(p.Socket, 0600); e != nil {
		p.Close()
		return nil, e
	}
	p.server = &http.Server{ConnState: func(c net.Conn, state http.ConnState) {
		if state == http.StateNew {
			p.track(c)
		}
		if state == http.StateClosed {
			p.untrack(c)
		}
	}, Handler: p, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 20 * time.Second, MaxHeaderBytes: 32 << 10, ErrorLog: log.New(io.Discard, "", 0)}
	go func() { _ = p.server.Serve(&limitedListener{Listener: p.listener, slots: make(chan struct{}, 64)}) }()
	return p, nil
}
func (p *Proxy) Close() {
	p.cancel()
	if p.server != nil {
		_ = p.server.Close()
	}
	if p.listener != nil {
		_ = p.listener.Close()
	}
	p.mu.Lock()
	for c := range p.conns {
		c.Close()
	}
	p.mu.Unlock()
	_ = os.RemoveAll(p.Dir)
}
func Validate(raw string) error {
	u, e := url.Parse(raw)
	if e != nil {
		return ErrPolicy
	}
	if u.Scheme == "ws" {
		u.Scheme = "http"
	}
	if u.Scheme == "wss" {
		u.Scheme = "https"
	}
	if _, e = fetch.Validate(u.String()); e != nil {
		return ErrPolicy
	}
	return nil
}
func (p *Proxy) Dial(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, e := net.SplitHostPort(address)
	if e != nil || (port != "80" && port != "443") {
		return nil, ErrPolicy
	}
	scheme := "http"
	if port == "443" {
		scheme = "https"
	}
	if Validate(scheme+"://"+address) != nil {
		return nil, ErrPolicy
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ips, e := p.resolve.LookupNetIP(ctx, "ip", host)
	if e != nil || len(ips) == 0 {
		return nil, errors.New("NODE_DNS_ERROR")
	}
	for _, ip := range ips {
		if !fetch.Public(ip) {
			return nil, ErrPolicy
		}
	}
	for _, ip := range ips {
		c, err := p.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return &budgetConn{Conn: c, total: &p.bytes}, nil
		}
		e = err
	}
	return nil, e
}
func (p *Proxy) track(c net.Conn)   { p.mu.Lock(); p.conns[c] = true; p.mu.Unlock() }
func (p *Proxy) untrack(c net.Conn) { p.mu.Lock(); delete(p.conns, c); p.mu.Unlock() }
func (p *Proxy) reject(w http.ResponseWriter) {
	p.Rejected.Add(1)
	w.Header().Set("X-WolfTunnel-Policy-Rejected", "1")
	http.Error(w, "WolfTunnel destination policy rejected this request", http.StatusForbidden)
}
func (p *Proxy) failure(w http.ResponseWriter, e error) {
	if errors.Is(e, ErrPolicy) {
		p.reject(w)
		return
	}
	code := "NODE_UNREACHABLE"
	status := http.StatusBadGateway
	if errors.Is(e, ErrBudget) {
		code = "RESOURCE_LIMIT"
		status = http.StatusServiceUnavailable
	}
	if e != nil && e.Error() == "NODE_DNS_ERROR" {
		code = "NODE_DNS_ERROR"
	}
	w.Header().Set("X-WolfTunnel-Error", code)
	http.Error(w, code, status)
}
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	select {
	case p.slots <- struct{}{}:
		defer func() { <-p.slots }()
	default:
		http.Error(w, "Resource limit", 503)
		return
	}
	if p.bytes.Load() >= byteBudget {
		p.failure(w, ErrBudget)
		return
	}
	if r.Method == http.MethodConnect {
		target := r.Host
		scheme := "https"
		if strings.HasSuffix(target, ":80") {
			scheme = "http"
		}
		if Validate(scheme+"://"+target) != nil {
			p.reject(w)
			return
		}
		remote, e := p.Dial(p.ctx, "tcp", target)
		if e != nil {
			p.failure(w, e)
			return
		}
		defer remote.Close()
		local, rw, e := w.(http.Hijacker).Hijack()
		if e != nil {
			return
		}
		defer local.Close()
		p.track(local)
		p.track(remote)
		defer p.untrack(local)
		defer p.untrack(remote)
		_ = local.SetDeadline(time.Now().Add(2 * time.Minute))
		_ = remote.SetDeadline(time.Now().Add(2 * time.Minute))
		_, _ = rw.WriteString("HTTP/1.1 200 Connection Established\r\n\r\n")
		if rw.Flush() != nil {
			return
		}
		done := make(chan struct{})
		go func() {
			_, _ = io.Copy(remote, io.LimitReader(rw, 64<<20))
			remote.Close()
			close(done)
		}()
		_, _ = io.Copy(local, io.LimitReader(remote, 64<<20))
		local.Close()
		remote.Close()
		<-done
		return
	}
	if !r.URL.IsAbs() || Validate(r.URL.String()) != nil || strings.HasPrefix(strings.ToLower(r.Header.Get("Content-Type")), "multipart/") {
		p.reject(w)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	tr := &http.Transport{Proxy: nil, DialContext: p.Dial, DisableKeepAlives: true, ResponseHeaderTimeout: 20 * time.Second, TLSHandshakeTimeout: 5 * time.Second, MaxResponseHeaderBytes: 32 << 10}
	defer tr.CloseIdleConnections()
	rp := &httputil.ReverseProxy{Rewrite: func(pr *httputil.ProxyRequest) {
		pr.Out.URL.Scheme = pr.In.URL.Scheme
		pr.Out.URL.Host = pr.In.URL.Host
		pr.Out.Host = pr.In.URL.Host
		pr.Out.Header.Del("Forwarded")
		pr.Out.Header.Del("X-Forwarded-For")
		pr.Out.Header.Del("X-Forwarded-Host")
		pr.Out.Header.Del("X-Forwarded-Proto")
		pr.Out.Header.Del("Proxy-Authorization")
	}, Transport: tr, ErrorLog: log.New(io.Discard, "", 0), ErrorHandler: func(w http.ResponseWriter, r *http.Request, e error) { p.failure(w, e) }, ModifyResponse: func(resp *http.Response) error {
		resp.Header.Del("X-WolfTunnel-Policy-Rejected")
		resp.Header.Del("X-WolfTunnel-Error")
		if resp.StatusCode == http.StatusSwitchingProtocols {
			return nil
		}
		resp.Body = &countBody{ReadCloser: resp.Body, remaining: 32 << 20}
		return nil
	}}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	rp.ServeHTTP(proxyWriter{ResponseWriter: w, p: p}, r.WithContext(ctx))
}

type countBody struct {
	io.ReadCloser
	remaining int64
}

func (b *countBody) Read(p []byte) (int, error) {
	if b.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, e := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, e
}

// Account every upstream TCP read/write, including HTTPS and WebSocket bytes.
// Reserve before I/O so simultaneous tunnels cannot exceed the session budget.
type budgetConn struct {
	net.Conn
	total *atomic.Int64
}

func (c *budgetConn) claim(n int) int {
	for {
		used := c.total.Load()
		remaining := byteBudget - used
		if remaining <= 0 {
			return 0
		}
		take := min(int64(n), remaining)
		if c.total.CompareAndSwap(used, used+take) {
			return int(take)
		}
	}
}
func (c *budgetConn) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	reserved := c.claim(len(p))
	if reserved == 0 {
		c.Conn.Close()
		return 0, ErrBudget
	}
	n, e := c.Conn.Read(p[:reserved])
	c.total.Add(int64(n - reserved))
	return n, e
}
func (c *budgetConn) Write(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	reserved := c.claim(len(p))
	if reserved == 0 {
		c.Conn.Close()
		return 0, ErrBudget
	}
	n, e := c.Conn.Write(p[:reserved])
	c.total.Add(int64(n - reserved))
	return n, e
}

type proxyWriter struct {
	http.ResponseWriter
	p *Proxy
}

func (w proxyWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}
func (w proxyWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c, r, e := w.ResponseWriter.(http.Hijacker).Hijack()
	if e != nil {
		return nil, nil, e
	}
	return &trackedConn{Conn: c, p: w.p}, r, nil
}

type trackedConn struct {
	net.Conn
	p *Proxy
}

func (c *trackedConn) Close() error { c.p.untrack(c.Conn); return c.Conn.Close() }

// Bound sockets before net/http can allocate a per-connection goroutine.
type limitedListener struct {
	net.Listener
	slots chan struct{}
}

func (l *limitedListener) Accept() (net.Conn, error) {
	for {
		c, e := l.Listener.Accept()
		if e != nil {
			return nil, e
		}
		select {
		case l.slots <- struct{}{}:
			return &limitedConn{Conn: c, release: func() { <-l.slots }}, nil
		default:
			c.Close()
		}
	}
}

type limitedConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitedConn) Close() error { e := c.Conn.Close(); c.once.Do(c.release); return e }
