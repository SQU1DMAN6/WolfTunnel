package frontend

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wolftunnel/internal/enrolment"
	"wolftunnel/internal/node"
	"wolftunnel/internal/nodeclient"
	"wolftunnel/internal/protocol"
)

func TestResolveNavigationAndSearch(t *testing.T) {
	for _, tc := range []struct{ input, template, want string }{{"https://example.com/a?q=x#fragment", nodeclient.Google, "https://example.com/a?q=x"}, {"wolves & snow ☃", nodeclient.DuckDuckGo, "https://duckduckgo.com/?q=wolves+%26+snow+%E2%98%83"}, {"https://www.google.com/search?q=wolf", nodeclient.DuckDuckGo, "https://www.google.com/search?q=wolf"}, {"example.com", nodeclient.Google, "https://www.google.com/search?q=example.com"}} {
		out, err := Resolve(tc.input, tc.template)
		if err != nil || out != tc.want {
			t.Fatalf("%q: %q %v", tc.input, out, err)
		}
	}
	for _, input := range []string{"", "file:///tmp/x", "javascript:alert(1)", "mailto:x@example.com", "https:///bad", "http://", "https://u:p@example.com", "http://[broken", "ftp://example.com"} {
		if _, err := Resolve(input, nodeclient.Google); err == nil {
			t.Fatalf("accepted %q", input)
		}
	}
}
func TestTabsHistoryCancellationAndSettings(t *testing.T) {
	c, err := nodeclient.Load(filepath.Join(t.TempDir(), "client"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(c, t.TempDir())
	defer m.Shutdown()
	a := m.Current()
	a.Input = "https://a.example"
	a.Result.Status = 404
	a.State = "Completed"
	a.History = []Snapshot{{Input: a.Input, State: a.State, Result: a.Result}, {Input: "second", State: "Completed", Result: protocol.Envelope{Status: 200}}}
	a.Position = 1
	m.NewTab()
	b := m.Current()
	b.Input = "independent"
	if b.Result.Status != 0 {
		t.Fatal("tab shared result")
	}
	m.Active = 0
	m.History(-1)
	if a.Result.Status != 404 || b.Input != "independent" {
		t.Fatal("history shared across tabs")
	}
	cancelled := false
	a.cancel = func() { cancelled = true }
	a.State = "Loading"
	m.CloseTab(0)
	if !cancelled || m.Current() != b {
		t.Fatal("tab closure did not cancel")
	}
	for i := 0; i < 10; i++ {
		m.NewTab()
	}
	m.Settings()
	if !m.Current().Settings || len(m.Tabs) != 8 {
		t.Fatal("settings not available at tab limit")
	}
	m.Settings()
	if len(m.Tabs) != 8 {
		t.Fatal("duplicate settings tab")
	}
}
func TestConnectionStateIsIndependentOfHTTP(t *testing.T) {
	c, err := nodeclient.Load(filepath.Join(t.TempDir(), "client"))
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(c, t.TempDir())
	defer m.Shutdown()
	m.Connection = "Connected"
	m.Verified = true
	tab := m.Current()
	tab.RequestID = "one"
	tab.State = "Loading"
	m.jobs <- completion{kind: "fetch", tab: tab.ID, id: "one", result: protocol.Envelope{Status: 404}}
	m.Drain()
	if m.Connection != "Connected" || tab.State != "Completed" || tab.Result.Status != 404 {
		t.Fatal("HTTP 404 disconnected node")
	}
	tab.RequestID = "two"
	m.jobs <- completion{kind: "fetch", tab: tab.ID, id: "two", result: protocol.Envelope{Code: "NODE_POLICY_REJECTED"}}
	m.Drain()
	if m.Connection != "Connected" || tab.State != "Failed" {
		t.Fatal("policy error disconnected node")
	}
	tab.RequestID = "three"
	m.jobs <- completion{kind: "fetch", tab: tab.ID, id: "three", err: &nodeclient.Error{Code: "NODE_BUSY"}}
	m.Drain()
	if m.Connection != "Connected" || !m.Verified {
		t.Fatal("node capacity error disconnected node")
	}
	tab.RequestID = "four"
	m.jobs <- completion{kind: "fetch", tab: tab.ID, id: "four", err: &nodeclient.Error{Code: "NODE_UNREACHABLE"}}
	m.Drain()
	if m.Connection != "Error" || m.Verified {
		t.Fatal("transport failure left node connected")
	}
	m.Disconnect()
	m.jobs <- completion{kind: "ping", generation: m.Generation - 1}
	m.Drain()
	if m.Connection != "Disconnected" {
		t.Fatal("stale completion reconnected")
	}
}

func TestSearchPreviewAndChangedPin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "client")
	c, err := nodeclient.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(c, root)
	defer m.Shutdown()
	u := &ui{m: m, fields: map[string]string{}}
	m.Current().Input = "http wolves & snow"
	u.action("go")
	if m.Current().Target != "https://duckduckgo.com/?q=http+wolves+%26+snow" || !strings.Contains(m.Notice, "Press Go again") {
		t.Fatal("search submitted without preview")
	}
	c.Config.Fingerprint = strings.Repeat("a", 64)
	if err = m.Configure(c.Config); err != nil {
		t.Fatal(err)
	}
	changed := c.Config
	changed.Fingerprint = strings.Repeat("b", 64)
	if err = m.Configure(changed); err == nil {
		t.Fatal("silently accepted changed identity")
	}
	if err = m.Forget(); err != nil {
		t.Fatal(err)
	}
	if m.Client.Config.Fingerprint != "" || m.Client.Config.ClientID != "" {
		t.Fatal("forget retained node credential")
	}
}

type modelFetch func(context.Context, string) protocol.Envelope

func (f modelFetch) Get(c context.Context, s string) protocol.Envelope { return f(c, s) }
func TestNativeModelRealClientAndNode(t *testing.T) {
	s, err := node.Open(filepath.Join(t.TempDir(), "node"))
	if err != nil {
		t.Fatal(err)
	}
	s.Fetcher = modelFetch(func(context.Context, string) protocol.Envelope {
		return protocol.Envelope{Status: 404, Reason: "Not Found", FinalURL: "https://public.test/", DurationMS: 5}
	})
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	root := filepath.Join(t.TempDir(), "state")
	c, err := nodeclient.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	c.Config.Host = "127.0.0.1"
	c.Config.Port = ln.Addr().(*net.TCPAddr).Port
	c.Config.Fingerprint = s.Fingerprint
	code, err := s.Auth.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Enrol(ctx, code); err != nil {
		t.Fatal(err)
	}
	m := NewModel(c, root)
	m.MetadataOnly = true
	defer m.Shutdown()
	m.Operation("ping", "")
	await(t, m, func() bool { return m.Connection == "Connected" })
	m.Current().Input = "https://public.test"
	if err = m.Navigate(); err != nil {
		t.Fatal(err)
	}
	await(t, m, func() bool { return m.Current().State == "Completed" })
	if m.Connection != "Connected" || m.Current().Result.Status != 404 || len(m.Current().History) != 1 {
		t.Fatal("incorrect metadata state")
	}
	if strings.Contains(m.Notice, "token") {
		t.Fatal("unexpected credential diagnostic")
	}
}
func await(t *testing.T, m *Model, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		m.Drain()
		if condition() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("timed out: %s %s", m.Connection, m.ConnectionError)
}
func TestSDLNativeInputAndTabs(t *testing.T) {
	t.Setenv("SDL_VIDEODRIVER", "dummy")
	t.Setenv("SDL_RENDER_DRIVER", "software")
	if err := Run(Options{StateDir: filepath.Join(t.TempDir(), "state"), Smoke: true}); err != nil {
		t.Fatal(err)
	}
}

func TestApplyPreservesPendingApproval(t *testing.T) {
	root := filepath.Join(t.TempDir(), "client")
	c, err := nodeclient.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(c, root)
	defer m.Shutdown()
	u := &ui{m: m, fields: map[string]string{}}
	u.loadForm()
	u.fields["code"] = strings.Repeat("b", 64)
	u.fields["invite"] = "pending-invitation"
	u.fields["pin"] = strings.Repeat("a", 64)
	u.action("apply")
	if u.fields["code"] != strings.Repeat("b", 64) || u.fields["invite"] != "pending-invitation" {
		t.Fatal("Apply erased approval input")
	}
	u.action("settings")
	if u.fields["code"] != strings.Repeat("b", 64) {
		t.Fatal("opening Settings erased approval input")
	}
}
func TestManualConnectUsesDisplayedFieldsAndRecovers(t *testing.T) { testSetupFlow(t, false) }
func TestOneInvitationConnectAndRecover(t *testing.T)              { testSetupFlow(t, true) }
func testSetupFlow(t *testing.T, useInvitation bool) {
	s, err := node.Open(filepath.Join(t.TempDir(), "node"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	root := filepath.Join(t.TempDir(), "client")
	c, err := nodeclient.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(c, root)
	defer m.Shutdown()
	u := &ui{m: m, manual: !useInvitation, fields: map[string]string{}}
	u.loadForm()
	code, err := s.Auth.IssueCode()
	if err != nil {
		t.Fatal(err)
	}
	if useInvitation {
		u.fields["invite"] = enrolment.Invitation{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Fingerprint: s.Fingerprint, Code: code}.String()
		u.action("connect")
	} else {
		u.fields["host"] = "127.0.0.1"
		u.fields["port"] = fmt.Sprint(ln.Addr().(*net.TCPAddr).Port)
		u.fields["pin"] = s.Fingerprint
		u.fields["code"] = code
		u.action("enrol")
	}
	await(t, m, func() bool { return !m.busy })
	if m.Connection != "Connected" || !m.Verified {
		t.Fatalf("setup failed: %s", m.ConnectionError)
	}
	clientID := m.Client.Config.ClientID
	m.Client.Config.ClientID = ""
	m.Client.Config.Expires = time.Time{}
	m.Disconnect()
	u.fields["code"] = ""
	u.fields["invite"] = ""
	u.loadForm()
	u.action("connect")
	await(t, m, func() bool { return !m.busy })
	if m.Connection != "Connected" || m.Client.Config.ClientID != clientID {
		t.Fatalf("saved key recovery failed: %s", m.ConnectionError)
	}
}
func TestSDLResponsiveSizesAndDPI(t *testing.T) {
	t.Setenv("SDL_VIDEODRIVER", "dummy")
	t.Setenv("SDL_RENDER_DRIVER", "software")
	for _, size := range []struct {
		w, h  int
		scale float64
	}{{800, 600, 1}, {1366, 768, 1}, {1600, 1200, 2}, {1280, 900, 1.5}} {
		t.Run(fmt.Sprintf("%dx%d@%.1f", size.w, size.h, size.scale), func(t *testing.T) {
			for _, view := range []string{"request", "settings", "manual"} {
				if err := Run(Options{StateDir: filepath.Join(t.TempDir(), "client"), Smoke: true, Width: size.w, Height: size.h, UIScale: size.scale, SmokeView: view}); err != nil {
					t.Fatal(view, err)
				}
			}
		})
	}
}

func TestSDLDefaultFitsDisplayWorkArea(t *testing.T) {
	t.Setenv("SDL_VIDEODRIVER", "dummy")
	t.Setenv("SDL_RENDER_DRIVER", "software")
	t.Setenv("SDL_DISPLAY_USABLE_BOUNDS", "0,0,1024,600")
	root := filepath.Join(t.TempDir(), "client")
	if err := Run(Options{StateDir: root, Smoke: true}); err != nil {
		t.Fatal(err)
	}
	paths, _ := filepath.Glob(filepath.Join(root, "logs", "*.jsonl"))
	found := false
	for _, p := range paths {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			var e struct{ Event, Status string }
			if json.Unmarshal([]byte(line), &e) != nil {
				t.Fatal("bad diagnostic JSON")
			}
			if e.Event == "window_metrics" {
				var w, h, pw, ph int
				var scale float64
				if n, err := fmt.Sscanf(e.Status, "content=%dx%d pixels=%dx%d scale=%f", &w, &h, &pw, &ph, &scale); err != nil || n != 5 {
					t.Fatal("missing window metrics")
				}
				if pw > 1024 || ph > 600 || w < 520 || h < 420 {
					t.Fatalf("window does not fit display: %dx%d", pw, ph)
				}
				found = true
			}
		}
	}
	if !found {
		t.Fatal("window metrics were not logged")
	}
}

func TestSettingsAcceptInvitationAndBareCodeWithPastedEndpoint(t *testing.T) {
	s, err := node.Open(filepath.Join(t.TempDir(), "node"))
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, ln) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	}()
	for _, bare := range []bool{false, true} {
		root := filepath.Join(t.TempDir(), "client")
		c, err := nodeclient.Load(root)
		if err != nil {
			t.Fatal(err)
		}
		m := NewModel(c, root)
		defer m.Shutdown()
		u := &ui{m: m, fields: map[string]string{}}
		u.loadForm()
		code, err := s.Auth.IssueCode()
		if err != nil {
			t.Fatal(err)
		}
		invitation := enrolment.Invitation{Host: "127.0.0.1", Port: ln.Addr().(*net.TCPAddr).Port, Fingerprint: s.Fingerprint, Code: code}.String()
		u.fields["invite"] = invitation
		if bare {
			u.fields["invite"] = code
			u.fields["host"] = ln.Addr().String()
			u.fields["pin"] = s.Fingerprint
		}
		u.action("connect")
		deadline := time.Now().Add(4 * time.Second)
		for m.Connection != "Connected" && m.Connection != "Error" && time.Now().Before(deadline) {
			m.Drain()
			time.Sleep(5 * time.Millisecond)
		}
		if m.Connection != "Connected" || !m.Verified {
			t.Fatalf("bare=%t failed: %s %s", bare, m.Connection, m.Notice)
		}
		again, err := nodeclient.Load(root)
		if err != nil || again.Config.ClientID == "" {
			t.Fatal("enrolment was not persisted", err)
		}
		logs, err := os.ReadFile(filepath.Join(root, "logs", "client.log"))
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(logs), code) || strings.Contains(string(logs), invitation) || !strings.Contains(string(logs), "enrolment_result") {
			t.Fatal("missing or unsafe diagnostics")
		}
	}
}
func TestBareCodeCannotSilentlyTrustDiscoveredPin(t *testing.T) {
	root := filepath.Join(t.TempDir(), "client")
	c, err := nodeclient.Load(root)
	if err != nil {
		t.Fatal(err)
	}
	m := NewModel(c, root)
	defer m.Shutdown()
	u := &ui{m: m, fields: map[string]string{}}
	u.loadForm()
	u.fields["invite"] = strings.Repeat("a", 64)
	u.action("connect")
	if !u.manual || u.fields["code"] == "" || m.busy || m.Client.Config.Fingerprint != "" {
		t.Fatal("bare code bypassed explicit node identity verification")
	}
	if !strings.Contains(m.Notice, "pin") {
		t.Fatal("missing actionable pin explanation", m.Notice)
	}
}

func TestBrowserStartupMessagesHaveSpecificRemedies(t *testing.T) {
	for code, expected := range map[string]string{"BROWSER_NAMESPACE_DENIED": "AppArmor", "BROWSER_USER_SESSION_UNAVAILABLE": "SSH", "BROWSER_DEPENDENCY_MISSING": "library", "BROWSER_TASK_LIMIT": "TasksMax", "BROWSER_START_FAILED": "diagnose-browser"} {
		if !strings.Contains(ConnectionMessage(code), expected) {
			t.Fatalf("missing remedy for %s: %s", code, ConnectionMessage(code))
		}
	}
}
