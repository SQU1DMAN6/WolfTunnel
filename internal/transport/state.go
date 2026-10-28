// Package transport establishes explicitly selected, identity-verified TLS
// streams. Application framing/authentication stay in their existing packages.
package transport

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"sync"
	"time"
)

type State string

const (
	Disconnected        State = "DISCONNECTED"
	Resolving           State = "RESOLVING"
	Connecting          State = "CONNECTING"
	ProxyConnect        State = "PROXY_CONNECT"
	TLSHandshake        State = "TLS_HANDSHAKE"
	VerifyingIdentity   State = "VERIFYING_IDENTITY"
	Negotiating         State = "NEGOTIATING_PROTOCOL"
	Authenticating      State = "AUTHENTICATING"
	CreatingSession     State = "CREATING_BROWSER_SESSION"
	InitialisingBrowser State = "INITIALISING_BROWSER"
	ConnectingPixels    State = "CONNECTING_PIXEL_STREAM"
	Connected           State = "CONNECTED"
	RetryWait           State = "RETRY_WAIT"
	Failed              State = "FAILED"
	Cancelled           State = "CANCELLED"
)

type Event struct {
	Correlation string `json:"correlation,omitempty"`
	Attempt     string `json:"attempt"`
	Channel     string `json:"channel"`
	State       State  `json:"state"`
	Stage       State  `json:"stage"`
	Transport   string `json:"transport"`
	Retry       int    `json:"retry"`
	DurationMS  int64  `json:"duration_ms"`
	Code        string `json:"code,omitempty"`
}

type Failure struct {
	Code  string
	Stage State
	Cause error
}

func (e *Failure) Error() string { return e.Code }
func (e *Failure) Unwrap() error { return e.Cause }
func Code(err error) string {
	var f *Failure
	if errors.As(err, &f) {
		return f.Code
	}
	return ""
}
func At(stage State, code string, cause error) error { return &Failure{code, stage, cause} }

type Attempt struct {
	mu      sync.Mutex
	event   Event
	started time.Time
	observe func(Event)
}

func NewAttempt(channel, mode string, observe func(Event)) *Attempt {
	var token [12]byte
	_, _ = rand.Read(token[:])
	if mode == "" {
		mode = "tls-tcp"
	}
	return &Attempt{event: Event{Attempt: hex.EncodeToString(token[:]), Channel: channel, Transport: mode}, started: time.Now(), observe: observe}
}
func (a *Attempt) Set(s State) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.event.State = s
	a.event.Stage = s
	a.event.Code = ""
	a.publish()
	a.mu.Unlock()
}
func (a *Attempt) publish() {
	a.event.DurationMS = time.Since(a.started).Milliseconds()
	if a.observe != nil {
		a.observe(a.event)
	}
}
func (a *Attempt) Retry(n int) {
	if a == nil {
		return
	}
	a.mu.Lock()
	a.event.State = RetryWait
	a.event.Retry = n
	a.event.Code = ""
	a.publish()
	a.mu.Unlock()
}
func (a *Attempt) Finish(err error) {
	if a == nil {
		return
	}
	if err == nil {
		a.Set(Connected)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	var f *Failure
	if errors.As(err, &f) {
		a.event.Stage = f.Stage
		a.event.Code = f.Code
	}
	a.event.State = Failed
	if errors.Is(err, context.Canceled) {
		a.event.State = Cancelled
		a.event.Code = "NODE_CANCELLED"
	}
	a.publish()
}
func (a *Attempt) Wrap(err error) error {
	if err == nil {
		return nil
	}
	var f *Failure
	if errors.As(err, &f) {
		return err
	}
	a.mu.Lock()
	stage := a.event.Stage
	a.mu.Unlock()
	return At(stage, "CONNECTION_INTERRUPTED", err)
}
