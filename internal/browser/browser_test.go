package browser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wolftunnel/internal/browsernet"
	v3 "wolftunnel/internal/protocol/v3"
)

type testResolver struct{}

func (testResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}
func integrationConfig(t *testing.T) Config {
	t.Helper()
	bin := os.Getenv("WOLF_BROWSER_BIN")
	if bin == "" {
		t.Skip("set WOLF_BROWSER_BIN for required browser integration target")
	}
	f, e := os.Open(bin)
	if e != nil {
		t.Fatal(e)
	}
	h := sha256.New()
	_, e = io.Copy(h, f)
	f.Close()
	if e != nil {
		t.Fatal(e)
	}
	c := DefaultConfig()
	c.Binary = bin
	c.SHA256 = hex.EncodeToString(h.Sum(nil))
	return c
}
func TestRealBrowserRenderingAndInput(t *testing.T) {
	c := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	var forbidden atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { forbidden.Add(1); http.Error(w, "forbidden", 500) }))
	defer trap.Close()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><style>body{margin:0;background:#1555b0}button{position:absolute;left:20px;top:20px;width:180px;height:70px}input{position:absolute;left:20px;top:120px}</style><button onclick="document.body.style.background='#15b055'">CLICK TEST</button><input id="text"><script>document.title='JavaScript active'</script>`)
	}))
	defer fixture.Close()
	p, e := browsernet.New(ctx, browsernet.Options{Resolver: testResolver{}, Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, fixture.Listener.Addr().String())
	}})
	if e != nil {
		t.Fatal(e)
	}
	b, e := Start(ctx, c, p)
	if e != nil {
		p.Close()
		var debug *startupError
		if errors.As(e, &debug) {
			t.Log(debug.diagnostic)
		}
		var command *commandError
		if errors.As(e, &command) {
			t.Logf("startup method %s: code %d", command.method, command.code)
		}
		t.Fatal(e)
	}
	defer b.Close()
	id := v3.NewID().String()
	if e = b.NewPage(ctx, id); e != nil {
		t.Fatal(e)
	}
	if e = b.Select(ctx, id, 800, 600); e != nil {
		t.Fatal(e)
	}
	if e = b.Navigate(ctx, id, "http://fixture.test/page?secret=never-log"); e != nil {
		var nav *navigationError
		if errors.As(e, &nav) {
			t.Logf("navigation category %s; proxy rejects=%d", nav.cause, p.Rejected.Load())
		}
		t.Fatal(e)
	}
	deadline := time.Now().Add(5 * time.Second)
	var raw []byte
	for time.Now().Before(deadline) {
		raw, _, _, e = b.Capture(ctx)
		if e == nil && len(raw) > 0 {
			img, _, err := image.Decode(bytes.NewReader(raw))
			if err == nil {
				r, g, blue, _ := img.At(700, 500).RGBA()
				if blue > r*2 && blue > g {
					break
				}
			}
		}
		time.Sleep(30 * time.Millisecond)
	}
	img, _, e := image.Decode(bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	r, g, blue, _ := img.At(700, 500).RGBA()
	if !(blue > r*2 && blue > g) {
		t.Fatal("fixture not rendered")
	}
	for _, kind := range []string{"down", "up"} {
		if e = b.Input(ctx, v3.Input{Kind: kind, Button: 1, X: 50, Y: 50}); e != nil {
			t.Fatal(e)
		}
	}
	time.Sleep(100 * time.Millisecond)
	raw, _, _, e = b.Capture(ctx)
	if e != nil {
		t.Fatal(e)
	}
	img, _, e = image.Decode(bytes.NewReader(raw))
	if e != nil {
		t.Fatal(e)
	}
	r, g, blue, _ = img.At(700, 500).RGBA()
	if !(g > r*2 && g > blue) {
		t.Fatal("mouse did not execute page JavaScript")
	}
	if e = b.Navigate(ctx, id, "http://127.0.0.1/"); e == nil {
		t.Fatal("private navigation accepted")
	}

	// International text and ordered physical key down/up through the page API.
	for _, kind := range []string{"down", "up"} {
		if e = b.Input(ctx, v3.Input{Kind: kind, Button: 1, X: 50, Y: 130}); e != nil {
			t.Fatal(e)
		}
	}
	if e = b.Input(ctx, v3.Input{Kind: "text", Text: "✓ Привет"}); e != nil {
		t.Fatal(e)
	}
	page, _ := b.page(id)
	var result struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if e = b.cdp.call(ctx, page.CDP, "Runtime.evaluate", map[string]any{"expression": "document.getElementById('text').value", "returnByValue": true}, &result); e != nil || result.Result.Value != "✓ Привет" {
		t.Fatal("international text was not applied")
	}
	for _, i := range []v3.Input{{Kind: "key_down", Code: "ControlLeft", Key: "Control", Modifiers: 2}, {Kind: "key_down", Code: "KeyA", Key: "a", Modifiers: 2}, {Kind: "key_up", Code: "KeyA", Key: "a", Modifiers: 2}, {Kind: "key_up", Code: "ControlLeft", Key: "Control"}} {
		if e = b.Input(ctx, i); e != nil {
			t.Fatal(e)
		}
	}
	if e = b.Input(ctx, v3.Input{Kind: "text", Text: "Final ✓"}); e != nil {
		t.Fatal(e)
	}
	if e = b.cdp.call(ctx, page.CDP, "Runtime.evaluate", map[string]any{"expression": "document.getElementById('text').value", "returnByValue": true}, &result); e != nil || result.Result.Value != "Final ✓" {
		t.Fatal("physical modifier/release ordering failed")
	}
	// Script, image, frame and WebSocket requests must not escape the network namespace.
	expression := fmt.Sprintf(`(()=>{for(const tag of ['img','iframe','script']){const e=document.createElement(tag);e.src=%q+'/forbidden';document.body.appendChild(e)};fetch(%q+'/forbidden').catch(()=>{});try{new WebSocket(%q+'/forbidden')}catch{};return true})()`, trap.URL, trap.URL, "ws"+strings.TrimPrefix(trap.URL, "http"))
	if e = b.cdp.call(ctx, page.CDP, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true}, nil); e != nil {
		t.Fatal(e)
	}
	time.Sleep(200 * time.Millisecond)
	if forbidden.Load() != 0 {
		t.Fatal("browser reached host loopback services")
	}
	// A separate tab has its own page and viewport; resources remain one process scope.
	tab2 := v3.NewID().String()
	if e = b.NewPage(ctx, tab2); e != nil {
		t.Fatal(e)
	}
	if e = b.Select(ctx, tab2, 1024, 768); e != nil {
		t.Fatal(e)
	}
	if e = b.Navigate(ctx, tab2, "http://fixture.test/second"); e != nil {
		t.Fatal(e)
	}
	if e = b.Select(ctx, id, 800, 600); e != nil {
		t.Fatal(e)
	}
	stats, e := b.Resources(ctx)
	if e != nil || stats.Tasks == 0 || stats.Memory > uint64(c.MemoryMB)<<20 {
		t.Fatal("resource scope not enforced", e)
	}
	t.Logf("resource scope: memory=%d peak=%d tasks=%d cpu_ns=%d", stats.Memory, stats.MemoryPeak, stats.Tasks, stats.CPUTimeNS)
	t.Logf("full JPEG: %d bytes", len(raw))
}
