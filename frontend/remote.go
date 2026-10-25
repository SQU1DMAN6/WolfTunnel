package frontend

import (
	"context"
	"net/url"

	"wolftunnel/internal/nodeclient"
	v3 "wolftunnel/internal/protocol/v3"
)

func (m *Model) navigateRemote() error {
	t := m.Current()
	m.RemoteFailureCode = ""
	t.State = "Loading"
	t.Result.Code = ""
	t.Result.Status = 0
	if u, e := url.Parse(t.Target); e == nil {
		t.Title = u.Hostname()
	}
	if m.Remote != nil {
		return m.Remote.Navigate(t.ID, t.Target)
	}
	if m.opening {
		return nil
	}
	if !m.reserveWorker() {
		return &nodeclient.Error{Code: "RESOURCE_LIMIT"}
	}
	m.opening = true
	client := m.Client
	generation := m.Generation
	w, h := m.ViewportWidth, m.ViewportHeight
	if m.streamCancel != nil {
		m.streamCancel()
	}
	ctx, cancel := context.WithCancel(m.ctx)
	m.streamCancel = cancel
	go func() {
		defer m.finishWorker()
		b, e := client.OpenBrowser(ctx, nodeclient.BrowserOptions{Width: w, Height: h})
		if e != nil {
			cancel()
		}
		m.deliver(completion{kind: "browser_open", stream: b, err: e, generation: generation})
	}()
	return nil
}
func (m *Model) SelectTab(i int) {
	if i < 0 || i >= len(m.Tabs) {
		return
	}
	m.Active = i
	t := m.Current()
	if m.Remote != nil && !t.Settings {
		if e := m.Remote.Select(t.ID); e != nil {
			m.Notice = ConnectionMessage(e.Error())
		}
	}
}
func (m *Model) drainRemote() bool {
	if m.Remote == nil {
		return false
	}
	changed := false
	for {
		select {
		case frame := <-m.Remote.Frames:
			changed = true
			id := frame.Update.Tab.String()
			for _, t := range m.Tabs {
				if t.ID == id {
					t.Frame = frame.Image
					t.FrameSequence = frame.Sequence
					t.RemoteFrame = frame
					if t.State == "Ready" {
						t.State = "Completed"
					}
					break
				}
			}
		case event := <-m.Remote.Events:
			changed = true
			c := event.Control
			if event.Type == v3.Error {
				m.RemoteFailureCode = c.Code
				m.Notice = ConnectionMessage(c.Code)
			}
			for _, t := range m.Tabs {
				if t.ID != c.Tab {
					continue
				}
				switch event.Type {
				case v3.NavigationStatus:
					t.State = c.State
					if c.Target != "" {
						t.Target, t.Input = c.Target, c.Target
						if u, e := url.Parse(c.Target); e == nil {
							t.Title = u.Hostname()
						}
					}
					if c.Status != 0 {
						t.Result.Status = c.Status
						m.LastStatus = c.Status
					}
					if c.Code != "" {
						t.Result.Code = c.Code
					}
				case v3.SessionStatus:
					if t.State != "Loading" {
						t.State = "Ready"
					}
				case v3.Error:
					t.State = "Failed"
					t.Result.Code = c.Code
				}
			}
		default:
			select {
			case <-m.Remote.Done():
				m.Remote.Close()
				m.Remote = nil
				for _, t := range m.Tabs {
					t.Frame = nil
					t.State = "Disconnected"
				}
				m.Notice = "Remote browser disconnected. Reconnect or navigate to start a fresh session."
				if m.RemoteFailureCode != "" {
					m.Notice = ConnectionMessage(m.RemoteFailureCode)
				}
				changed = true
			default:
			}
			return changed
		}
	}
}
func (m *Model) SendInput(i v3.Input) error {
	if m.Remote == nil || m.Current().Settings {
		return nil
	}
	err := m.Remote.Input(m.Current().ID, i)
	if err != nil {
		m.Client.Report("browser_input_failed", err)
	}
	return err
}
func (m *Model) SetViewport(w, h int) {
	if w < 320 {
		w = 320
	}
	if h < 200 {
		h = 200
	}
	if w > 1920 {
		w = 1920
	}
	if h > 1200 {
		h = 1200
	}
	m.ViewportWidth = w
	m.ViewportHeight = h
	if m.Remote != nil {
		_ = m.Remote.Resize(w, h)
	}
}
