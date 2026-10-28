package session_test

import (
	"context"
	"crypto/tls"
	"testing"
	"time"
	"wolftunnel/internal/nodeclient"
	"wolftunnel/internal/transport"
)

type delayedDisplayConnector struct {
	calls int
	delay time.Duration
}

func (d *delayedDisplayConnector) Connect(ctx context.Context, e transport.Endpoint, a *transport.Attempt) (*tls.Conn, error) {
	d.calls++
	if d.calls == 2 {
		timer := time.NewTimer(d.delay)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
	return (transport.Dialer{}).Connect(ctx, e, a)
}
func TestDelayedDisplayAttachment(t *testing.T) {
	s, c, ctx := performanceNode(t, 0)
	c.Connector = &delayedDisplayConnector{delay: 6 * time.Second}
	start := time.Now()
	b, e := c.OpenBrowser(ctx, nodeclient.BrowserOptions{Width: 800, Height: 600})
	if b != nil {
		defer b.Close()
	}
	t.Logf("attachment delay=6s; elapsed=%s; result=%v; sessions=%d", time.Since(start), e, s.Browser.Count())
	if e != nil {
		t.Fatal("a permitted display handshake delay must not expire the healthy control session", e)
	}
}
