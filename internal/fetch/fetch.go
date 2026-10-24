// Package fetch is the sole outbound HTTP boundary. Every connection resolves,
// validates all DNS answers, then dials a validated numeric address directly.
package fetch

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"wolftunnel/internal/audit"
	"wolftunnel/internal/protocol"
)

type Resolver interface {
	LookupNetIP(context.Context, string, string) ([]netip.Addr, error)
}
type Config struct {
	Timeout      time.Duration `json:"timeout"`
	MaxBytes     int64         `json:"max_bytes"`
	MaxRedirects int           `json:"max_redirects"`
}

func DefaultConfig() Config {
	return Config{Timeout: 15 * time.Second, MaxBytes: 2 << 20, MaxRedirects: 5}
}
func (c Config) Valid() bool {
	return c.Timeout >= time.Second && c.Timeout <= time.Minute && c.MaxBytes > 0 && c.MaxBytes <= 16<<20 && c.MaxRedirects >= 0 && c.MaxRedirects <= 10
}

type Fetcher struct {
	Config   Config
	resolver Resolver
	dial     func(context.Context, string, string) (net.Conn, error)
}

func New(c Config) *Fetcher {
	return &Fetcher{Config: c, resolver: net.DefaultResolver, dial: (&net.Dialer{Timeout: 5 * time.Second}).DialContext}
}

var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"), netip.MustParsePrefix("10.0.0.0/8"), netip.MustParsePrefix("100.64.0.0/10"), netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"), netip.MustParsePrefix("172.16.0.0/12"), netip.MustParsePrefix("192.0.0.0/24"), netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("168.63.129.16/32"), netip.MustParsePrefix("192.88.99.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"), netip.MustParsePrefix("198.18.0.0/15"), netip.MustParsePrefix("198.51.100.0/24"), netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("224.0.0.0/3"),
	netip.MustParsePrefix("2001::/23"), netip.MustParsePrefix("2001:db8::/32"), netip.MustParsePrefix("2002::/16"), netip.MustParsePrefix("3fff::/20"),
}

func Public(ip netip.Addr) bool {
	ip = ip.Unmap()
	if !ip.IsValid() || ip.Zone() != "" || !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return false
	}
	// Only allocated global IPv6 space; reject translation and local-use prefixes.
	if ip.Is6() && !netip.MustParsePrefix("2000::/3").Contains(ip) {
		return false
	}
	for _, p := range blocked {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

var errPolicy = errors.New("NODE_POLICY_REJECTED")
var errDNS = errors.New("NODE_DNS_ERROR")
var errRedirect = errors.New("NODE_REDIRECT_LIMIT")

func Validate(raw string) (*url.URL, error) {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\r\n\x00") {
		return nil, errPolicy
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.Opaque != "" || strings.Contains(u.Hostname(), "%") {
		return nil, errPolicy
	}
	port := u.Port()
	if (u.Scheme == "http" && port != "" && port != "80") || (u.Scheme == "https" && port != "" && port != "443") {
		return nil, errPolicy
	}
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || host == "metadata.google.internal" {
		return nil, errPolicy
	}
	if ip, err := netip.ParseAddr(host); err == nil && !Public(ip) {
		return nil, errPolicy
	}
	u.Fragment = ""
	u.RawFragment = ""
	return u, nil
}

func (f *Fetcher) dialContext(ctx context.Context, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil || (port != "80" && port != "443") {
		return nil, errPolicy
	}
	ips, err := f.resolver.LookupNetIP(ctx, "ip", host)
	if err != nil || len(ips) == 0 {
		return nil, errDNS
	}
	for _, ip := range ips {
		if !Public(ip) {
			return nil, errPolicy
		}
	}
	// No second DNS lookup: pin this connection to the checked address.
	var last error
	for _, ip := range ips {
		c, e := f.dial(ctx, network, net.JoinHostPort(ip.String(), port))
		if e == nil {
			return c, nil
		}
		last = e
	}
	return nil, last
}

func (f *Fetcher) Get(ctx context.Context, raw string) (out protocol.Envelope) {
	start := time.Now()
	defer func() { out.DurationMS = time.Since(start).Milliseconds() }()
	u, err := Validate(raw)
	if err != nil {
		out.Code = "NODE_POLICY_REJECTED"
		return
	}
	ctx, cancel := context.WithTimeout(ctx, f.Config.Timeout)
	defer cancel()
	tr := &http.Transport{Proxy: nil, DialContext: f.dialContext, DisableKeepAlives: true, DisableCompression: true, TLSHandshakeTimeout: 5 * time.Second, ResponseHeaderTimeout: f.Config.Timeout, MaxResponseHeaderBytes: 16 << 10}
	defer tr.CloseIdleConnections()
	seen := map[string]bool{u.String(): true}
	client := &http.Client{Transport: tr, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) > f.Config.MaxRedirects || seen[req.URL.String()] {
			return errRedirect
		}
		if _, e := Validate(req.URL.String()); e != nil {
			return e
		}
		// Headers are created by WolfTunnel only. Never forward referer across hops.
		req.Header = make(http.Header)
		req.Header.Set("User-Agent", "WolfTunnel/0.2")
		seen[req.URL.String()] = true
		out.Redirects = len(via)
		return nil
	}}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		out.Code = "NODE_POLICY_REJECTED"
		return
	}
	req.Header.Set("User-Agent", "WolfTunnel/0.2")
	resp, err := client.Do(req)
	if err != nil {
		var timeout net.Error
		switch {
		case errors.Is(ctx.Err(), context.DeadlineExceeded):
			out.Code = "NODE_TIMEOUT"
		case errors.Is(ctx.Err(), context.Canceled):
			out.Code = "NODE_CANCELLED"
		case errors.Is(err, errPolicy):
			out.Code = "NODE_POLICY_REJECTED"
		case errors.Is(err, errDNS):
			out.Code = "NODE_DNS_ERROR"
		case errors.Is(err, errRedirect):
			out.Code = "NODE_REDIRECT_LIMIT"
		case errors.As(err, &timeout) && timeout.Timeout():
			out.Code = "NODE_TIMEOUT"
		default:
			out.Code = "NODE_FETCH_ERROR"
		}
		return
	}
	defer resp.Body.Close()
	out.Status = resp.StatusCode
	out.Reason = http.StatusText(resp.StatusCode)
	out.FinalURL = audit.SanitizeURL(resp.Request.URL.String())
	out.ContentType = resp.Header.Get("Content-Type")
	out.Headers = make(map[string]string)
	for _, h := range []string{"Content-Type", "Content-Length", "Content-Encoding"} {
		if v := resp.Header.Get(h); v != "" {
			out.Headers[h] = v
		}
	}
	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, f.Config.MaxBytes+1))
	out.BodyLength = n
	if n > f.Config.MaxBytes {
		out.Code = "NODE_RESPONSE_TOO_LARGE"
	} else if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			out.Code = "NODE_TIMEOUT"
		} else if ctx.Err() == context.Canceled {
			out.Code = "NODE_CANCELLED"
		} else {
			out.Code = "NODE_FETCH_ERROR"
		}
	}
	return
}
