package session_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"wolftunnel/internal/browser"
	"wolftunnel/internal/browsernet"
	"wolftunnel/internal/node"
	"wolftunnel/internal/nodeclient"
	v3 "wolftunnel/internal/protocol/v3"
	"wolftunnel/internal/transport"
)

// Delay is applied to each TLS socket read/write at the node, independently on
// both channels. This repeatable impairment is not a claim to test a public WAN.
type delayedConn struct {
	net.Conn
	delay time.Duration
}

func (c delayedConn) Read(p []byte) (int, error) {
	n, e := c.Conn.Read(p)
	if n > 0 {
		time.Sleep(c.delay)
	}
	return n, e
}
func (c delayedConn) Write(p []byte) (int, error) { time.Sleep(c.delay); return c.Conn.Write(p) }

type delayedListener struct {
	net.Listener
	delay time.Duration
}

func (l delayedListener) Accept() (net.Conn, error) {
	c, e := l.Listener.Accept()
	if e != nil {
		return nil, e
	}
	return delayedConn{c, l.delay}, nil
}

func performanceNode(t *testing.T, delay time.Duration) (*node.Server, *nodeclient.Client, context.Context) {
	t.Helper()
	cfg := browser.DefaultConfig()
	bin := os.Getenv("WOLF_BROWSER_BIN")
	if bin == "" {
		t.Skip("browser integration target required")
	}
	cfg.Binary = bin
	// Use the same pinned local executable as the other browser integration tests.
	cfg.SHA256 = browserHash(t, bin)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		io.WriteString(w, `<!doctype html><style>body{margin:0;background:#1555b0;font:20px sans-serif}button{position:absolute;left:20px;top:20px;width:180px;height:70px}</style><button onclick="window.n=(window.n||0)+1;document.body.style.background=window.n%2?'#15b055':'#1555b0'">Toggle</button>`)
	}))
	s, e := node.Open(filepath.Join(t.TempDir(), "node"))
	if e != nil {
		t.Fatal(e)
	}
	if e = s.EnableBrowser(cfg); e != nil {
		t.Fatal(e)
	}
	s.Browser.ProxyOptions = browsernet.Options{Resolver: resolver{}, Dial: func(ctx context.Context, n, a string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, n, fixture.Listener.Addr().String())
	}}
	ln, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, delayedListener{ln, delay}) }()
	t.Cleanup(func() {
		cancel()
		select {
		case e := <-done:
			if e != nil {
				t.Error(e)
			}
		case <-time.After(6 * time.Second):
			t.Error("node cleanup timed out")
		}
		fixture.Close()
	})
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
	return s, &c, ctx
}

type distribution struct{ P50, P95, P99, Max float64 }

func dist(values []float64) distribution {
	sort.Float64s(values)
	n := len(values)
	return distribution{values[n/2], values[int(math.Ceil(.95*float64(n)))-1], values[int(math.Ceil(.99*float64(n)))-1], values[n-1]}
}
func TestBrowserLatencyAndBoundedConsumer(t *testing.T) {
	for _, delay := range []time.Duration{0, 20 * time.Millisecond} {
		t.Run(fmt.Sprintf("socket_delay_%dms", delay.Milliseconds()), func(t *testing.T) {
			s, c, ctx := performanceNode(t, delay)
			start := time.Now()
			b, e := c.OpenBrowser(ctx, nodeclient.BrowserOptions{Width: 1920, Height: 1080})
			if e != nil {
				t.Fatal(e)
			}
			defer b.Close()
			tab := v3.NewID().String()
			if e = b.Navigate(tab, "http://fixture.test/performance"); e != nil {
				t.Fatal(e)
			}
			awaitFrame(t, b, func(f nodeclient.BrowserFrame) bool {
				r, g, blue, _ := f.Image.At(1800, 900).RGBA()
				return blue > r*2 && blue > g
			})
			first := time.Since(start).Seconds() * 1000
			latency, capture, decode, queue, dispatch, engine, rtt := []float64{}, []float64{}, []float64{}, []float64{}, []float64{}, []float64{}, []float64{}
			for n := 0; n < 24; n++ {
				started := time.Now()
				for _, kind := range []string{"down", "up"} {
					if e = b.Input(tab, v3.Input{Kind: kind, Button: 1, X: 50, Y: 50}); e != nil {
						t.Fatal(e)
					}
				}
				f := awaitFrame(t, b, func(f nodeclient.BrowserFrame) bool {
					r, g, blue, _ := f.Image.At(1800, 900).RGBA()
					if n%2 == 0 {
						return g > r*2 && g > blue
					}
					return blue > r*2 && blue > g
				})
				latency = append(latency, time.Since(started).Seconds()*1000)
				capture = append(capture, float64(f.Update.CaptureUS)/1000)
				decode = append(decode, float64(f.DecodeUS)/1000)
				m := b.Metrics()
				queue = append(queue, float64(m.InputQueueUS)/1000)
				dispatch = append(dispatch, float64(m.DispatchUS)/1000)
				engine = append(engine, float64(m.BrowserUS)/1000)
				rtt = append(rtt, float64(m.RoundTripUS)/1000)
			}
			// A stalled application consumer cannot grow the one-frame queue or block
			// authenticated control replies. Refreshes supersede previous visual states.
			for n := 0; n < 12; n++ {
				if e = b.Refresh(); e != nil {
					t.Fatal(e)
				}
				time.Sleep(80 * time.Millisecond)
			}
			if len(b.Frames) > 1 || cap(b.Frames) != 1 {
				t.Fatal("unbounded visual queue")
			}
			ping := time.Now()
			if e = b.Ping(); e != nil {
				t.Fatal(e)
			}
			timer := time.NewTimer(time.Second)
			defer timer.Stop()
		waitPong:
			for {
				select {
				case event := <-b.Events:
					if event.Type == v3.Pong {
						break waitPong
					}
					if event.Type == v3.Error {
						t.Fatal(event.Control.Code)
					}
				case <-timer.C:
					t.Fatal("control blocked by visual consumer")
				}
			}
			pongMS := time.Since(ping).Seconds() * 1000
			measureSeconds := time.Since(start).Seconds()
			cleanupStart := time.Now()
			b.Close()
			for s.Browser.Count() != 0 && time.Since(cleanupStart) < 4*time.Second {
				time.Sleep(10 * time.Millisecond)
			}
			if s.Browser.Count() != 0 {
				t.Fatal("session cleanup leak")
			}
			cleanupMS := time.Since(cleanupStart).Seconds() * 1000
			phases := map[string][]float64{}
			type checkpoint struct {
				state transport.State
				at    time.Time
			}
			last := map[string]checkpoint{}
			c.Observer = func(ev transport.Event) {
				now := time.Now()
				if prev, ok := last[ev.Attempt]; ok {
					phases[string(prev.state)] = append(phases[string(prev.state)], float64(now.Sub(prev.at).Microseconds())/1000)
				}
				last[ev.Attempt] = checkpoint{ev.State, now}
			}
			connectionSamples := []float64{}
			for n := 0; n < 24; n++ {
				at := time.Now()
				if _, e = c.Ping(ctx); e != nil {
					t.Fatal(e)
				}
				connectionSamples = append(connectionSamples, time.Since(at).Seconds()*1000)
			}
			phaseStats := map[string]distribution{}
			for name, samples := range phases {
				if len(samples) > 0 {
					phaseStats[name] = dist(samples)
				}
			}
			report := map[string]any{"stage_ms": phaseStats, "authenticated_ping_ms": dist(connectionSamples), "cleanup_ms": cleanupMS, "received_frames_per_second": float64(b.Metrics().Frames) / measureSeconds, "measurement_seconds": measureSeconds, "socket_delay_ms": delay.Milliseconds(), "samples": 24, "viewport": "1920x1080", "first_page_ms": first, "input_visible_ms": dist(latency), "capture_encode_ms": dist(capture), "decode_ms": dist(decode), "client_queue_ms": dist(queue), "node_overhead_ms": dist(dispatch), "browser_dispatch_ms": dist(engine), "control_ack_rtt_ms": dist(rtt), "slow_consumer_pong_ms": pongMS, "pixel_queue_capacity": cap(b.Frames), "stream": b.Metrics(), "sessions": s.Browser.Count()}
			raw, e := json.MarshalIndent(report, "", "  ")
			if e != nil {
				t.Fatal(e)
			}
			t.Log(string(raw))
			if dir := os.Getenv("WOLF_ARTIFACT_DIR"); dir != "" {
				if e = os.MkdirAll(dir, 0700); e != nil {
					t.Fatal(e)
				}
				if e = os.WriteFile(filepath.Join(dir, fmt.Sprintf("latency-%dms.json", delay.Milliseconds())), raw, 0600); e != nil {
					t.Fatal(e)
				}
			}
		})
	}
}
