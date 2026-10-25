package browser

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"wolftunnel/internal/browsernet"
	v3 "wolftunnel/internal/protocol/v3"
)

type workerResolver struct{}

func (workerResolver) LookupNetIP(_ context.Context, _ string, host string) ([]netip.Addr, error) {
	if host == "rebound.test" {
		return []netip.Addr{netip.MustParseAddr("169.254.169.254")}, nil
	}
	return []netip.Addr{netip.MustParseAddr("93.184.216.34")}, nil
}
func TestHTTPSServiceWorkerEgressPolicy(t *testing.T) {
	cfg := integrationConfig(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	fixture := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/sw.js" {
			w.Header().Set("Content-Type", "application/javascript")
			io.WriteString(w, `self.addEventListener('install',e=>self.skipWaiting());self.addEventListener('activate',e=>e.waitUntil(self.clients.claim()));self.addEventListener('message',e=>e.waitUntil(fetch('https://rebound.test/private').then(()=>e.ports[0].postMessage('leaked')).catch(()=>e.ports[0].postMessage('denied'))));`)
			return
		}
		w.Header().Set("Content-Type", "text/html")
		io.WriteString(w, `<!doctype html><title>Service worker fixture</title><p>HTTPS on the node</p>`)
	}))
	defer fixture.Close()
	p, e := browsernet.New(ctx, browsernet.Options{Resolver: workerResolver{}, Dial: func(ctx context.Context, n, a string) (net.Conn, error) {
		if !strings.HasPrefix(a, "93.184.216.34:") {
			t.Error("unchecked worker dial")
		}
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
	defer b.Close()
	// Controlled self-signed fixture only. Production has no certificate bypass
	// option/API; browser TLS verification remains enabled by default.
	if e = b.cdp.call(ctx, "", "Security.setIgnoreCertificateErrors", map[string]bool{"ignore": true}, nil); e != nil {
		t.Fatal(e)
	}
	tab := v3.NewID().String()
	if e = b.NewPage(ctx, tab); e != nil {
		t.Fatal(e)
	}
	if e = b.Select(ctx, tab, 800, 600); e != nil {
		t.Fatal(e)
	}
	if e = b.Navigate(ctx, tab, "https://fixture.test/"); e != nil {
		t.Fatal(e)
	}
	time.Sleep(100 * time.Millisecond)
	if got := evaluate(t, b, ctx, tab, `(async()=>{for(let name of ['camera','microphone','geolocation','notifications','clipboard-read','clipboard-write']){let permission=await navigator.permissions.query({name});if(permission.state!=='denied')return name+':'+permission.state;}return 'denied'})()`); got != "denied" {
		t.Fatal("browser permissions were not denied", got)
	}
	got := evaluate(t, b, ctx, tab, `new Promise(async resolve=>{let timeout=setTimeout(()=>resolve('timeout'),4000);try{await navigator.serviceWorker.register('/sw.js');let registration=await navigator.serviceWorker.ready;let channel=new MessageChannel();channel.port1.onmessage=e=>{clearTimeout(timeout);resolve(e.data)};registration.active.postMessage('test',[channel.port2]);}catch(e){resolve('registration-failed')}})`)
	if got != "denied" || p.Rejected.Load() == 0 {
		t.Fatalf("service worker policy failed: result=%s rejected=%d", got, p.Rejected.Load())
	}
}
