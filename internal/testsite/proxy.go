package testsite

import (
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

// ConnectProxy is a local test fixture restricted to one exact authority. It
// cannot become an open relay and never records TLS payloads.
type ConnectProxy struct {
	Target      string
	mu          sync.Mutex
	connections []net.Conn
}

func (p *ConnectProxy) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, c := range p.connections {
		c.Close()
	}
}
func (p *ConnectProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodConnect || r.Host != p.Target {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	up, err := net.DialTimeout("tcp", p.Target, time.Second)
	if err != nil {
		w.WriteHeader(http.StatusBadGateway)
		return
	}
	peer, rw, err := w.(http.Hijacker).Hijack()
	if err != nil {
		up.Close()
		return
	}
	p.mu.Lock()
	p.connections = append(p.connections, peer, up)
	p.mu.Unlock()
	defer peer.Close()
	defer up.Close()
	if _, err = io.WriteString(peer, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}
	done := make(chan struct{})
	go func() { io.Copy(up, rw); up.Close(); close(done) }()
	io.Copy(peer, up)
	peer.Close()
	<-done
}
