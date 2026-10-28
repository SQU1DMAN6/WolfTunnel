package frontend

import (
	"errors"
	"net/url"
	"strings"
	"time"
	"unicode"
	"wolftunnel/internal/browsernet"
)

func statusURL(raw string) string {
	u, e := url.Parse(raw)
	if e != nil || u.User != nil || len(raw) > 8192 {
		return ""
	}
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, raw)
}
func (m *Model) navigationNotice(tab, message string, ttl time.Duration) {
	m.LinkTarget = ""
	m.NavigationTab = tab
	m.NavigationText = message
	m.NavigationUntil = time.Now().Add(ttl)
}
func (m *Model) navigationStatus() string {
	if m.RemoteFailureCode != "" || m.Connection == "Error" {
		return ""
	}
	if time.Now().Before(m.NavigationUntil) {
		return m.NavigationText
	}
	m.NavigationText = ""
	if m.LinkTarget != "" {
		return statusURL(m.LinkTarget)
	}
	return ""
}
func (m *Model) OpenTargetInNewTab(origin, target string) error {
	found := false
	for _, t := range m.Tabs {
		if t.ID == origin && !t.Settings {
			found = true
			break
		}
	}
	if !found || m.Remote == nil {
		return errors.New("SESSION_INVALID")
	}
	if browsernet.Validate(target) != nil {
		return errors.New("NODE_POLICY_REJECTED")
	}
	before := len(m.Tabs)
	previous := m.Active
	m.navigationNotice(origin, "Opening new tab · "+statusURL(target), 3*time.Second)
	m.NewTab()
	if len(m.Tabs) == before {
		return errors.New("TAB_LIMIT")
	}
	t := m.Current()
	t.Input = target
	t.Target = target
	t.State = "Loading"
	if e := m.navigateRemote(); e != nil {
		m.CloseTab(m.Active)
		m.SelectTab(previous)
		return e
	}
	m.NavigationTab = t.ID
	return nil
}
