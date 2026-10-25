package session_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/hex"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wolftunnel/internal/browser"
	"wolftunnel/internal/browsernet"
	"wolftunnel/internal/node"
	"wolftunnel/internal/nodeclient"
	v3 "wolftunnel/internal/protocol/v3"
)

type resolver struct{}

func (resolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}
func TestRealTLSBrowserPixelStream(t *testing.T) {
	bin := os.Getenv("WOLF_BROWSER_BIN")
	if bin == "" {
		t.Skip("required browser integration target sets WOLF_BROWSER_BIN")
	}
	hash, e := os.Open(bin)
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.New()
	_, e = io.Copy(h, hash)
	hash.Close()
	if e != nil {
		t.Fatal(e)
	}
	cfg := browser.DefaultConfig()
	cfg.MaxSessions = 1
	cfg.Binary = bin
	cfg.SHA256 = hex.EncodeToString(h.Sum(nil))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
	defer cancel()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><style>body{margin:0;background:#1555b0}button{position:absolute;left:20px;top:20px;width:180px;height:70px}input{position:absolute;left:20px;top:120px}</style><button onclick="document.body.style.background='#15b055'">WOLF PIXEL TEST</button><input id="text"><script>document.title='JavaScript active';</script>`)
	}))
	defer fixture.Close()
	root := filepath.Join(t.TempDir(), "node")
	s, e := node.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.EnableBrowser(cfg); e != nil {
		t.Fatal(e)
	}
	s.Browser.ProxyOptions = browsernet.Options{Resolver: resolver{}, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, fixture.Listener.Addr().String())
	}}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	serverDone := make(chan error, 1)
	go func() { serverDone <- s.Serve(ctx, ln) }()
	defer func() {
		cancel()
		select {
		case e := <-serverDone:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(5 * time.Second):
			t.Error("node teardown timeout")
		}
	}()
	c, e := nodeclient.Load(filepath.Join(t.TempDir(), "client"))
	if e != nil {
		t.Fatal(e)
	}
	c.Config.Port = ln.Addr().(*net.TCPAddr).Port
	c.Config.Fingerprint = s.Fingerprint
	code, e := s.Auth.IssueCode()
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Enrol(ctx, code); e != nil {
		t.Fatal(e)
	}
	start := time.Now()
	stream, e := c.OpenBrowser(ctx, nodeclient.BrowserOptions{Width: 800, Height: 600})
	if e != nil {
		t.Fatal(e)
	}
	defer stream.Close()
	if duplicate, err := c.OpenBrowser(ctx, nodeclient.BrowserOptions{Width: 800, Height: 600}); err == nil {
		duplicate.Close()
		t.Fatal("per-client session limit was not enforced")
	} else if nodeclient.ErrorCode(err) != "SESSION_LIMIT" {
		t.Fatal(err)
	}
	// Session IDs are not bearer tokens: a separately enrolled client cannot attach.
	other, e := nodeclient.Load(filepath.Join(t.TempDir(), "other-client"))
	if e != nil {
		t.Fatal(e)
	}
	other.Config = c.Config
	other.Config.ClientID = ""
	otherCode, e := s.Auth.IssueCode()
	if e != nil {
		t.Fatal(e)
	}
	if e = other.Enrol(ctx, otherCode); e != nil {
		t.Fatal(e)
	}
	if code := attemptAttach(t, other, stream.ID); code != "SESSION_INVALID" {
		t.Fatalf("cross-client pixel attach accepted: %s", code)
	}
	if extra, err := other.OpenBrowser(ctx, nodeclient.BrowserOptions{Width: 800, Height: 600}); err == nil {
		extra.Close()
		t.Fatal("global session limit was not enforced")
	} else if nodeclient.ErrorCode(err) != "SESSION_LIMIT" {
		t.Fatal(err)
	}
	tab := v3.NewID().String()
	if e = stream.Navigate(tab, "http://fixture.test/render?secret=not-in-logs"); e != nil {
		t.Fatal(e)
	}
	blue := awaitFrame(t, stream, func(f nodeclient.BrowserFrame) bool {
		r, g, b, _ := f.Image.At(700, 500).RGBA()
		return b > r*2 && b > g
	})
	t.Logf("first remote image: %s, %d JPEG bytes", time.Since(start), len(blue.Update.Data))
	click := time.Now()
	for _, kind := range []string{"down", "up"} {
		if e = stream.Input(tab, v3.Input{Kind: kind, Button: 1, X: 50, Y: 50}); e != nil {
			t.Fatal(e)
		}
	}
	green := awaitFrame(t, stream, func(f nodeclient.BrowserFrame) bool {
		r, g, b, _ := f.Image.At(700, 500).RGBA()
		return g > r*2 && g > b
	})
	t.Logf("input-to-visible: %s; capture+encode %d us; decode %d us", time.Since(click), green.Update.CaptureUS, green.DecodeUS)
	if dir := os.Getenv("WOLF_ARTIFACT_DIR"); dir != "" {
		if e = os.MkdirAll(dir, 0700); e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(filepath.Join(dir, "node-stream.jpg"), green.Update.Data, 0600); e != nil {
			t.Fatal(e)
		}
		file, e := os.OpenFile(filepath.Join(dir, "client-decoded.png"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if e != nil {
			t.Fatal(e)
		}
		e = png.Encode(file, green.Image)
		file.Close()
		if e != nil {
			t.Fatal(e)
		}
	}
	if e = stream.Resize(1024, 768); e != nil {
		t.Fatal(e)
	}
	awaitFrame(t, stream, func(f nodeclient.BrowserFrame) bool { return f.Update.Width == 1024 && f.Update.Height == 768 })
	if e = s.Auth.Revoke(c.Config.ClientID); e != nil {
		t.Fatal(e)
	}
	select {
	case <-stream.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("revocation did not close stream")
	}
	stream.Close()
	deadline := time.Now().Add(4 * time.Second)
	for s.Browser.Count() != 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if s.Browser.Count() != 0 {
		t.Fatal("browser session leaked")
	}
}
func awaitFrame(t *testing.T, b *nodeclient.Browser, predicate func(nodeclient.BrowserFrame) bool) nodeclient.BrowserFrame {
	t.Helper()
	timer := time.NewTimer(8 * time.Second)
	defer timer.Stop()
	for {
		select {
		case f := <-b.Frames:
			if predicate(f) {
				return f
			}
		case e := <-b.Events:
			if e.Type == v3.Error {
				t.Fatalf("browser error: %s", e.Control.Code)
			}
		case <-b.Done():
			t.Fatal("stream closed before expected frame")
		case <-timer.C:
			t.Fatal("pixel timeout")
		}
	}
}

func TestNativeClientBrowserScreenshot(t *testing.T) {
	bin, clientBin := os.Getenv("WOLF_BROWSER_BIN"), os.Getenv("WOLF_CLIENT_BIN")
	if bin == "" || clientBin == "" {
		t.Skip("required native smoke target sets browser/client binaries")
	}
	f, e := os.Open(bin)
	if e != nil {
		t.Fatal(e)
	}
	hash := sha256.New()
	_, _ = io.Copy(hash, f)
	f.Close()
	cfg := browser.DefaultConfig()
	cfg.Binary = bin
	cfg.SHA256 = hex.EncodeToString(hash.Sum(nil))
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><style>body{margin:0;background:#1555b0;font:20px sans-serif}button{position:absolute;left:20px;top:20px;width:180px;height:70px}input{position:absolute;left:20px;top:120px;width:300px}#label{position:absolute;left:230px;top:25px;color:white}</style><button onclick="document.body.style.background='#15b055'">WOLF REMOTE BROWSER</button><input oninput="document.getElementById('label').textContent=this.value"><div id="label">Pixels from the node</div>`)
	}))
	defer fixture.Close()
	root := filepath.Join(t.TempDir(), "node")
	s, e := node.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.EnableBrowser(cfg); e != nil {
		t.Fatal(e)
	}
	s.Browser.ProxyOptions = browsernet.Options{Resolver: resolver{}, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, fixture.Listener.Addr().String())
	}}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	defer func() {
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(5 * time.Second):
			t.Error("node shutdown timeout")
		}
	}()
	clientState := filepath.Join(t.TempDir(), "client")
	c, e := nodeclient.Load(clientState)
	if e != nil {
		t.Fatal(e)
	}
	c.Config.Port = ln.Addr().(*net.TCPAddr).Port
	c.Config.Fingerprint = s.Fingerprint
	code, e := s.Auth.IssueCode()
	if e != nil {
		t.Fatal(e)
	}
	if e = c.Enrol(ctx, code); e != nil {
		t.Fatal(e)
	}
	if e = c.Save(clientState); e != nil {
		t.Fatal(e)
	}
	dir := os.Getenv("WOLF_ARTIFACT_DIR")
	if dir == "" {
		dir = t.TempDir()
	}
	if e = os.MkdirAll(dir, 0700); e != nil {
		t.Fatal(e)
	}
	command := exec.CommandContext(ctx, clientBin, "-state-dir", clientState, "-browser-smoke", "http://fixture.test/pixels?token=fixture-secret", "-screenshot", filepath.Join(dir, "native-client.bmp"), "-smoke-timeout", "25s")
	command.Env = os.Environ()
	if os.Getenv("WOLF_NATIVE_DISPLAY") != "1" {
		command.Env = append(command.Env, "SDL_VIDEODRIVER=dummy", "SDL_RENDER_DRIVER=software")
	}
	output, e := command.CombinedOutput()
	if e != nil {
		t.Fatalf("native browser smoke: %v\n%s", e, output)
	}
	t.Log(string(output))
	for _, state := range []string{root, clientState} {
		paths, _ := filepath.Glob(filepath.Join(state, "logs", "*.jsonl"))
		for _, p := range paths {
			data, _ := os.ReadFile(p)
			for _, secret := range []string{"fixture-secret", "/pixels", "Привет", code} {
				if bytes.Contains(data, []byte(secret)) {
					t.Fatal("private browser data entered logs")
				}
			}
		}
	}
}

func TestV3AuthenticationWithoutBrowser(t *testing.T) {
	root := filepath.Join(t.TempDir(), "node")
	s, e := node.Open(root)
	if e != nil {
		t.Fatal(e)
	}
	if e = s.EnableBrowser(browser.DefaultConfig()); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
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
	if _, e = c.OpenBrowser(ctx, nodeclient.BrowserOptions{}); nodeclient.ErrorCode(e) != "BROWSER_UNAVAILABLE" {
		t.Fatal("missing browser did not fail closed", e)
	}
	c.Config.Fingerprint = strings.Repeat("0", 64)
	if _, e = c.OpenBrowser(ctx, nodeclient.BrowserOptions{}); nodeclient.ErrorCode(e) != "NODE_IDENTITY_CHANGED" {
		t.Fatal("wrong v3 pin accepted", e)
	}
}

func attemptAttach(t *testing.T, c nodeclient.Client, id v3.ID) string {
	t.Helper()
	conn, e := tls.Dial("tcp", c.Config.Address(), &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true})
	if e != nil {
		t.Fatal(e)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	send := func(typ byte, seq uint64, sid v3.ID, p v3.Control) {
		if e := v3.Write(conn, v3.Frame{Type: typ, Sequence: seq, Session: sid, Payload: v3.Encode(p)}); e != nil {
			t.Fatal(e)
		}
	}
	receive := func() (v3.Frame, v3.Control) {
		f, e := v3.Read(conn, false)
		if e != nil {
			t.Fatal(e)
		}
		p, e := v3.Decode(f)
		if e != nil {
			t.Fatal(e)
		}
		return f, p
	}
	send(v3.Hello, 1, v3.ID{}, v3.Control{Versions: []int{3}})
	receive()
	state := conn.ConnectionState()
	binding, e := state.ExportKeyingMaterial("WolfTunnel-v3", nil, 32)
	if e != nil {
		t.Fatal(e)
	}
	send(v3.Authenticate, 2, id, v3.Sign(c.Key, binding, id, v3.Control{ClientID: c.Config.ClientID, Role: "pixel"}))
	f, p := receive()
	if f.Type == v3.Error {
		return p.Code
	}
	send(v3.PixelAttach, 3, id, v3.Control{})
	_, p = receive()
	return p.Code
}
