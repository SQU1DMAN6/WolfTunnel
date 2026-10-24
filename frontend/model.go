// Package frontend implements the one native Wolf Client. Model state belongs
// exclusively to the SDL thread; workers return immutable completions.
package frontend

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"wolftunnel/internal/enrolment"
	"wolftunnel/internal/nodeclient"
	"wolftunnel/internal/protocol"
)

type Snapshot struct {
	Input, Target, State string
	Result               protocol.Envelope
}
type Tab struct {
	ID, Title, Input, Target, State, RequestID string
	Settings                                   bool
	Result                                     protocol.Envelope
	History                                    []Snapshot
	Position                                   int
	cancel                                     context.CancelFunc
}
type completion struct {
	kind, tab, id string
	result        protocol.Envelope
	err           error
	client        nodeclient.Client
	value         string
	latency       time.Duration
	generation    uint64
	input, target string
}
type Model struct {
	Client                                          nodeclient.Client
	Root                                            string
	Tabs                                            []*Tab
	Active                                          int
	Connection, ConnectionError, Notice, Discovered string
	RTT                                             time.Duration
	LastStatus                                      int
	Verified                                        bool
	Generation                                      uint64
	jobs                                            chan completion
	connectionCancel                                context.CancelFunc
	busy                                            bool
	ctx                                             context.Context
	stop                                            context.CancelFunc
	wg                                              sync.WaitGroup
	workers                                         chan struct{}
}

func NewModel(c nodeclient.Client, root string) *Model {
	m := &Model{Client: c, Root: root, Connection: "Disconnected", jobs: make(chan completion, 16)}
	m.ctx, m.stop = context.WithCancel(context.Background())
	m.workers = make(chan struct{}, 8)
	m.NewTab()
	return m
}
func (m *Model) Current() *Tab { return m.Tabs[m.Active] }
func (m *Model) NewTab() {
	requests := 0
	for _, t := range m.Tabs {
		if !t.Settings {
			requests++
		}
	}
	if requests >= 7 {
		m.Notice = "Maximum seven request tabs; Settings has a reserved slot"
		return
	}
	m.Tabs = append(m.Tabs, &Tab{ID: nodeclient.ID(), Title: "New request", State: "Ready", Position: -1})
	m.Active = len(m.Tabs) - 1
}
func (m *Model) Settings() {
	for i, t := range m.Tabs {
		if t.Settings {
			m.Active = i
			return
		}
	}
	if len(m.Tabs) >= 8 {
		m.Notice = "Close a tab to open Settings"
		return
	}
	m.Tabs = append(m.Tabs, &Tab{ID: nodeclient.ID(), Title: "Settings", Settings: true, Position: -1})
	m.Active = len(m.Tabs) - 1
}
func (m *Model) CloseTab(i int) {
	if i < 0 || i >= len(m.Tabs) {
		return
	}
	t := m.Tabs[i]
	if t.cancel != nil {
		t.cancel()
	}
	m.Tabs = append(m.Tabs[:i], m.Tabs[i+1:]...)
	if len(m.Tabs) == 0 {
		m.NewTab()
		return
	}
	if m.Active > i {
		m.Active--
	}
	if m.Active >= len(m.Tabs) {
		m.Active = len(m.Tabs) - 1
	}
}
func (m *Model) Cancel() {
	t := m.Current()
	if t.cancel != nil {
		t.cancel()
		t.cancel = nil
		t.RequestID = ""
		t.State = "Cancelled"
	}
}
func (m *Model) Disconnect() {
	m.Generation++
	if m.connectionCancel != nil {
		m.connectionCancel()
		m.connectionCancel = nil
	}
	m.busy = false
	for _, t := range m.Tabs {
		if t.cancel != nil {
			t.cancel()
			t.cancel = nil
			t.RequestID = ""
			t.State = "Cancelled"
		}
	}
	m.Connection = "Disconnected"
	m.Verified = false
	m.RTT = 0
}
func (m *Model) Shutdown() { m.Disconnect(); m.stop(); m.wg.Wait() }

func (m *Model) reserveWorker() bool {
	select {
	case m.workers <- struct{}{}:
		m.wg.Add(1)
		return true
	default:
		return false
	}
}
func (m *Model) finishWorker() { <-m.workers; m.wg.Done() }
func (m *Model) deliver(c completion) {
	select {
	case m.jobs <- c:
	case <-m.ctx.Done():
	}
}
func (m *Model) Configure(c nodeclient.Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if m.Client.Config.Fingerprint != "" && c.Fingerprint != m.Client.Config.Fingerprint {
		m.Disconnect()
		m.Connection = "Error"
		m.ConnectionError = "NODE_IDENTITY_CHANGED"
		return errors.New("forget the existing node before accepting a changed fingerprint")
	}
	if c.Host != m.Client.Config.Host || c.Port != m.Client.Config.Port {
		c.ClientID = ""
		c.Expires = time.Time{}
	}
	m.Disconnect()
	next := m.Client
	next.Config = c
	if err := next.Save(m.Root); err != nil {
		return err
	}
	m.Client = next
	m.Notice = "Settings saved. Verify the fingerprint through the node owner before connecting."
	return nil
}

func (m *Model) ImportInvitation(raw string) (string, error) {
	invitation, err := enrolment.Parse(raw)
	if err != nil {
		return "", err
	}
	c := m.Client.Config
	c.Host = invitation.Host
	c.Port = invitation.Port
	c.Fingerprint = invitation.Fingerprint
	if err = m.Configure(c); err != nil {
		return "", err
	}
	return invitation.Code, nil
}

// ConnectionMessage is the user-facing explanation; structured codes remain in
// diagnostics. It never includes peer-supplied strings or bearer credentials.
func ConnectionMessage(code string) string {
	switch code {
	case "INVALID_FINGERPRINT":
		return "Enter the 64 hexadecimal characters of the owner's SHA-256 pin."
	case "INVALID_CONFIGURATION":
		return "Check the node address, port (1–65535) and timeout (1–60 seconds)."
	case "NODE_POLICY_REJECTED":
		return "This destination is blocked by the node's network policy."
	case "NODE_TIMEOUT":
		return "The request timed out."
	case "NODE_DNS_ERROR":
		return "The node could not resolve this destination."
	case "NODE_RESPONSE_TOO_LARGE":
		return "The response exceeded the node's size limit."
	case "NODE_CANCELLED":
		return "Request cancelled."
	case "NODE_BUSY", "RATE_LIMITED":
		return "The node is busy. Try again shortly."
	case "ENROLMENT_CODE_REQUIRED":
		return "Paste an invitation from the node owner, or enter a fresh enrolment code."
	case "ENROLMENT_CODE_INVALID":
		return "The code is invalid, already used, or belongs to another node. Ask the owner for a fresh invitation."
	case "ENROLMENT_CODE_EXPIRED":
		return "The enrolment code expired. Ask the owner for a fresh invitation."
	case "ENROLMENT_ALREADY_ENROLLED":
		return "This installation is registered but cannot authenticate. Ask the owner to check expiry or revocation."
	case "ENROLMENT_CLIENT_REVOKED", "AUTH_FAILED":
		return "The saved identity was rejected. Ask the owner to check expiry or revocation."
	case "ENROLMENT_STORAGE_ERROR":
		return "The node could not update its credential store. Ask its owner to check node logs and permissions."
	case "ENROLMENT_CAPACITY":
		return "The node has reached its client limit. Contact its owner."
	case "ENROLMENT_DENIED":
		return "The node denied enrolment. Its older response gives no reason; use a fresh invitation or ask the owner to check node logs."
	case "NODE_IDENTITY_CHANGED":
		return "Node identity changed. Verify with the owner before forgetting or replacing the saved node."
	case "NODE_IDENTITY_UNVERIFIED":
		return "Import a trusted invitation or enter the owner's verified certificate pin."
	case "INVALID_INVITATION":
		return "That invitation is not valid. Paste the complete invitation supplied by the owner."
	case "NODE_UNREACHABLE":
		return "Cannot reach the node. Check its address, port and listener."
	case "CREDENTIAL_SAVE_FAILED":
		return "The client could not save its credentials. Check the local state directory permissions."
	default:
		return code
	}
}
func (m *Model) Forget() error {
	m.Disconnect()
	next := m.Client
	next.Config = nodeclient.DefaultConfig()
	if err := next.Save(m.Root); err != nil {
		return err
	}
	m.Client = next
	m.Discovered = ""
	m.ConnectionError = ""
	m.Notice = "Node forgotten locally. Ask its owner to revoke the old client ID."
	return nil
}

func Resolve(input, template string) (string, error) {
	s := strings.TrimSpace(input)
	if s == "" || len(s) > 8192 {
		return "", errors.New("enter a URL or search query (maximum 8192 bytes)")
	}
	u, err := url.Parse(s)
	if err == nil && u.Scheme != "" {
		if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || strings.ContainsAny(s, "\r\n") {
			return "", errors.New("only absolute HTTP and HTTPS URLs without credentials are supported")
		}
		u.Fragment = ""
		u.RawFragment = ""
		return u.String(), nil
	}
	if strings.Contains(s, "://") || strings.HasPrefix(strings.ToLower(s), "http:") || strings.HasPrefix(strings.ToLower(s), "https:") {
		return "", errors.New("malformed URL")
	}
	if template != nodeclient.Google && template != nodeclient.DuckDuckGo {
		return "", errors.New("unsupported search template")
	}
	return strings.Replace(template, "{query}", url.QueryEscape(s), 1), nil
}

// Prepare exposes the final URL before Navigate submits it, including searches.
func (m *Model) Prepare() error {
	t := m.Current()
	if t.Settings {
		return nil
	}
	target, err := Resolve(t.Input, m.Client.Config.SearchTemplate)
	if err != nil {
		return err
	}
	t.Target = target
	return nil
}
func (m *Model) Navigate() error {
	t := m.Current()
	if t.Settings {
		return nil
	}
	if m.Connection != "Connected" || !m.Verified {
		return errors.New("connect to a verified enrolled node first")
	}
	if err := m.Prepare(); err != nil {
		return err
	}
	if !m.reserveWorker() {
		return errors.New("waiting for cancelled requests to finish")
	}
	m.Cancel()
	id := nodeclient.ID()
	t.RequestID = id
	t.State = "Loading"
	t.Result = protocol.Envelope{}
	u, _ := url.Parse(t.Target)
	t.Title = u.Hostname()
	ctx, cancel := context.WithCancel(m.ctx)
	t.cancel = cancel
	client := m.Client
	tabID, target := t.ID, t.Target
	input := t.Input
	generation := m.Generation
	go func() {
		defer m.finishWorker()
		out, err := client.Fetch(ctx, id, target)
		cancel()
		m.deliver(completion{kind: "fetch", tab: tabID, id: id, result: out, err: err, generation: generation, input: input, target: target})
	}()
	return nil
}
func (m *Model) History(delta int) {
	t := m.Current()
	if t.Settings {
		return
	}
	m.Cancel()
	p := t.Position + delta
	if p < 0 || p >= len(t.History) {
		return
	}
	t.Position = p
	h := t.History[p]
	t.Input = h.Input
	t.Target = h.Target
	if parsed, err := url.Parse(h.Target); err == nil && parsed.Hostname() != "" {
		t.Title = parsed.Hostname()
	}
	t.State = h.State
	t.Result = h.Result
	m.Notice = "Local metadata history; no webpage is restored."
}
func (m *Model) Operation(kind, code string) {
	if m.busy {
		m.Notice = "Connection operation already running"
		return
	}
	if !m.reserveWorker() {
		m.Notice = "Waiting for cancelled requests to finish"
		return
	}
	m.busy = true
	generation := m.Generation
	client := m.Client
	ctx, cancel := context.WithCancel(m.ctx)
	m.connectionCancel = cancel
	if kind != "discover" {
		m.Connection = "Connecting"
	}
	go func() {
		defer m.finishWorker()
		defer cancel()
		c := completion{kind: kind, client: client, generation: generation}
		switch kind {
		case "discover":
			c.value, c.err = nodeclient.Discover(ctx, client.Config)
		case "enrol", "connect":
			c.err = c.client.Enrol(ctx, code)
			if c.err == nil {
				c.latency, c.err = c.client.Ping(ctx)
			}
		case "renew":
			c.err = c.client.Renew(ctx)
		case "ping":
			c.latency, c.err = client.Ping(ctx)
		default:
			c.err = errors.New("unknown operation")
		}
		m.deliver(c)
	}()
}
func (m *Model) Drain() bool {
	changed := false
	for {
		select {
		case c := <-m.jobs:
			changed = true
			if c.generation != m.Generation {
				continue
			}
			if c.kind == "fetch" {
				for _, t := range m.Tabs {
					if t.ID != c.tab || t.RequestID != c.id {
						continue
					}
					t.cancel = nil
					t.RequestID = ""
					t.Result = c.result
					if c.err != nil {
						t.State = "Failed"
						t.Result.Code = nodeclient.ErrorCode(c.err)
						if t.Result.Code != "NODE_BUSY" && t.Result.Code != "RATE_LIMITED" && t.Result.Code != "INVALID_REQUEST" {
							m.Connection = "Error"
							m.ConnectionError = t.Result.Code
							m.Verified = false
						}
					} else if c.result.Code != "" {
						t.State = "Failed"
					} else {
						t.State = "Completed"
					}
					if t.Result.Code == "NODE_CANCELLED" {
						t.State = "Cancelled"
					}
					m.LastStatus = t.Result.Status
					if t.Position+1 < len(t.History) {
						t.History = t.History[:t.Position+1]
					}
					t.History = append(t.History, Snapshot{Input: c.input, Target: c.target, State: t.State, Result: t.Result})
					if len(t.History) > 50 {
						t.History = t.History[1:]
					}
					t.Position = len(t.History) - 1
				}
				continue
			}
			m.busy = false
			m.connectionCancel = nil
			if (c.kind == "enrol" || c.kind == "connect") && c.client.Config.ClientID != "" {
				if err := c.client.Save(m.Root); err != nil {
					c.err = &nodeclient.Error{Code: "CREDENTIAL_SAVE_FAILED"}
				} else {
					m.Client = c.client
				}
			}
			if c.err != nil {
				m.Connection = "Error"
				m.Verified = false
				m.ConnectionError = nodeclient.ErrorCode(c.err)
				m.Notice = ConnectionMessage(m.ConnectionError)
				continue
			}
			switch c.kind {
			case "discover":
				m.Discovered = c.value
				m.Notice = "Discovered identity is UNVERIFIED. Compare it with the node owner; enter the verified pin separately."
			case "enrol", "connect", "renew":
				if err := c.client.Save(m.Root); err != nil {
					m.Connection = "Error"
					m.ConnectionError = "CREDENTIAL_SAVE_FAILED"
					m.Notice = m.ConnectionError
					continue
				}
				m.Client = c.client
				m.Verified = true
				m.Connection = "Connected"
				m.ConnectionError = ""
				m.RTT = c.latency
				m.Notice = fmt.Sprintf("Connected · credential valid until %s", c.client.Config.Expires.Local().Format("2 Jan 2006"))
			case "ping":
				m.Connection = "Connected"
				m.Verified = true
				m.ConnectionError = ""
				m.RTT = c.latency
				m.Notice = "Node identity verified; authenticated connection test passed."
			}
		default:
			return changed
		}
	}
}
