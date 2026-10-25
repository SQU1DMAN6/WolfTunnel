package nodeclient

import (
	"context"
	"crypto/tls"
	"errors"
	"image"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"wolftunnel/internal/identity"
	"wolftunnel/internal/pixelstream"
	v3 "wolftunnel/internal/protocol/v3"
)

type BrowserOptions struct{ Width, Height, Quality, FPS int }
type BrowserEvent struct {
	Type    byte
	Control v3.Control
}
type BrowserFrame struct {
	Update   pixelstream.Update
	Image    image.Image
	Sequence uint64
	DecodeUS int64
}
type BrowserMetrics struct {
	BrowserUS                                       int64
	Frames, Bytes, Skipped, InputAcks               uint64
	DecodeUS, DispatchUS, InputQueueUS, RoundTripUS int64
}
type browserPeer struct {
	conn    *tls.Conn
	mu      sync.Mutex
	out, in uint64
	id      v3.ID
}

func (p *browserPeer) send(typ byte, c v3.Control) (uint64, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.out++
	_ = p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return p.out, v3.Write(p.conn, v3.Frame{Type: typ, Session: p.id, Sequence: p.out, Payload: v3.Encode(c)})
}
func (p *browserPeer) read(pixels bool) (v3.Frame, v3.Control, error) {
	f, e := v3.Read(p.conn, pixels)
	if e != nil {
		return f, v3.Control{}, e
	}
	if f.Sequence <= p.in || (f.Type != v3.PixelUpdate && f.Sequence != p.in+1) {
		return f, v3.Control{}, v3.ErrFrame
	}
	p.in = f.Sequence
	if f.Type == v3.PixelUpdate {
		return f, v3.Control{}, nil
	}
	c, e := v3.Decode(f)
	c.Code = v3.SafeCode(c.Code)
	return f, c, e
}
func (c Client) openPeer(ctx context.Context, role string, id v3.ID) (*browserPeer, error) {
	if e := c.Config.Validate(); e != nil {
		return nil, e
	}
	if c.Config.ClientID == "" {
		return nil, &Error{Code: "NOT_ENROLLED"}
	}
	if c.Config.Fingerprint == "" {
		return nil, &Error{Code: "NODE_IDENTITY_UNVERIFIED"}
	}
	d := tls.Dialer{NetDialer: &net.Dialer{Timeout: 5 * time.Second}, Config: &tls.Config{MinVersion: tls.VersionTLS13, InsecureSkipVerify: true, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.PeerCertificates) == 0 || identity.Fingerprint(cs.PeerCertificates[0].Raw) != c.Config.Fingerprint {
			return &Error{Code: "NODE_IDENTITY_CHANGED"}
		}
		cert := cs.PeerCertificates[0]
		if time.Now().Before(cert.NotBefore) || time.Now().After(cert.NotAfter) {
			return &Error{Code: "NODE_IDENTITY_EXPIRED"}
		}
		return nil
	}}}
	raw, e := d.DialContext(ctx, "tcp", c.Config.Address())
	if e != nil {
		return nil, e
	}
	conn := raw.(*tls.Conn)
	if deadline, ok := ctx.Deadline(); ok {
		conn.SetDeadline(deadline)
	}
	p := &browserPeer{conn: conn}
	ok := false
	defer func() {
		if !ok {
			conn.Close()
		}
	}()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	if _, e = p.send(v3.Hello, v3.Control{Versions: []int{3, 2}}); e != nil {
		return nil, e
	}
	f, out, e := p.read(false)
	if e != nil {
		return nil, e
	}
	if f.Type == v3.Error {
		return nil, &Error{Code: out.Code}
	}
	if f.Type != v3.NodeIdentity || out.Version != 3 || out.Fingerprint != c.Config.Fingerprint || !f.Session.Zero() {
		return nil, v3.ErrFrame
	}
	state := conn.ConnectionState()
	binding, e := state.ExportKeyingMaterial("WolfTunnel-v3", nil, 32)
	if e != nil {
		return nil, e
	}
	p.id = id
	proof := v3.Sign(c.Key, binding, id, v3.Control{ClientID: c.Config.ClientID, Role: role})
	if _, e = p.send(v3.Authenticate, proof); e != nil {
		return nil, e
	}
	f, out, e = p.read(false)
	if e != nil {
		return nil, e
	}
	if f.Type == v3.Error {
		return nil, &Error{Code: out.Code}
	}
	if f.Type != v3.AuthResult || out.ClientID != c.Config.ClientID || f.Session != id {
		return nil, v3.ErrFrame
	}
	ok = true
	return p, nil
}

type transmit struct {
	typ  byte
	body v3.Control
	at   time.Time
}
type Browser struct {
	report         func(string, error)
	textSequence   atomic.Uint64
	ID             v3.ID
	MaxTabs        int
	Frames         chan BrowserFrame
	Events         chan BrowserEvent
	ctx            context.Context
	cancel         context.CancelFunc
	control, pixel *browserPeer
	once           sync.Once
	wg             sync.WaitGroup
	queue          chan transmit
	wake           chan struct{}
	mu             sync.Mutex
	pages          map[string]bool
	movement       *transmit
	pending        map[uint64]time.Time
	metrics        BrowserMetrics
	width, height  int
	frames         atomic.Uint64
}

func (c Client) OpenBrowser(parent context.Context, o BrowserOptions) (opened *Browser, result error) {
	start := time.Now()
	defer func() { c.reportOperation("browser_open_result", start, result) }()
	startup, stop := context.WithTimeout(parent, 30*time.Second)
	defer stop()
	p, e := c.openPeer(startup, "control", v3.ID{})
	if e != nil {
		return nil, e
	}
	ok := false
	defer func() {
		if !ok {
			p.conn.Close()
		}
	}()
	if _, e = p.send(v3.SessionCreate, v3.Control{Width: o.Width, Height: o.Height, Quality: o.Quality, FPS: o.FPS}); e != nil {
		return nil, e
	}
	f, out, e := p.read(false)
	if e != nil {
		return nil, e
	}
	if f.Type == v3.Error {
		return nil, &Error{Code: out.Code}
	}
	if f.Type != v3.SessionCreated || f.Session.Zero() || out.MaxTabs < 1 || out.MaxTabs > 8 || out.Width < 320 || out.Height < 200 {
		return nil, v3.ErrFrame
	}
	p.id = f.Session
	pixels, e := c.openPeer(startup, "pixel", p.id)
	if e != nil {
		return nil, e
	}
	if _, e = pixels.send(v3.PixelAttach, v3.Control{}); e != nil {
		pixels.conn.Close()
		return nil, e
	}
	af, attached, e := pixels.read(false)
	if e != nil || af.Type != v3.SessionStatus || af.Session != p.id || attached.State != "Attached" {
		pixels.conn.Close()
		return nil, v3.ErrFrame
	}
	ctx, cancel := context.WithCancel(parent)
	b := &Browser{report: c.Report, ID: p.id, MaxTabs: out.MaxTabs, Frames: make(chan BrowserFrame, 1), Events: make(chan BrowserEvent, 32), ctx: ctx, cancel: cancel, control: p, pixel: pixels, queue: make(chan transmit, 128), wake: make(chan struct{}, 1), pages: make(map[string]bool), pending: make(map[uint64]time.Time), width: out.Width, height: out.Height}
	p.conn.SetDeadline(time.Time{})
	pixels.conn.SetDeadline(time.Time{})
	b.wg.Add(3)
	go b.write()
	go b.readControl()
	go b.readPixels()
	go func() { <-ctx.Done(); p.conn.Close(); pixels.conn.Close() }()
	ok = true
	c.trace("browser_session_opened", "", "ok", time.Now())
	return b, nil
}
func (b *Browser) Close() {
	b.once.Do(func() { b.cancel(); b.control.conn.Close(); b.pixel.conn.Close(); b.wg.Wait() })
}
func (b *Browser) Done() <-chan struct{}   { return b.ctx.Done() }
func (b *Browser) Size() (int, int)        { b.mu.Lock(); defer b.mu.Unlock(); return b.width, b.height }
func (b *Browser) Metrics() BrowserMetrics { b.mu.Lock(); defer b.mu.Unlock(); return b.metrics }
func (b *Browser) fail(code string) {
	if b.report != nil {
		b.report("browser_stream_failed", &Error{Code: code})
	}
	select {
	case b.Events <- BrowserEvent{Type: v3.Error, Control: v3.Control{Code: v3.SafeCode(code)}}:
	default:
	}
	b.cancel()
}
func (b *Browser) command(typ byte, c v3.Control) error {
	if b.ctx.Err() != nil {
		return errors.New("NODE_UNREACHABLE")
	}
	t := transmit{typ: typ, body: c, at: time.Now()}
	b.mu.Lock()
	if typ == v3.InputEvent && c.Input != nil && c.Input.Kind == "move" {
		b.movement = &t
		b.mu.Unlock()
		select {
		case b.wake <- struct{}{}:
		default:
		}
		return nil
	}
	if typ == v3.TabSelect || typ == v3.TabClose {
		b.movement = nil
	}
	if typ == v3.InputEvent && c.Input != nil && (c.Input.Kind == "down" || c.Input.Kind == "up" || c.Input.Kind == "wheel" || c.Input.Kind == "blur") {
		// These events already carry their current coordinates (or clear focus).
		// An earlier coalesced move must not replay after the urgent event.
		b.movement = nil
	}
	b.mu.Unlock()
	select {
	case b.queue <- t:
		return nil
	default:
		b.fail("RESOURCE_LIMIT")
		return errors.New("RESOURCE_LIMIT")
	}
}
func (b *Browser) NewTab(id string) error {
	if _, e := v3.ParseID(id); e != nil {
		return e
	}
	b.mu.Lock()
	if b.pages[id] {
		b.mu.Unlock()
		return nil
	}
	if len(b.pages) >= b.MaxTabs {
		b.mu.Unlock()
		return errors.New("TAB_LIMIT")
	}
	b.pages[id] = true
	b.mu.Unlock()
	return b.command(v3.TabCreate, v3.Control{Tab: id})
}
func (b *Browser) Select(id string) error {
	if e := b.NewTab(id); e != nil {
		return e
	}
	return b.command(v3.TabSelect, v3.Control{Tab: id})
}
func (b *Browser) CloseTab(id string) error {
	b.mu.Lock()
	exists := b.pages[id]
	delete(b.pages, id)
	b.mu.Unlock()
	if !exists {
		return nil
	}
	return b.command(v3.TabClose, v3.Control{Tab: id})
}
func (b *Browser) Navigate(id, target string) error {
	if e := b.Select(id); e != nil {
		return e
	}
	return b.command(v3.Navigate, v3.Control{Tab: id, Target: target})
}
func (b *Browser) Input(id string, i v3.Input) error {
	w, h := b.Size()
	if i.Validate(w, h) != nil {
		return v3.ErrFrame
	}
	return b.command(v3.InputEvent, v3.Control{Tab: id, Input: &i})
}
func (b *Browser) Resize(w, h int) error {
	if w < 320 || h < 200 || w > 3840 || h > 2160 || w*h > pixelstream.MaxPixels {
		return errors.New("VIEWPORT_REJECTED")
	}
	return b.command(v3.ViewportSet, v3.Control{Width: w, Height: h})
}
func (b *Browser) Cancel(id string) error { return b.command(v3.Cancel, v3.Control{Tab: id}) }
func (b *Browser) History(id string, delta int) error {
	return b.command(v3.History, v3.Control{Tab: id, Direction: delta})
}
func (b *Browser) Refresh() error { return b.command(v3.FrameRefresh, v3.Control{}) }
func (b *Browser) Ping() error    { return b.command(v3.Ping, v3.Control{}) }
func (b *Browser) write() {
	defer b.wg.Done()
	heartbeat := time.NewTicker(10 * time.Second)
	defer heartbeat.Stop()
	for {
		var t transmit
		select {
		case <-b.ctx.Done():
			return
		case t = <-b.queue:
		default:
			select {
			case <-b.ctx.Done():
				return
			case t = <-b.queue:
			case <-b.wake:
				b.mu.Lock()
				if b.movement != nil {
					t = *b.movement
					b.movement = nil
				}
				b.mu.Unlock()
				if t.typ == 0 {
					continue
				}
			case <-heartbeat.C:
				t = transmit{typ: v3.Ping, at: time.Now()}
			}
		}
		b.control.mu.Lock()
		seq := b.control.out + 1
		b.mu.Lock()
		if t.typ == v3.InputEvent || t.typ == v3.Ping {
			if len(b.pending) < 256 {
				b.pending[seq] = time.Now()
			}
		}
		b.metrics.InputQueueUS = time.Since(t.at).Microseconds()
		b.mu.Unlock()
		b.control.out = seq
		_ = b.control.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
		e := v3.Write(b.control.conn, v3.Frame{Type: t.typ, Session: b.ID, Sequence: seq, Payload: v3.Encode(t.body)})
		b.control.mu.Unlock()
		if e == nil && t.typ == v3.InputEvent && t.body.Input != nil && t.body.Input.Kind == "text" {
			b.textSequence.Store(seq)
		}
		if e != nil {
			b.fail("NODE_UNREACHABLE")
			return
		}
	}
}
func (b *Browser) readControl() {
	defer b.wg.Done()
	for {
		b.control.conn.SetReadDeadline(time.Now().Add(30 * time.Second))
		f, c, e := b.control.read(false)
		if e != nil {
			if b.ctx.Err() == nil {
				b.fail(ErrorCode(e))
			}
			return
		}
		if f.Session != b.ID {
			b.fail("SESSION_INVALID")
			return
		}
		if f.Type == v3.InputAck || f.Type == v3.Pong {
			b.mu.Lock()
			if start, ok := b.pending[c.Ack]; ok {
				b.metrics.RoundTripUS = time.Since(start).Microseconds()
				delete(b.pending, c.Ack)
			}
			if f.Type == v3.InputAck {
				b.metrics.InputAcks++
				b.metrics.DispatchUS = c.DispatchUS
				b.metrics.BrowserUS = c.BrowserUS
			}
			b.mu.Unlock()
			if f.Type == v3.InputAck {
				continue
			}
		}
		if f.Type == v3.ViewportAck {
			b.mu.Lock()
			b.width = c.Width
			b.height = c.Height
			b.mu.Unlock()
		}
		select {
		case b.Events <- BrowserEvent{Type: f.Type, Control: c}:
		case <-b.ctx.Done():
			return
		default:
			b.fail("RESOURCE_LIMIT")
			return
		}
		if c.Code != "" && (f.Type == v3.Error || f.Type == v3.NavigationStatus) && b.report != nil {
			b.report("browser_remote_error", &Error{Code: c.Code})
		}
		if f.Type == v3.Error && (c.Code == "AUTH_FAILED" || c.Code == "SESSION_INVALID" || c.Code == "PROTOCOL_ERROR" || c.Code == "BROWSER_CRASHED" || c.Code == "PIXEL_ERROR") {
			b.cancel()
			return
		}
	}
}
func (b *Browser) readPixels() {
	defer b.wg.Done()
	for {
		b.pixel.conn.SetReadDeadline(time.Now().Add(15 * time.Second))
		f, _, e := b.pixel.read(true)
		if e != nil {
			if b.ctx.Err() == nil {
				b.fail(ErrorCode(e))
			}
			return
		}
		if f.Type != v3.PixelUpdate || f.Session != b.ID {
			b.fail("PROTOCOL_ERROR")
			return
		}
		start := time.Now()
		u, img, e := pixelstream.Decode(f.Payload)
		if e != nil {
			b.fail("PIXEL_ERROR")
			return
		}
		w, h := b.Size()
		if u.Width != w || u.Height != h {
			_ = b.Refresh()
			continue
		}
		frame := BrowserFrame{Update: u, Image: img, Sequence: f.Sequence, DecodeUS: time.Since(start).Microseconds()}
		b.mu.Lock()
		b.metrics.Frames++
		b.metrics.Bytes += uint64(len(f.Payload))
		b.metrics.DecodeUS = frame.DecodeUS
		b.mu.Unlock()
		select {
		case b.Frames <- frame:
		default:
			select {
			case <-b.Frames:
			default:
			}
			b.mu.Lock()
			b.metrics.Skipped++
			b.mu.Unlock()
			select {
			case b.Frames <- frame:
			default:
			}
		}
	}
}

func (b *Browser) LastTextSequence() uint64 { return b.textSequence.Load() }
