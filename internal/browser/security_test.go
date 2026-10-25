package browser

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"wolftunnel/internal/browsernet"
	v3 "wolftunnel/internal/protocol/v3"
)

func evaluate(t *testing.T, b *Browser, ctx context.Context, tab, expression string) string {
	t.Helper()
	p, e := b.page(tab)
	if e != nil {
		t.Fatal(e)
	}
	var out struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if e = b.cdp.call(ctx, p.CDP, "Runtime.evaluate", map[string]any{"expression": expression, "returnByValue": true, "awaitPromise": true}, &out); e != nil {
		t.Fatal(e)
	}
	return out.Result.Value
}
func TestIsolationEgressFormsAndCrashRecovery(t *testing.T) {
	cfg := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	var form atomic.Int32
	trap := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("private redirect escaped policy") }))
	defer trap.Close()
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect":
			http.Redirect(w, r, "http://fixture.test/page", 302)
			return
		case "/blocked":
			http.Redirect(w, r, trap.URL, 302)
			return
		case "/slow":
			select {
			case <-r.Context().Done():
			case <-time.After(5 * time.Second):
			}
			return
		case "/form":
			if r.Method == "POST" {
				form.Add(1)
			}
		case "/ws":
			if !strings.EqualFold(r.Header.Get("Upgrade"), "websocket") {
				http.Error(w, "upgrade required", 400)
				return
			}
			key := sha1.Sum([]byte(r.Header.Get("Sec-WebSocket-Key") + "258EAFA5-E914-47DA-95CA-C5AB0DC85B11"))
			c, rw, e := w.(http.Hijacker).Hijack()
			if e != nil {
				return
			}
			defer c.Close()
			fmt.Fprintf(rw, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: %s\r\n\r\n", base64.StdEncoding.EncodeToString(key[:]))
			rw.Write([]byte{0x81, 7})
			rw.WriteString("wolf-ws")
			rw.Flush()
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><style>body{margin:0;height:3000px;background:#1555b0}input{position:absolute;top:120px;left:20px}</style><form method="post" action="/form"><input id="text" name="value"></form><script>window.points=0;document.onmousemove=()=>window.points++;window.tick=0;setInterval(()=>window.tick++,20)</script>`)
	}))
	defer fixture.Close()
	start := func() (*Browser, string) {
		p, e := browsernet.New(ctx, browsernet.Options{Resolver: testResolver{}, Dial: func(ctx context.Context, n, a string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, n, fixture.Listener.Addr().String())
		}})
		if e != nil {
			t.Fatal(e)
		}
		b, e := Start(ctx, cfg, p)
		if e != nil {
			p.Close()
			t.Fatal(e)
		}
		t.Cleanup(b.Close)
		tab := v3.NewID().String()
		if e = b.NewPage(ctx, tab); e != nil {
			t.Fatal(e)
		}
		if e = b.Select(ctx, tab, 800, 600); e != nil {
			t.Fatal(e)
		}
		if e = b.Navigate(ctx, tab, "http://fixture.test/redirect"); e != nil {
			t.Fatal(e)
		}
		// Navigation headers and page load complete asynchronously.
		deadline := time.Now().Add(3 * time.Second)
		for evaluate(t, b, ctx, tab, "String(!!document.getElementById('text'))") != "true" {
			if time.Now().After(deadline) {
				t.Fatal("redirect did not render")
			}
			time.Sleep(20 * time.Millisecond)
		}
		return b, tab
	}
	b, tab := start()
	if got := evaluate(t, b, ctx, tab, `document.cookie='wolf=isolated; SameSite=Strict';localStorage.setItem('wolf','isolated');'set'`); got != "set" {
		t.Fatal("state fixture failed")
	}
	if e := b.Input(ctx, v3.Input{Kind: "move", X: 100, Y: 100}); e != nil {
		t.Fatal(e)
	}
	if e := b.Input(ctx, v3.Input{Kind: "wheel", X: 400, Y: 400, DeltaY: 500}); e != nil {
		t.Fatal(e)
	}
	time.Sleep(150 * time.Millisecond)
	if evaluate(t, b, ctx, tab, "String(window.scrollY>0 && window.points>0 && window.tick>0)") != "true" {
		t.Fatal("wheel/pointer/timer failed")
	}
	if got := evaluate(t, b, ctx, tab, `new Promise(resolve=>{let w=new WebSocket('ws://fixture.test/ws');w.onmessage=e=>resolve(e.data);w.onerror=()=>resolve('failed');setTimeout(()=>resolve('timeout'),2000)})`); got != "wolf-ws" {
		t.Fatal("allowed WebSocket failed", got)
	}
	evaluate(t, b, ctx, tab, "window.scrollTo(0,0);document.getElementById('text').focus();'focused'")
	if e := b.Input(ctx, v3.Input{Kind: "text", Text: "ordinary form"}); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"key_down", "key_up"} {
		if e := b.Input(ctx, v3.Input{Kind: kind, Code: "Enter", Key: "Enter"}); e != nil {
			t.Fatal(e)
		}
	}
	deadline := time.Now().Add(2 * time.Second)
	for form.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	if form.Load() != 1 {
		t.Fatal("ordinary form did not submit")
	}
	// New page creation is bounded; web-created popup targets are closed.
	for n := 1; n < cfg.MaxTabs; n++ {
		if e := b.NewPage(ctx, v3.NewID().String()); e != nil {
			t.Fatal(e)
		}
	}
	if e := b.NewPage(ctx, v3.NewID().String()); e == nil || e.Error() != "TAB_LIMIT" {
		t.Fatal("tab limit not enforced", e)
	}
	evaluate(t, b, ctx, tab, `window.open('http://fixture.test/page');'popup'`)
	time.Sleep(100 * time.Millisecond)
	b.mu.Lock()
	pageCount := len(b.pages)
	b.mu.Unlock()
	if pageCount != cfg.MaxTabs {
		t.Fatal("popup mutated managed tabs")
	}
	// Redirects are checked at actual proxy dial, not only at the address bar.
	_ = b.Navigate(ctx, tab, "http://fixture.test/blocked")
	policy := false
	timer := time.NewTimer(2 * time.Second)
policyLoop:
	for {
		select {
		case ev := <-b.Events:
			if ev.Code == "NODE_POLICY_REJECTED" {
				policy = true
				break policyLoop
			}
		case <-timer.C:
			break policyLoop
		}
	}
	timer.Stop()
	if !policy {
		t.Fatal("private redirect was not distinguished from remote HTTP status")
	}
	// Internal node files are absent from the browser's filesystem namespace.
	secret := filepath.Join(t.TempDir(), "private-node-state")
	if e := os.WriteFile(secret, []byte("private"), 0600); e != nil {
		t.Fatal(e)
	}
	if got := evaluate(t, b, ctx, tab, fmt.Sprintf(`fetch(%q).then(r=>r.text()).then(()=> 'leaked').catch(()=> 'denied')`, "file://"+secret)); got != "denied" {
		t.Fatal("file boundary failed")
	}
	// A blocked navigation does not prevent cancellation from using the CDP pipe.
	navDone := make(chan error, 1)
	go func() { navDone <- b.Navigate(ctx, tab, "http://fixture.test/slow") }()
	time.Sleep(100 * time.Millisecond)
	bounded, stop := context.WithTimeout(ctx, time.Second)
	if e := b.Cancel(bounded, tab); e != nil {
		t.Fatal(e)
	}
	stop()
	select {
	case <-navDone:
	case <-time.After(2 * time.Second):
		t.Fatal("cancel did not unblock navigation")
	}
	// Crash the session deliberately, then launch a fresh isolated browser. No
	// persistent profile, cookie or local-storage reuse is allowed after recovery.
	if e := b.cmd.Process.Kill(); e != nil {
		t.Fatal(e)
	}
	select {
	case <-b.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("crash did not terminate browser")
	}
	b.Close()
	if _, e := os.Stat(b.Proxy.Dir); !errors.Is(e, os.ErrNotExist) {
		t.Fatal("proxy state leaked")
	}
	recovered, recoveredTab := start()
	if got := evaluate(t, recovered, ctx, recoveredTab, `String(document.cookie==='' && localStorage.getItem('wolf')===null)`); got != "true" {
		t.Fatal("browser state leaked across sessions")
	}
}
