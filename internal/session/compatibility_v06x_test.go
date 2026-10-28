package session_test

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"wolftunnel/internal/browser"
	"wolftunnel/internal/browsernet"
	"wolftunnel/internal/node"
	"wolftunnel/internal/nodeclient"
	v3 "wolftunnel/internal/protocol/v3"
	"wolftunnel/internal/testsite"
)

type compatibilityProfile struct {
	Name, Block               string
	Delay                     time.Duration
	Fragment                  int
	Truncate, Blackhole, Idle bool
}
type faultSocket struct {
	cut       *atomic.Bool
	trace     func(int, int)
	drop      *atomic.Bool
	truncated atomic.Bool
	net.Conn
	profile compatibilityProfile
	armed   atomic.Bool
	records atomic.Int32
	closed  chan struct{}
	once    sync.Once
}

func (s *faultSocket) Close() error { s.once.Do(func() { close(s.closed) }); return s.Conn.Close() }
func (s *faultSocket) Write(p []byte) (int, error) {
	if s.drop != nil && s.drop.Load() {
		return len(p), nil
	}
	if s.armed.Load() {
		n := s.records.Add(1)
		if s.trace != nil && n < 8 {
			s.trace(int(n), len(p))
		}
		if n > 2 {
			if s.profile.Truncate && s.cut != nil && s.cut.Load() {
				if !s.truncated.Swap(true) {
					s.Conn.Write(p[:max(1, len(p)/2)])
					if tcp, ok := s.Conn.(*net.TCPConn); ok {
						tcp.CloseWrite()
					}
				}
				return len(p), nil
			}
			if s.profile.Blackhole {
				return len(p), nil
			}
		}
	}
	if s.profile.Fragment > 0 {
		written := 0
		for len(p) > 0 {
			n, e := s.Conn.Write(p[:min(len(p), s.profile.Fragment)])
			written += n
			p = p[n:]
			if e != nil {
				return written, e
			}
			if n == 0 {
				return written, io.ErrNoProgress
			}
		}
		return written, nil
	}
	return s.Conn.Write(p)
}

// This reader runs INSIDE the developer-controlled TLS endpoint. It sees only
// authenticated test traffic after decryption; it is not an on-path pin bypass.
type inspectionReader struct {
	ctx     context.Context
	peer    *tls.Conn
	socket  *faultSocket
	pending bytes.Buffer
	profile compatibilityProfile
}

func (r *inspectionReader) Read(p []byte) (int, error) {
	if r.pending.Len() > 0 {
		return r.pending.Read(p)
	}
	f, e := v3.Read(r.peer, false)
	if e != nil {
		return 0, e
	}
	c, e := v3.Decode(f)
	if e != nil {
		return 0, e
	}
	if r.profile.Block == "session_create" && f.Type == v3.SessionCreate {
		return 0, io.EOF
	}
	if r.profile.Block == "pixel_auth" && f.Type == v3.Authenticate && c.Role == "pixel" {
		return 0, io.EOF
	}
	if r.profile.Delay > 0 && f.Type == v3.SessionCreate {
		timer := time.NewTimer(r.profile.Delay)
		defer timer.Stop()
		select {
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		case <-timer.C:
		}
	}
	if f.Type == v3.PixelAttach {
		r.socket.armed.Store(true)
	}
	if e = v3.Write(&r.pending, f); e != nil {
		return 0, e
	}
	return r.pending.Read(p)
}

type scenarioResult struct {
	Name, Expected, Actual, Control, Display string
	Repeat                                   int
	ElapsedMS, CleanupMS                     int64
	Sessions                                 int
}

func TestControlledInspectionProfiles(t *testing.T) {
	bin := os.Getenv("WOLF_BROWSER_BIN")
	if bin == "" {
		t.Skip("browser test target required")
	}
	profiles := []struct {
		p    compatibilityProfile
		want string
	}{
		{compatibilityProfile{Name: "normal"}, "ok"},
		{compatibilityProfile{Name: "fragmented_tls", Fragment: 31}, "ok"},
		{compatibilityProfile{Name: "delayed_session_readiness", Delay: 350 * time.Millisecond}, "ok"},
		{compatibilityProfile{Name: "protocol_blacklist_session_create", Block: "session_create"}, "PROTOCOL_ERROR"},
		{compatibilityProfile{Name: "protocol_blacklist_display", Block: "pixel_auth"}, "DISPLAY_CONNECTION_FAILED"},
		{compatibilityProfile{Name: "interrupted_image_record", Truncate: true}, "DISPLAY_FRAME_TRUNCATED"},
		{compatibilityProfile{Name: "first_frame_blackhole", Blackhole: true}, "FIRST_FRAME_TIMEOUT"},
		{compatibilityProfile{Name: "idle_frame_blackhole", Idle: true}, "FRAME_IDLE_TIMEOUT"},
	}
	var results []scenarioResult
	beforeGoroutines := runtime.NumGoroutine()
	for _, profile := range profiles {
		repeats := 1
		if profile.p.Block == "pixel_auth" || profile.p.Blackhole || profile.p.Truncate {
			repeats = 3
		}
		for repeat := 0; repeat < repeats; repeat++ {
			t.Run(profile.p.Name+string(rune('0'+repeat)), func(t *testing.T) {
				cfg := browser.DefaultConfig()
				cfg.Binary = bin
				cfg.SHA256 = browserHash(t, bin)
				s, e := node.Open(filepath.Join(t.TempDir(), "node"))
				if e != nil {
					t.Fatal(e)
				}
				if e = s.EnableBrowser(cfg); e != nil {
					t.Fatal(e)
				}
				fixture := httptest.NewServer(&testsite.Site{})
				defer fixture.Close()
				s.Browser.ProxyOptions = browsernet.Options{Resolver: resolver{}, Dial: func(c context.Context, n, a string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(c, n, fixture.Listener.Addr().String())
				}}
				c, e := nodeclient.Load(filepath.Join(t.TempDir(), "client"))
				if e != nil {
					t.Fatal(e)
				}
				code, _ := s.Auth.IssueCode()
				record, e := s.Auth.Enrol(c.Key.Public().(ed25519.PublicKey), code)
				if e != nil {
					t.Fatal(e)
				}
				c.Config.ClientID = record.ClientID
				c.Config.Fingerprint = s.Fingerprint
				c.Config.Retries = 0
				ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
				defer cancel()
				ln, e := net.Listen("tcp", "127.0.0.1:0")
				if e != nil {
					t.Fatal(e)
				}
				c.Config.Port = ln.Addr().(*net.TCPAddr).Port
				var drop, cut atomic.Bool
				var wg sync.WaitGroup
				acceptDone := make(chan struct{})
				go func() {
					defer close(acceptDone)
					for {
						raw, e := ln.Accept()
						if e != nil {
							return
						}
						wg.Add(1)
						go func() {
							defer wg.Done()
							sock := &faultSocket{trace: func(n, size int) {
								if profile.p.Truncate || profile.p.Fragment > 0 {
									t.Logf("pixel TLS write record=%d bytes=%d", n, size)
								}
							}, Conn: raw, profile: profile.p, drop: &drop, cut: &cut, closed: make(chan struct{})}
							defer sock.Close()
							stop := context.AfterFunc(ctx, func() { sock.Close() })
							defer stop()
							peer := tls.Server(sock, s.TLSConfig())
							peer.SetDeadline(time.Now().Add(5 * time.Second))
							if peer.HandshakeContext(ctx) != nil {
								return
							}
							s.Browser.Handle(ctx, peer, &inspectionReader{ctx: ctx, peer: peer, socket: sock, profile: profile.p})
						}()
					}
				}()
				cleanup := func() { cancel(); ln.Close(); <-acceptDone; s.Browser.Close(); wg.Wait() }
				defer cleanup()
				at := time.Now()
				firstBudget := 4 * time.Second
				if profile.p.Blackhole {
					firstBudget = 700 * time.Millisecond
				}
				idleBudget := 4 * time.Second
				if profile.p.Idle {
					idleBudget = 700 * time.Millisecond
				}
				stream, e := c.OpenBrowser(ctx, nodeclient.BrowserOptions{Width: 800, Height: 600, FirstFrameTimeout: firstBudget, FrameIdleTimeout: idleBudget})
				actual := nodeclient.ErrorCode(e)
				control, display := "", ""
				if e != nil {
					var f *nodeclient.LifecycleError
					if errors.As(e, &f) {
						control, display = f.Status.Control, f.Status.Display
					}
					if profile.p.Block == "pixel_auth" && control != "responsive" {
						t.Fatalf("healthy control misclassified: %v %+v", e, f)
					}
				}
				if stream != nil {
					if e = stream.Navigate(v3.NewID().String(), "http://fixture.test/interaction"); e != nil {
						stream.Close()
						t.Fatal(e)
					}
					if profile.p.Truncate {
						awaitFrame(t, stream, func(f nodeclient.BrowserFrame) bool { return f.Image != nil })
						cut.Store(true)
					}
					if profile.p.Idle {
						awaitFrame(t, stream, func(f nodeclient.BrowserFrame) bool { return f.Image != nil })
						drop.Store(true)
					}
					if profile.want == "ok" {
						awaitFrame(t, stream, func(f nodeclient.BrowserFrame) bool { return f.Image != nil })
						actual = "ok"
					} else {
						timer := time.NewTimer(5 * time.Second)
						defer timer.Stop()
					failed:
						for {
							select {
							case event := <-stream.Events:
								if event.Type == v3.Error {
									actual = event.Control.Code
									break failed
								}
							case <-timer.C:
								stream.Close()
								t.Fatal("bounded injected failure not detected")
							}
						}
					}
					state := stream.Status()
					control, display = state.Control, state.Display
					stream.Close()
				}
				elapsed := time.Since(at)
				startCleanup := time.Now()
				deadline := time.Now().Add(3 * time.Second)
				for s.Browser.Count() != 0 && time.Now().Before(deadline) {
					time.Sleep(10 * time.Millisecond)
				}
				if actual != profile.want || s.Browser.Count() != 0 {
					t.Fatalf("profile=%s got=%s want=%s sessions=%d", profile.p.Name, actual, profile.want, s.Browser.Count())
				}
				if elapsed > 10*time.Second {
					t.Fatal("scenario exceeded bound", elapsed)
				}
				results = append(results, scenarioResult{profile.p.Name, profile.want, actual, control, display, repeat, elapsed.Milliseconds(), time.Since(startCleanup).Milliseconds(), s.Browser.Count()})
			})
		}
	}
	runtime.GC()
	if growth := runtime.NumGoroutine() - beforeGoroutines; growth > 16 {
		t.Fatal("unbounded goroutine growth", growth)
	}
	if dir := os.Getenv("WOLF_ARTIFACT_DIR"); dir != "" {
		os.MkdirAll(dir, 0700)
		data, _ := json.MarshalIndent(results, "", "  ")
		if e := os.WriteFile(filepath.Join(dir, "compatibility-scenarios.json"), data, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
