package nodeclient

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
	"wolftunnel/internal/audit"
	v3 "wolftunnel/internal/protocol/v3"
)

// BrowserStatus records independent observations; it never contains page data.
type BrowserStatus struct {
	FailureStage  string `json:"failure_stage,omitempty"`
	Encoding      byte   `json:"encoding,omitempty"`
	Width, Height int
	Decoder       string `json:"decoder,omitempty"`
	Correlation   string `json:"correlation"`
	Control       string `json:"control"`
	Display       string `json:"display"`
	Browser       string `json:"browser"`
	Frame         string `json:"frame"`
	Stage         string `json:"stage"`
	Code          string `json:"code,omitempty"`
	CauseCode     string `json:"cause_code,omitempty"`
	ElapsedMS     int64  `json:"elapsed_ms"`
}
type lifecycle struct {
	mu      sync.Mutex
	status  BrowserStatus
	started time.Time
	publish func(BrowserStatus)
}

func newLifecycle(c Client, id string) *lifecycle {
	l := &lifecycle{status: BrowserStatus{Correlation: id, Control: "disconnected", Display: "disconnected", Browser: "not_started", Frame: "not_requested"}, started: time.Now()}
	l.publish = func(s BrowserStatus) {
		if c.Log != nil {
			c.Log.Log(audit.Entry{Event: "browser_lifecycle", Component: "client", Correlation: s.Correlation, Stage: s.Stage, Code: s.Code, CauseCode: s.CauseCode, Status: s.Control + "/" + s.Browser + "/" + s.Display + "/" + s.Frame, DurationMS: s.ElapsedMS, Evidence: decoderEvidence(s)})
		}
		if c.BrowserObserver != nil {
			c.BrowserObserver(s)
		}
	}
	return l
}
func (l *lifecycle) set(stage string, update func(*BrowserStatus)) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.status.Stage = stage
	update(&l.status)
	l.status.ElapsedMS = time.Since(l.started).Milliseconds()
	s := l.status
	l.mu.Unlock()
	l.publish(s)
}
func (l *lifecycle) snapshot() BrowserStatus {
	if l == nil {
		return BrowserStatus{}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.status
}

// LifecycleError keeps the original fixed cause as well as the affected role.
// Control state is an observation immediately before teardown, not a guarantee
// that a remote connection remains healthy after a failure.
type LifecycleError struct {
	Code   string
	Status BrowserStatus
	Cause  error
}

func (e *LifecycleError) Error() string { return e.Code }
func (e *LifecycleError) Unwrap() error { return e.Cause }
func (l *lifecycle) failure(code string, cause error) error {
	causeCode := ErrorCode(cause)
	var remote *Error
	if errors.As(cause, &remote) && code == "BROWSER_INITIALISATION_FAILED" {
		code = remote.Code
	}
	if causeCode == "NODE_IDENTITY_CHANGED" || causeCode == "NODE_IDENTITY_EXPIRED" || (code != "DISPLAY_CONNECTION_FAILED" && (causeCode == "UNSUPPORTED_VERSION" || causeCode == "PROTOCOL_ERROR" || causeCode == "AUTH_FAILED")) {
		code = causeCode
	}
	if errors.Is(cause, context.Canceled) {
		code = "NODE_CANCELLED"
	}
	stage := l.snapshot().Stage
	switch code {
	case "CONTROL_STREAM_FAILED", "CONTROL_PROTOCOL_INVALID", "CONTROL_IDLE_TIMEOUT":
		stage = "control_receive"
	case "DISPLAY_CONNECTION_FAILED":
		stage = "display_connect"
	case "FIRST_FRAME_TIMEOUT":
		stage = "first_frame"
	case "FRAME_IDLE_TIMEOUT", "DISPLAY_STREAM_LOST", "DISPLAY_FRAME_TRUNCATED", "DISPLAY_PROTOCOL_INVALID":
		stage = "display_receive"
	case "FRAME_DECODE_FAILED":
		stage = "image_decode"
	case "RENDER_UPLOAD_FAILED":
		stage = "texture_upload"
	case "RENDER_SUBMIT_FAILED":
		stage = "render_submit"
	case "RENDER_PRESENT_FAILED":
		stage = "render_present"
	}
	l.set(stage, func(s *BrowserStatus) {
		s.Code = code
		s.FailureStage = stage
		s.CauseCode = causeCode
		if stage == "control_receive" {
			s.Control = "lost"
		}
		if stage == "display_receive" || stage == "image_decode" || code == "FIRST_FRAME_TIMEOUT" || code == "FRAME_IDLE_TIMEOUT" {
			s.Display = "failed"
		}
	})
	return &LifecycleError{Code: code, Status: l.snapshot(), Cause: cause}
}

func probeControl(p *browserPeer, l *lifecycle) {
	p.conn.SetReadDeadline(time.Now().Add(time.Second))
	seq, e := p.send(v3.Ping, v3.Control{})
	if e == nil {
		f, c, err := p.read(false)
		e = err
		if e == nil && (f.Type != v3.Pong || c.Ack != seq) {
			e = v3.ErrFrame
		}
	}
	p.conn.SetReadDeadline(time.Time{})
	l.set("display_connect", func(s *BrowserStatus) {
		s.Control = "lost"
		s.Display = "failed"
		if e == nil {
			s.Control = "responsive"
		}
	})
}

func decoderEvidence(s BrowserStatus) string {
	if s.Decoder == "" {
		return ""
	}
	return fmt.Sprintf("decoder=%s; encoding=%d; dimensions=%dx%d", s.Decoder, s.Encoding, s.Width, s.Height)
}
