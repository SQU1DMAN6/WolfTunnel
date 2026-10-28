package frontend

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"wolftunnel/internal/browser"
	"wolftunnel/internal/browsernet"
	"wolftunnel/internal/node"
	"wolftunnel/internal/nodeclient"
	v3 "wolftunnel/internal/protocol/v3"
	"wolftunnel/internal/testsite"
)

type navigationResolver struct{}

func (navigationResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}
func TestRealModelNewTabNavigation(t *testing.T) {
	bin := os.Getenv("WOLF_BROWSER_BIN")
	if bin == "" {
		t.Skip("browser target required")
	}
	raw, e := os.ReadFile(bin)
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.Sum256(raw)
	cfg := browser.DefaultConfig()
	cfg.Binary = bin
	cfg.SHA256 = hex.EncodeToString(hash[:])
	cfg.MaxTabs = 3
	fixture := httptest.NewServer(&testsite.Site{})
	defer fixture.Close()
	s, e := node.Open(filepath.Join(t.TempDir(), "node"))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.EnableBrowser(cfg); e != nil {
		t.Fatal(e)
	}
	s.Browser.ProxyOptions = browsernet.Options{Resolver: navigationResolver{}, Dial: func(c context.Context, n, a string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(c, n, fixture.Listener.Addr().String())
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	defer func() { cancel(); <-done }()
	c, e := nodeclient.Load(filepath.Join(t.TempDir(), "client"))
	if e != nil {
		t.Fatal(e)
	}
	c.Config.Port = ln.Addr().(*net.TCPAddr).Port
	c.Config.Fingerprint = s.Fingerprint
	code, _ := s.Auth.IssueCode()
	if e = c.Enrol(ctx, code); e != nil {
		t.Fatal(e)
	}
	m := NewModel(c, t.TempDir())
	defer m.Shutdown()
	m.Connection = "Connected"
	m.Verified = true
	m.ViewportWidth = 900
	m.ViewportHeight = 650
	origin := m.Current()
	origin.Input = "http://fixture.test/links"
	if e = m.Navigate(); e != nil {
		t.Fatal(e)
	}
	wait := func(what string, predicate func() bool) {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			m.Drain()
			if predicate() {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("timeout", what, m.Notice)
	}
	wait("origin", func() bool { return origin.Frame != nil && origin.State == "Completed" })
	m.SendInput(v3.Input{Kind: "move", X: 70, Y: 35})
	wait("hover", func() bool { return strings.Contains(m.LinkTarget, "case=normal") })
	click := func(y float64) {
		t.Helper()
		m.SelectTab(0)
		if e = m.SendInput(v3.Input{Kind: "down", X: 70, Y: y, Button: 1, Modifiers: 2}); e != nil {
			t.Fatal(e)
		}
		if e = m.SendInput(v3.Input{Kind: "up", X: 70, Y: y, Button: 1}); e != nil {
			t.Fatal(e)
		}
	}
	click(35)
	wait("new tab", func() bool { return len(m.Tabs) == 2 && m.Current().Frame != nil && m.Current().State == "Completed" })
	if origin.Target != "http://fixture.test/links" {
		t.Fatal("source tab navigated", origin.Target)
	}
	click(155)
	wait("redirect", func() bool {
		return len(m.Tabs) == 3 && strings.Contains(m.Current().Target, "redirected=1") && m.Current().State == "Completed"
	})
	click(35)
	wait("tab limit", func() bool {
		return strings.Contains(m.Notice, "Maximum") || strings.Contains(m.NavigationText, "tabs")
	})
	if len(m.Tabs) != 3 || m.RemoteFailureCode != "" {
		t.Fatal("limit changed session state", len(m.Tabs), m.RemoteFailureCode)
	}
	m.CloseTab(2)
	click(275)
	wait("policy", func() bool {
		return strings.Contains(m.NavigationText, "blocked") || strings.Contains(m.NavigationText, "public") || strings.Contains(m.NavigationText, "policy")
	})
	if len(m.Tabs) != 2 {
		t.Fatal("private destination allocated tab")
	}
}
