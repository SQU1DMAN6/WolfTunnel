package frontend

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"wolftunnel/internal/audit"
	"wolftunnel/internal/nodeclient"
	"wolftunnel/internal/storage"
	"wolftunnel/internal/transport"
)

// All positions use current window content coordinates. No fixed-size render
// target is enlarged to fit the display; text is rasterised at physical DPI.
func (u *ui) addControl(c control) {
	if u.clipped {
		top, bottom := 148, u.height-118
		if c.y+c.h <= top || c.y >= bottom {
			return
		}
		end := min(c.y+c.h, bottom)
		c.y = max(c.y, top)
		c.h = end - c.y
	}
	u.controls = append(u.controls, c)
}
func (u *ui) draw() {
	oldW, oldH, oldDPI := u.width, u.height, u.dpi
	u.width, u.height, u.pixelWidth, u.pixelHeight, u.dpi = dimensions()
	w, h := u.width, u.height
	if (oldW != w || oldH != h || oldDPI != u.dpi) && u.m.Client.Log != nil {
		u.m.Client.Log.Log(audit.Entry{Component: "frontend", Event: "window_metrics", Status: fmt.Sprintf("content=%dx%d pixels=%dx%d scale=%.2f", w, h, u.pixelWidth, u.pixelHeight, u.dpi)})
	}
	clearFrame()
	u.controls = nil
	u.clipped = false
	rect(0, 0, w, 40, 0x172331)
	text(16, 10, 110, "WolfTunnel", accent)
	for i, label := range []string{"File", "View", "Node", "Help"} {
		name := label
		if label == "Node" {
			name = "Connection"
		}
		u.button(132+i*64, 3, 60, 34, label, "menu:"+name)
	}
	u.button(w-110, 3, 98, 34, "Settings", "settings")
	if !u.m.MetadataOnly && w > 780 {
		u.button(w-260, 3, 138, 34, "End session", "end-session")
	}
	count := max(1, min(7, (w-68)/122))
	if u.m.Active < u.tabStart {
		u.tabStart = u.m.Active
	}
	if u.m.Active >= u.tabStart+count {
		u.tabStart = u.m.Active - count + 1
	}
	u.tabStart = min(u.tabStart, max(0, len(u.m.Tabs)-count))
	tabW := min(172, (w-68)/min(count, len(u.m.Tabs)))
	for i := u.tabStart; i < min(len(u.m.Tabs), u.tabStart+count); i++ {
		t := u.m.Tabs[i]
		x := 12 + (i-u.tabStart)*tabW
		c := 0x1b2736
		if i == u.m.Active {
			c = 0x34485d
		}
		rect(x, 47, tabW-4, 34, c)
		text(x+9, 55, tabW-42, t.Title, ink)
		u.addControl(control{x: x, y: 47, w: tabW - 36, h: 34, action: fmt.Sprintf("tab:%d", i)})
		u.button(x+tabW-35, 47, 29, 34, "×", fmt.Sprintf("close:%d", i))
	}
	u.button(w-48, 47, 36, 34, "+", "new")
	t := u.m.Current()
	if !t.Settings {
		for i, b := range [][2]string{{"←", "back"}, {"→", "forward"}, {"↻", "reload"}, {"×", "cancel"}} {
			u.button(12+i*39, 94, 34, 36, b[0], b[1])
		}
		u.field(174, 94, w-250, "url", t.Input, false)
		u.button(w-66, 94, 54, 36, "Go", "go")
		if u.m.MetadataOnly {
			u.requestDraw()
		} else {
			u.viewportDraw()
		}
	} else {
		u.settingsDraw()
	}
	notice := u.m.Notice
	color := muted
	if u.m.Connection == "Error" {
		color = 0xffb8ba
	}
	if u.m.ConnectionError == "NODE_IDENTITY_CHANGED" {
		notice = "Security: node identity changed. Verify with the owner before replacing it."
		color = 0xffb8ba
	}
	if !u.m.MetadataOnly && !u.m.Current().Settings {
		page := u.m.Current()
		if u.m.opening || u.m.busy {
			notice = transport.StateMessage(u.m.Network.State)
		}
		if u.m.Connection != "Error" && u.m.RemoteFailureCode == "" {
			if page.Result.Code != "" {
				notice = ConnectionMessage(page.Result.Code)
			} else if page.Result.Status != 0 {
				notice = fmt.Sprintf("%s · HTTP %d", page.State, page.Result.Status)
			}
		}
		u.wrapped(20, h-78, w-40, notice, color, 1)
	} else {
		u.wrapped(20, h-108, w-40, notice, color, 3)
	}
	rect(0, h-42, w, 42, 0x172331)
	stateColor := muted
	if u.m.Connection == "Connected" {
		stateColor = accent
	} else if u.m.Connection == "Error" {
		stateColor = 0xffa0a6
	}
	rect(16, h-24, 6, 6, stateColor)
	text(32, h-29, 124, u.m.Connection, stateColor)
	status := u.m.Client.Config.Address()
	if !u.m.MetadataOnly && u.m.Remote != nil {
		if u.remoteFocus {
			status += " · Page keyboard"
		} else {
			status += " · Click page to type"
		}
		if u.pinchActive {
			status += " · Pinch"
		}
	}
	if u.m.opening || u.m.busy {
		status = transport.StateMessage(u.m.Network.State)
	}
	text(166, h-29, max(0, w-330), status, muted)
	if u.m.Verified {
		text(w-300, h-29, 130, fmt.Sprintf("Verified · %d ms", u.m.RTT.Milliseconds()), accent)
	}
	if u.menu != "" {
		u.menuDraw()
	}
	if u.help != "" {
		u.helpDraw()
	}
}
func (u *ui) wrapped(x, y, w int, value string, color, lines int) int {
	words := strings.Fields(value)
	line := ""
	row := 0
	for _, word := range words {
		next := strings.TrimSpace(line + " " + word)
		if textWidth(next) > w && line != "" {
			text(x, y+row*22, w, line, color)
			row++
			if row >= lines {
				return row * 22
			}
			line = word
		} else {
			line = next
		}
	}
	if line != "" && row < lines {
		text(x, y+row*22, w, line, color)
		row++
	}
	return row * 22
}
func (u *ui) requestDraw() {
	t := u.m.Current()
	w, h := u.width, u.height
	left := max(20, (w-790)/2)
	cardW := w - 2*left
	top := 170 - u.scroll
	u.clipped = true
	setClip(0, 148, w, max(0, h-266))
	defer func() { setClip(0, 0, 0, 0); u.clipped = false }()
	end := top + 246
	if t.State == "Ready" && t.Result.Status == 0 && t.Result.Code == "" {
		headline(left, top, cardW, "WolfTunnel Starts Here.", ink)
		prompt := "Enter a URL or search above."
		if u.m.Connection != "Connected" {
			prompt = "Connect a node, then enter a URL or search above."
		}
		u.wrapped(left, top+45, cardW, prompt, muted, 2)
		if u.m.Connection != "Connected" {
			u.button(left, top+106, 176, 38, "Set up connection", "settings")
		}
		text(left, top+168, cardW, "HTTP results only · webpages are not rendered", muted)
		if t.Target != "" {
			u.wrapped(left, top+204, cardW, "Search preview: "+t.Target, accent, 2)
		}
	} else {
		title := t.State
		sub := ""
		if t.Result.Status > 0 {
			title = fmt.Sprintf("%d %s", t.Result.Status, t.Result.Reason)
			sub = t.Result.FinalURL
		} else if t.Result.Code != "" {
			title = "Request couldn't complete"
			sub = ConnectionMessage(t.Result.Code)
		} else if t.State == "Loading" {
			title = "Fetching request…"
			sub = t.Target
		}
		headline(left, top, cardW, title, ink)
		u.wrapped(left, top+44, cardW, sub, muted, 2)
		if t.Result.Code != "" && t.Result.Status > 0 {
			u.wrapped(left, top+91, cardW, ConnectionMessage(t.Result.Code), 0xffb8ba, 2)
		}
		y := top + 136
		if t.Result.Status > 0 {
			columns := 4
			if cardW < 660 {
				columns = 2
			}
			metrics := [][2]string{{"Duration", fmt.Sprintf("%d ms", t.Result.DurationMS)}, {"Received", fmt.Sprintf("%d bytes", t.Result.BodyLength)}, {"Redirects", strconv.Itoa(t.Result.Redirects)}, {"Type", t.Result.ContentType}}
			for i, m := range metrics {
				x := left + (i%columns)*(cardW/columns)
				row := y + (i/columns)*66
				rect(x, row, cardW/columns-10, 56, 0x192534)
				text(x+12, row+7, cardW/columns-30, m[0], muted)
				text(x+12, row+30, cardW/columns-30, m[1], ink)
			}
			y += (4/columns)*66 + 18
		}
		u.button(left, y, 140, 34, "Details", "request-details")
		end = y + 42
		if u.resultDetails {
			detail := "State: " + t.State
			if t.Result.Code != "" {
				detail += " · Node error: " + t.Result.Code
			}
			detail += " · Request: " + t.Result.RequestID
			end += u.wrapped(left, y+50, cardW, detail, muted, 3) + 18
		}
	}
	u.maxScroll = max(0, end+u.scroll-(h-124))
	if u.scroll > u.maxScroll {
		u.scroll = u.maxScroll
	}
	if u.maxScroll > 0 {
		text(w-90, h-148, 70, "↕ Scroll", muted)
	}
}
func (u *ui) loadForm() {
	c := u.m.Client.Config
	u.fields["host"] = c.Host
	u.fields["port"] = strconv.Itoa(c.Port)
	u.fields["pin"] = c.Fingerprint
	u.fields["timeout"] = strconv.Itoa(int(c.Timeout / time.Second))
	u.fields["transport"] = c.Transport
	u.fields["proxy"] = c.Proxy
	// Pending invitation/code fields intentionally survive Apply and tab changes.
}
func (u *ui) settingsDraw() {
	w, h := u.width, u.height
	left := 20
	width := w - 40
	u.clipped = true
	setClip(0, 148, w, max(0, h-266))
	defer func() { setClip(0, 0, 0, 0); u.clipped = false }()
	y := 158 - u.scroll
	headline(left, y, width, "Node connection", ink)
	y += 46
	if !u.manual {
		text(left, y, width, "Invitation or enrolment code from the node owner", muted)
		y += 27
		u.field(left, y, width, "invite", u.fields["invite"], true)
		y += 51
		u.button(left, y, 140, 38, "Connect", "connect")
		u.button(left+154, y, 148, 38, "Manual setup", "manual")
		y += 62
		u.wrapped(left, y, width, "Use an invitation received from the owner over a trusted channel. Your client key is generated automatically.", muted, 3)
		y += 84
	} else {
		text(left, y, width, "Address", muted)
		text(w-130, y, 110, "Port", muted)
		y += 25
		u.field(left, y, width-124, "host", u.fields["host"], false)
		u.field(w-130, y, 110, "port", u.fields["port"], false)
		y += 47
		text(left, y, width, "Owner-verified SHA-256 pin", muted)
		y += 25
		u.field(left, y, width, "pin", u.fields["pin"], false)
		y += 47
		text(left, y, width, "Enrolment code · new installations only", muted)
		y += 25
		u.field(left, y, width, "code", u.fields["code"], true)
		y += 50
		u.button(left, y, 124, 38, "Connect", "enrol")
		u.button(left+136, y, 110, 38, "Apply", "apply")
		u.button(left+258, y, 120, 38, "Discover", "discover")
		y += 48
		u.button(left, y, 170, 34, "Use an invitation", "manual")
		y += 54
	}
	if u.m.Client.Config.ClientID != "" {
		text(left, y, width, "Enrolled · "+u.m.Client.Config.Expires.Local().Format("valid until 2 Jan 2006"), accent)
		y += 35
		u.button(left, y, 120, 34, "Ping and Reconnect", "ping")
		u.button(left+132, y, 120, 34, "Renew", "renew")
		u.button(left+264, y, 130, 34, "Disconnect", "disconnect")
		y += 48
	}
	provider := "DuckDuckGo"
	if u.m.Client.Config.SearchTemplate == nodeclient.Google {
		provider = "Google"
	}
	u.button(left, y, 230, 34, "Search: "+provider, "search")
	u.button(left+244, y, 150, 34, "Diagnostics", "details")
	y += 53
	mode := u.fields["transport"]
	if mode == "" {
		mode = "tls-tcp"
	}
	u.button(left, y, min(260, width), 34, "Transport: "+mode, "transport")
	y += 47
	if mode == "http-connect" {
		text(left, y, width, "Approved HTTP(S) proxy · URL without credentials", muted)
		y += 25
		u.field(left, y, width, "proxy", u.fields["proxy"], false)
		y += 50
	}
	if u.m.Client.Config.Details {
		text(left, y, width, "Diagnostics", accent)
		y += 30
		y += u.wrapped(left, y, width, fmt.Sprintf("Stage: %s · Channel: %s · Retry: %d", u.m.Network.State, u.m.Network.Channel, u.m.Network.Retry), muted, 2) + 8
		for _, line := range []string{"Identity: " + u.m.Client.Config.Fingerprint, "Discovered (unverified): " + u.m.Discovered, "Client ID: " + u.m.Client.Config.ClientID, "Last error: " + u.m.ConnectionError, "Client log: " + u.clientLogPath(), fmt.Sprintf("Window: %d × %d · Pixels: %d × %d · Scale: %.2f", w, h, u.pixelWidth, u.pixelHeight, u.dpi)} {
			y += u.wrapped(left, y, width, line, muted, 2) + 8
		}
		text(left, y, 175, "Request timeout (s)", muted)
		u.field(left+184, y-7, 100, "timeout", u.fields["timeout"], false)
		y += 45
		u.button(left, y, 140, 34, "Forget node", "forget")
		y += 50
	}
	u.maxScroll = max(0, y+u.scroll-(h-126))
	if u.scroll > u.maxScroll {
		u.scroll = u.maxScroll
	}
	if u.maxScroll > 0 {
		text(w-90, h-148, 70, "↕ Scroll", muted)
	}
}
func (u *ui) menuDraw() {
	items := map[string][][2]string{"File": {{"New tab", "new"}, {"Close tab", "close-active"}, {"Settings", "settings"}, {"Quit", "quit"}}, "View": {{"Reload request", "reload"}, {"Request details", "request-details"}}, "Connection": {{"Connect", "settings"}, {"Disconnect", "disconnect"}, {"Test connection", "ping"}, {"Node details", "settings"}}, "Help": {{"About WolfTunnel", "help:about"}, {"Security", "help:security"}, {"Documentation", "help:docs"}}}
	if !u.m.MetadataOnly {
		items["View"] = [][2]string{{"Reload page", "reload"}}
		items["View"] = append(items["View"], [2]string{"Verification help", "verification"}, [2]string{"End browser session", "end-session"})
	}
	x := 132
	for i, s := range []string{"File", "View", "Connection", "Help"} {
		if s == u.menu {
			x += i * 64
		}
	}
	x = min(x, u.width-254)
	for i, item := range items[u.menu] {
		u.button(x, 40+i*39, 242, 39, item[0], item[1])
	}
}
func (u *ui) helpDraw() {
	w, h := u.width, u.height
	rect(16, 142, w-32, h-270, 0x25374a)
	headline(32, 160, w-64, "WolfTunnel v0.6", accent)
	value := "Webpages run on your trusted node. This client displays pixels and sends browser input over verified TLS. Sites see the node IP; logins and fingerprinting can still identify you."
	if u.help == "docs" {
		value = strings.TrimSpace(guide)
	}
	if u.help == "verification" {
		value = "Verification is a website response, not a connection failure. Click the page to direct keyboard input there; use pointer movement, dragging, scrolling, Tab and text normally. Page state remains within this browser session. WolfTunnel does not solve challenges, guarantee website acceptance or restore site cookies after a disconnect."
	}
	if u.help == "security" {
		value = "Use invitations supplied over a trusted channel. Discovery alone does not verify identity. Client keys are generated automatically and never sent to the node. Codes are single use; renew credentials before expiry. Logs omit codes, signatures, URL paths and queries. TLS hides browsing contents, while network timing and volume remain visible. Details: docs/THREAT_MODEL-v0_6.md."
	}
	u.wrapped(32, 211, w-64, value, ink, max(1, (h-390)/22))
	u.button(w-160, h-176, 122, 34, "Close", "help-close")
}

func (u *ui) clientLogPath() string {
	dir, err := storage.ClientLogDir(u.m.Root)
	if err != nil {
		return "unavailable"
	}
	return filepath.Join(dir, "client.log")
}
