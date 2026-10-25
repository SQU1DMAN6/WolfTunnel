// Package session owns authenticated browser lifecycles and channel association.
package session

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"net/url"
	"sync"
	"sync/atomic"
	"time"

	"wolftunnel/internal/audit"
	"wolftunnel/internal/auth"
	"wolftunnel/internal/browser"
	"wolftunnel/internal/browsernet"
	"wolftunnel/internal/pixelstream"
	v3 "wolftunnel/internal/protocol/v3"
)

type wire struct {
	c   net.Conn
	r   io.Reader
	mu  sync.Mutex
	out uint64
	in  uint64
}

func (w *wire) read() (v3.Frame, v3.Control, error) {
	f, e := v3.Read(w.r, false)
	if e != nil {
		return f, v3.Control{}, e
	}
	if f.Sequence != w.in+1 {
		return f, v3.Control{}, v3.ErrFrame
	}
	w.in = f.Sequence
	c, e := v3.Decode(f)
	return f, c, e
}
func (w *wire) send(typ byte, id v3.ID, c v3.Control) error {
	return w.frame(v3.Frame{Type: typ, Session: id, Payload: v3.Encode(c)})
}
func (w *wire) frame(f v3.Frame) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.out++
	f.Sequence = w.out
	_ = w.c.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return v3.Write(w.c, f)
}

type Manager struct {
	Config       browser.Config
	Auth         *auth.Store
	Log          *audit.Logger
	Fingerprint  string
	ProxyOptions browsernet.Options
	mu           sync.Mutex
	sessions     map[v3.ID]*Session
}

func New(c browser.Config, a *auth.Store, log *audit.Logger, fp string) *Manager {
	return &Manager{Config: c, Auth: a, Log: log, Fingerprint: fp, sessions: make(map[v3.ID]*Session)}
}
func (m *Manager) Close() {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()
	for _, s := range sessions {
		s.close()
	}
}
func (m *Manager) Count() int { m.mu.Lock(); defer m.mu.Unlock(); return len(m.sessions) }
func (m *Manager) log(event, client string, id v3.ID, code string) {
	if m.Log != nil {
		m.Log.Log(audit.Entry{Event: event, ClientID: client, SessionID: id.String(), Code: v3.SafeCode(code)})
	}
}
func (m *Manager) fail(w *wire, id v3.ID, client, code string) {
	code = v3.SafeCode(code)
	m.log("browser_error", client, id, code)
	_ = w.send(v3.Error, id, v3.Control{Code: code})
}
func (m *Manager) Handle(parent context.Context, c *tls.Conn, r io.Reader) {
	w := &wire{c: c, r: r}
	f, hello, e := w.read()
	if e != nil || f.Type != v3.Hello || !f.Session.Zero() {
		m.fail(w, v3.ID{}, "", "PROTOCOL_ERROR")
		return
	}
	supported := false
	for _, v := range hello.Versions {
		if v == 3 {
			supported = true
		}
	}
	if !supported {
		m.fail(w, v3.ID{}, "", "UNSUPPORTED_VERSION")
		return
	}
	if w.send(v3.NodeIdentity, v3.ID{}, v3.Control{Version: 3, Fingerprint: m.Fingerprint, Capabilities: []string{"remote-browser", "jpeg", "two-channels", "tabs", "resize", "input"}}) != nil {
		return
	}
	f, a, e := w.read()
	if e != nil || f.Type != v3.Authenticate || (a.Role != "control" && a.Role != "pixel") {
		m.fail(w, f.Session, "", "AUTH_FAILED")
		return
	}
	cs := c.ConnectionState()
	binding, e := cs.ExportKeyingMaterial("WolfTunnel-v3", nil, 32)
	if e != nil {
		return
	}
	record, e := m.Auth.Lookup(a.ClientID)
	if e != nil || !ed25519.Verify(record.PublicKey, v3.Proof(binding, f.Session, a), a.Signature) || (a.Role == "control" && !f.Session.Zero()) || (a.Role == "pixel" && f.Session.Zero()) {
		m.fail(w, f.Session, "", "AUTH_FAILED")
		return
	}
	if w.send(v3.AuthResult, f.Session, v3.Control{ClientID: a.ClientID}) != nil {
		return
	}
	m.log("browser_channel_authenticated", a.ClientID, f.Session, "")
	if a.Role == "pixel" {
		m.attach(parent, w, a.ClientID, f.Session)
		return
	}
	f, create, e := w.read()
	if e != nil || f.Type != v3.SessionCreate || !f.Session.Zero() {
		m.fail(w, f.Session, a.ClientID, "PROTOCOL_ERROR")
		return
	}
	width, height := create.Width, create.Height
	if width == 0 && height == 0 {
		width, height = 1920, 1080
	}
	cfg := m.Config
	if !cfg.Valid() || cfg.Binary == "" {
		m.fail(w, v3.ID{}, a.ClientID, "BROWSER_UNAVAILABLE")
		return
	}
	if !cfg.Viewport(width, height) {
		m.fail(w, v3.ID{}, a.ClientID, "VIEWPORT_REJECTED")
		return
	}
	if create.Quality != 0 {
		if create.Quality < 40 || create.Quality > 95 {
			m.fail(w, v3.ID{}, a.ClientID, "PROTOCOL_ERROR")
			return
		}
		cfg.Quality = create.Quality
	}
	if create.FPS != 0 {
		if create.FPS < 1 || create.FPS > cfg.FPS {
			m.fail(w, v3.ID{}, a.ClientID, "PROTOCOL_ERROR")
			return
		}
		cfg.FPS = create.FPS
	}
	m.mu.Lock()
	owned := 0
	for _, s := range m.sessions {
		if s.owner == a.ClientID {
			owned++
		}
	}
	if len(m.sessions) >= cfg.MaxSessions || owned >= cfg.MaxPerClient {
		m.mu.Unlock()
		m.fail(w, v3.ID{}, a.ClientID, "SESSION_LIMIT")
		return
	}
	ctx, cancel := context.WithCancel(parent)
	s := &Session{id: v3.NewID(), owner: a.ClientID, m: m, ctx: ctx, cancel: cancel, ctrl: w, ready: make(chan struct{}), latest: make(chan []byte, 1), events: make(chan response, 32), width: width, height: height}
	s.navSlots = make(chan struct{}, 2)
	s.activity.Store(time.Now().UnixNano())
	s.started = time.Now()
	m.sessions[s.id] = s
	m.mu.Unlock()
	defer s.close()
	proxy, e := browsernet.New(ctx, m.ProxyOptions)
	if e != nil {
		m.fail(w, s.id, s.owner, "BROWSER_START_FAILED")
		return
	}
	_ = c.SetDeadline(time.Now().Add(cfg.Startup + 5*time.Second))
	launched, startError := browser.Start(ctx, cfg, proxy)
	e = startError
	if e != nil {
		proxy.Close()
		m.fail(w, s.id, s.owner, e.Error())
		return
	}
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		launched.Close()
		return
	}
	s.browser = launched
	s.mu.Unlock()
	if _, e = m.Auth.Lookup(s.owner); e != nil {
		m.fail(w, s.id, s.owner, "AUTH_FAILED")
		return
	}
	if w.send(v3.SessionCreated, s.id, v3.Control{Width: width, Height: height, MaxTabs: cfg.MaxTabs}) != nil {
		return
	}
	_ = c.SetDeadline(time.Time{})
	m.log("browser_session_created", s.owner, s.id, "")
	s.mu.Lock()
	if s.ctx.Err() != nil {
		s.mu.Unlock()
		return
	}
	s.wg.Add(3)
	go s.writeControl()
	go s.monitor()
	go s.capture()
	s.mu.Unlock()
	s.control()
}

type response struct {
	typ  byte
	body v3.Control
}
type Session struct {
	started       time.Time
	frames        atomic.Uint64
	pixelBytes    atomic.Uint64
	dropped       atomic.Uint64
	navSlots      chan struct{}
	id            v3.ID
	owner         string
	m             *Manager
	ctx           context.Context
	cancel        context.CancelFunc
	ctrl          *wire
	browser       *browser.Browser
	mu            sync.Mutex
	pixel         *wire
	ready         chan struct{}
	latest        chan []byte
	events        chan response
	width, height int
	once          sync.Once
	wg            sync.WaitGroup
	activity      atomic.Int64
	lastInput     atomic.Uint64
	force         atomic.Bool
}

func (s *Session) close() {
	s.once.Do(func() {
		s.cancel()
		s.ctrl.c.Close()
		s.mu.Lock()
		if s.pixel != nil {
			s.pixel.c.Close()
		}
		br := s.browser
		s.mu.Unlock()
		if br != nil {
			br.Close()
		}
		s.wg.Wait()
		s.m.mu.Lock()
		delete(s.m.sessions, s.id)
		s.m.mu.Unlock()
		if s.m.Log != nil {
			s.m.Log.Log(audit.Entry{Event: "browser_session_closed", ClientID: s.owner, SessionID: s.id.String(), DurationMS: time.Since(s.started).Milliseconds(), Frames: s.frames.Load(), PixelBytes: s.pixelBytes.Load(), Dropped: s.dropped.Load()})
		}
	})
}
func (s *Session) emit(typ byte, c v3.Control) {
	if c.Code != "" {
		s.m.log("browser_operation_result", s.owner, s.id, c.Code)
	}
	select {
	case s.events <- response{typ, c}:
	case <-s.ctx.Done():
	default:
		s.cancel()
		s.ctrl.c.Close()
	}
}
func (s *Session) terminal(code string) {
	s.m.log("browser_session_failed", s.owner, s.id, code)
	// Deliver the fixed failure category before closing the channel. Enqueueing
	// then immediately closing could hide a crash/revocation as a network error.
	_ = s.ctrl.send(v3.Error, s.id, v3.Control{Code: code})
	s.cancel()
	s.ctrl.c.Close()
}
func (s *Session) writeControl() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case e := <-s.events:
			if s.ctrl.send(e.typ, s.id, e.body) != nil {
				s.cancel()
				s.ctrl.c.Close()
				return
			}
		}
	}
}
func (s *Session) monitor() {
	defer s.wg.Done()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.browser.Done():
			s.terminal("BROWSER_CRASHED")
			return
		case e := <-s.browser.Events:
			s.emit(v3.NavigationStatus, v3.Control{Tab: e.Tab, State: e.State, Code: v3.SafeCode(e.Code), Status: e.Status, Target: e.Target})
		case <-ticker.C:
			if _, e := s.m.Auth.Lookup(s.owner); e != nil {
				s.m.log("browser_authorisation_lost", s.owner, s.id, "AUTH_FAILED")
				s.terminal("AUTH_FAILED")
				return
			}
			if time.Since(time.Unix(0, s.activity.Load())) > s.m.Config.Idle {
				s.cancel()
				s.ctrl.c.Close()
				return
			}
		}
	}
}
func (s *Session) capture() {
	defer s.wg.Done()
	select {
	case <-s.ready:
	case <-s.ctx.Done():
		return
	case <-time.After(5 * time.Second):
		s.cancel()
		s.ctrl.c.Close()
		return
	}
	ticker := time.NewTicker(time.Second / time.Duration(s.browser.Config.FPS))
	defer ticker.Stop()
	var previous [32]byte
	last := time.Time{}
	failures := 0
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(s.ctx, 3*time.Second)
			start := time.Now()
			applied := s.lastInput.Load()
			data, w, h, tab, e := s.browser.CapturePage(ctx)
			cancel()
			if e != nil {
				failures++
				if failures >= 3 {
					s.terminal("PIXEL_ERROR")
					return
				}
				continue
			}
			failures = 0
			if len(data) == 0 {
				continue
			}
			hash := sha256.Sum256(data)
			forced := s.force.Swap(false)
			if hash == previous && !forced && time.Since(last) < s.browser.Config.Refresh {
				continue
			}
			previous = hash
			last = time.Now()
			tabID, parseError := v3.ParseID(tab)
			if parseError != nil {
				continue
			}
			payload, e := (pixelstream.Update{Tab: tabID, Width: w, Height: h, Encoding: 1, Captured: start, InputSequence: applied, CaptureUS: uint32(time.Since(start).Microseconds()), Data: data}).Encode()
			if e != nil || len(payload) > s.browser.Config.MaxFrameBytes {
				s.terminal("PIXEL_ERROR")
				return
			}
			select {
			case s.latest <- payload:
			default:
				select {
				case <-s.latest:
					s.dropped.Add(1)
				default:
				}
				select {
				case s.latest <- payload:
				default:
				}
			}
		}
	}
}
func (s *Session) control() {
	inputWindow := time.Now()
	inputCount := 0
	for {
		s.ctrl.c.SetReadDeadline(time.Now().Add(30 * time.Second))
		f, c, e := s.ctrl.read()
		received := time.Now()
		if e != nil {
			return
		}
		if f.Session != s.id {
			_ = s.ctrl.send(v3.Error, s.id, v3.Control{Code: "SESSION_INVALID"})
			return
		}
		if _, e = s.m.Auth.Lookup(s.owner); e != nil {
			_ = s.ctrl.send(v3.Error, s.id, v3.Control{Code: "AUTH_FAILED"})
			return
		}
		if time.Since(inputWindow) >= time.Second {
			inputWindow = time.Now()
			inputCount = 0
		}
		inputCount++
		if inputCount > 2000 {
			_ = s.ctrl.send(v3.Error, s.id, v3.Control{Code: "RESOURCE_LIMIT"})
			return
		}
		if f.Type != v3.Ping {
			s.activity.Store(time.Now().UnixNano())
		}
		ctx, cancel := context.WithTimeout(s.ctx, 5*time.Second)
		e = nil
		switch f.Type {
		case v3.SessionClose:
			cancel()
			return
		case v3.Ping:
			s.emit(v3.Pong, v3.Control{Ack: f.Sequence})
		case v3.TabCreate:
			e = s.browser.NewPage(ctx, c.Tab)
			if e == nil {
				e = s.browser.Select(ctx, c.Tab, s.width, s.height)
			}
			if e == nil {
				s.force.Store(true)
				s.emit(v3.SessionStatus, v3.Control{Tab: c.Tab, State: "Ready"})
			}
		case v3.TabSelect:
			e = s.browser.Select(ctx, c.Tab, s.width, s.height)
			if e == nil {
				s.force.Store(true)
				s.emit(v3.SessionStatus, v3.Control{Tab: c.Tab, State: "Selected"})
			}
		case v3.TabClose:
			e = s.browser.ClosePage(ctx, c.Tab)
		case v3.Navigate:
			u, parseErr := url.Parse(c.Target)
			if parseErr != nil || u.Scheme != "http" && u.Scheme != "https" || browsernet.Validate(c.Target) != nil {
				e = browsernet.ErrPolicy
				break
			}
			if _, e = s.browser.ActivePage(c.Tab); e != nil {
				break
			}
			s.emit(v3.NavigationStatus, v3.Control{Tab: c.Tab, State: "Loading"})
			// Page.navigate can wait for response headers. Run it independently so cancel,
			// heartbeats and input never wait for a slow navigation in the read loop.
			select {
			case s.navSlots <- struct{}{}:
			default:
				e = errors.New("RESOURCE_LIMIT")
				break
			}
			if e != nil {
				break
			}
			s.wg.Add(1)
			go func(tab, target string) {
				defer s.wg.Done()
				defer func() { <-s.navSlots }()
				nav, stop := context.WithTimeout(s.ctx, s.browser.Config.Navigation)
				defer stop()
				if err := s.browser.Navigate(nav, tab, target); err != nil {
					s.emit(v3.NavigationStatus, v3.Control{Tab: tab, State: "Failed", Code: errorCode(err)})
				}
			}(c.Tab, c.Target)
		case v3.InputEvent:
			active, w, h := s.browser.Active()
			if c.Tab != active || c.Input == nil || c.Input.Validate(w, h) != nil {
				e = v3.ErrFrame
				break
			}
			overhead := time.Since(received).Microseconds()
			start := time.Now()
			e = s.browser.Input(ctx, *c.Input)
			if e == nil {
				s.lastInput.Store(f.Sequence)
				s.emit(v3.InputAck, v3.Control{Ack: f.Sequence, DispatchUS: overhead, BrowserUS: time.Since(start).Microseconds()})
			}
		case v3.ViewportSet:
			if !s.m.Config.Viewport(c.Width, c.Height) {
				e = errors.New("VIEWPORT_REJECTED")
				break
			}
			active, _, _ := s.browser.Active()
			if active != "" {
				e = s.browser.Select(ctx, active, c.Width, c.Height)
			}
			if e == nil {
				s.width = c.Width
				s.height = c.Height
				s.force.Store(true)
				s.emit(v3.ViewportAck, v3.Control{Width: c.Width, Height: c.Height})
			}
		case v3.FrameRefresh:
			s.force.Store(true)
		case v3.Cancel:
			e = s.browser.Cancel(ctx, c.Tab)
			s.emit(v3.NavigationStatus, v3.Control{Tab: c.Tab, State: "Cancelled", Code: "CANCELLED"})
		case v3.History:
			e = s.browser.History(ctx, c.Tab, c.Direction)
		default:
			cancel()
			_ = s.ctrl.send(v3.Error, s.id, v3.Control{Code: "PROTOCOL_ERROR"})
			return
		}
		cancel()
		if e != nil {
			code := errorCode(e)
			if code == "PROTOCOL_ERROR" || code == "BROWSER_CRASHED" {
				s.terminal(code)
				return
			}
			s.emit(v3.Error, v3.Control{Tab: c.Tab, Code: code})
		}
	}
}
func (m *Manager) attach(parent context.Context, w *wire, owner string, id v3.ID) {
	f, _, e := w.read()
	if e != nil || f.Type != v3.PixelAttach || f.Session != id {
		m.fail(w, id, owner, "SESSION_INVALID")
		return
	}
	m.mu.Lock()
	s := m.sessions[id]
	m.mu.Unlock()
	if s == nil || s.owner != owner {
		m.fail(w, id, owner, "SESSION_INVALID")
		return
	}
	s.mu.Lock()
	if s.pixel != nil || s.ctx.Err() != nil || s.browser == nil {
		s.mu.Unlock()
		m.fail(w, id, owner, "SESSION_INVALID")
		return
	}
	s.pixel = w
	s.mu.Unlock()
	defer func() { s.cancel(); s.ctrl.c.Close() }()
	if w.send(v3.SessionStatus, id, v3.Control{State: "Attached"}) != nil {
		return
	}
	w.c.SetDeadline(time.Time{})
	close(s.ready)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		for {
			f, _, e := w.read()
			if e != nil || f.Session != id || (f.Type != v3.FrameRefresh && f.Type != v3.Ping) {
				s.cancel()
				s.ctrl.c.Close()
				return
			}
			if _, e = m.Auth.Lookup(owner); e != nil {
				s.cancel()
				s.ctrl.c.Close()
				return
			}
			s.force.Store(true)
		}
	}()
	defer func() { w.c.Close(); <-readDone }()
	next := time.Now()
	for {
		select {
		case <-parent.Done():
			return
		case <-s.ctx.Done():
			return
		case payload := <-s.latest:
			if delay := time.Until(next); delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-timer.C:
				case <-s.ctx.Done():
					timer.Stop()
					return
				}
			}
			// Capture continued while bandwidth pacing waited. Prefer that newest
			// independent frame rather than sending the state selected before wait.
			select {
			case payload = <-s.latest:
			default:
			}
			next = time.Now().Add(time.Duration(float64(time.Second) * float64(len(payload)) / float64(s.browser.Config.Bandwidth)))
			if e = w.frame(v3.Frame{Type: v3.PixelUpdate, Flags: v3.FullFrame, Session: id, Payload: payload}); e != nil {
				return
			}
			s.frames.Add(1)
			s.pixelBytes.Add(uint64(len(payload)))
		}
	}
}
func errorCode(e error) string {
	if e == nil {
		return ""
	}
	if errors.Is(e, context.DeadlineExceeded) {
		return "BROWSER_TIMEOUT"
	}
	if errors.Is(e, context.Canceled) {
		return "CANCELLED"
	}
	return v3.SafeCode(e.Error())
}
