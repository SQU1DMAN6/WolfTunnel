package frontend

/*
#cgo pkg-config: sdl3 sdl3-ttf
#cgo linux LDFLAGS: -lm
#include "bridge.h"
*/
import "C"

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unsafe"

	"wolftunnel/internal/audit"
	"wolftunnel/internal/nodeclient"
)

//go:embed assets/DejaVuSans.ttf
var fontData []byte

//go:embed assets/guide.txt
var guide string

type Options struct {
	StateDir         string
	Smoke            bool
	Screenshot       string
	Width, Height    int
	UIScale          float64
	SmokeView        string
	MetadataOnly     bool
	BrowserSmoke     string
	SmokeTimeout     time.Duration
	OpenURL          string
	InteractionSmoke bool
}
type control struct {
	x, y, w, h           int
	label, action, field string
}
type ui struct {
	view                                   viewRect
	remoteFocus                            bool
	heldButtons                            int
	textureTab                             string
	textureSequence                        uint64
	desiredW, desiredH                     int
	resizeAt                               time.Time
	m                                      *Model
	controls                               []control
	focus                                  string
	cursor                                 int
	selected                               bool
	fields                                 map[string]string
	menu, help                             string
	quit                                   bool
	dirty                                  bool
	width, height, pixelWidth, pixelHeight int
	dpi                                    float64
	tabStart, scroll, maxScroll            int
	manual, resultDetails, clipped         bool
	enrolling                              bool
}

func Run(o Options) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	client, err := nodeclient.Load(o.StateDir)
	if err != nil {
		return err
	}
	data := C.CBytes(fontData)
	defer C.free(data)
	if C.wolf_init(data, C.int(len(fontData))) == 0 {
		err := errors.New(C.GoString(C.SDL_GetError()))
		C.wolf_destroy()
		return err
	}
	defer C.wolf_destroy()
	if o.SmokeView != "" && o.SmokeView != "request" && o.SmokeView != "settings" && o.SmokeView != "manual" {
		return errors.New("smoke view must be request, settings or manual")
	}
	if o.Width > 0 && o.Height > 0 {
		C.wolf_resize(C.int(o.Width), C.int(o.Height))
	}
	if o.UIScale != 0 {
		if o.UIScale < 0.75 || o.UIScale > 3 {
			return errors.New("UI scale must be 0 (automatic) or 0.75–3")
		}
		C.wolf_scale(C.float(o.UIScale))
	}
	u := &ui{m: NewModel(client, o.StateDir), focus: "url", fields: map[string]string{}, dirty: true}
	defer u.m.Shutdown()
	u.loadForm()
	u.m.MetadataOnly = o.MetadataOnly
	u.manual = client.Config.Fingerprint != "" && client.Config.ClientID == ""
	if !o.Smoke && client.Config.ClientID == "" && o.BrowserSmoke == "" {
		u.action("settings")
	}
	if !o.Smoke && client.Config.ClientID != "" && client.Config.Fingerprint != "" {
		u.enrolling = true
		u.m.Operation("connect", "")
	}
	frame := 0
	smokeDeadline := time.Now().Add(o.SmokeTimeout)
	if o.SmokeTimeout == 0 {
		smokeDeadline = time.Now().Add(25 * time.Second)
	}
	browserStage := 0
	var clicked time.Time
	for !u.quit {
		if (o.BrowserSmoke != "" || (o.OpenURL != "" && o.Screenshot != "")) && time.Now().After(smokeDeadline) {
			return fmt.Errorf("BROWSER_SMOKE_TIMEOUT: stage=%d connection=%s state=%s image=%t remote=%t", browserStage, u.m.Connection, u.m.Current().State, u.m.Current().Frame != nil, u.m.Remote != nil)
		}
		if !u.resizeAt.IsZero() && time.Now().After(u.resizeAt) {
			u.m.SetViewport(u.desiredW, u.desiredH)
			u.resizeAt = time.Time{}
			u.dirty = true
		}
		if o.Smoke {
			switch frame {
			case 0:
				C.wolf_key(C.int('t'), 1)
			case 1:
				C.wolf_key(C.int('l'), 1)
			case 2:
				pushText("wolf tunnel test")
			case 3:
				u.smokeClick("go")
			case 4:
				u.smokeClick("settings")
			case 5:
				u.smokeClick("tab:0")
			case 6:
				C.wolf_key(C.int('w'), 1)
			case 7:
				if len(u.m.Tabs) != 2 || !u.m.Tabs[1].Settings || u.m.Tabs[0].Input != "wolf tunnel test" {
					return fmt.Errorf("native input/tab smoke failed: tabs=%d active=%d content=%dx%d scale=%.2f", len(u.m.Tabs), u.m.Active, u.width, u.height, u.dpi)
				}
				u.m.Active = 0
				if o.SmokeView == "settings" || o.SmokeView == "manual" {
					u.m.Settings()
					u.manual = o.SmokeView == "manual"
					u.scroll = 0
					u.focus = ""
				}
				u.m.Notice = "Native input and tabs verified."
				u.dirty = true
			}
		}
		var e C.WolfEvent
		for C.wolf_poll(&e) != 0 {
			u.event(e)
			u.dirty = true
		}
		if u.m.Drain() {
			if u.enrolling && !u.m.busy {
				if u.m.Connection == "Connected" {
					u.fields["code"] = ""
					u.fields["invite"] = ""
					u.loadForm()
					if u.m.Current().Settings {
						for index, tab := range u.m.Tabs {
							if !tab.Settings {
								u.m.SelectTab(index)
								u.setFocus("url")
								break
							}
						}
					}
				}
				u.enrolling = false
			}
			u.dirty = true
		}
		if o.OpenURL != "" {
			done, err := u.openPage(o, &browserStage, &clicked)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
		if o.BrowserSmoke != "" {
			done, err := u.browserSmoke(o, &browserStage, &clicked)
			if err != nil {
				return err
			}
			if done {
				return nil
			}
		}
		if u.dirty {
			u.draw()
			if o.Smoke {
				for _, c := range u.controls {
					if c.x < 0 || c.y < 0 || c.x+c.w > u.width || c.y+c.h > u.height {
						return fmt.Errorf("native control outside window: %s at %dx%d", c.action, u.width, u.height)
					}
				}
			}
			if o.Screenshot != "" && o.Smoke && frame >= 8 {
				path := C.CString(o.Screenshot)
				ok := C.wolf_capture(path)
				C.free(unsafe.Pointer(path))
				if ok == 0 {
					return errors.New("screenshot failed")
				}
			}
			C.wolf_present()
			u.dirty = false
		}
		if o.Smoke && frame >= 8 {
			if o.Screenshot != "" {
				u.draw()
				path := C.CString(o.Screenshot)
				ok := C.wolf_capture(path)
				C.free(unsafe.Pointer(path))
				if ok == 0 {
					return errors.New("screenshot failed")
				}
			}
			return nil
		}
		frame++
		C.SDL_Delay(8)
	}
	return nil
}
func pushText(s string) { v := C.CString(s); C.wolf_input(v); C.free(unsafe.Pointer(v)) }
func clearFrame()       { C.wolf_clear() }
func dimensions() (int, int, int, int, float64) {
	var w, h, pw, ph C.int
	var scale C.float
	C.wolf_dimensions(&w, &h, &pw, &ph, &scale)
	return int(w), int(h), int(pw), int(ph), float64(scale)
}
func setClip(x, y, w, h int) { C.wolf_clip(C.int(x), C.int(y), C.int(w), C.int(h)) }
func headline(x, y, w int, s string, color int) {
	v := C.CString(s)
	C.wolf_text_sized(C.float(x), C.float(y), C.float(w), v, C.int(color), 26)
	C.free(unsafe.Pointer(v))
}
func textWidth(s string) int {
	v := C.CString(s)
	w := C.wolf_text_width(v)
	C.free(unsafe.Pointer(v))
	return int(w)
}
func (u *ui) smokeClick(action string) {
	for _, c := range u.controls {
		if c.action == action {
			C.wolf_click(C.float(c.x+c.w/2), C.float(c.y+c.h/2))
			return
		}
	}
}
func text(x, y, w int, s string, color int) {
	v := C.CString(s)
	C.wolf_text(C.float(x), C.float(y), C.float(w), v, C.int(color))
	C.free(unsafe.Pointer(v))
}
func rect(x, y, w, h, color int) {
	C.wolf_rect(C.float(x), C.float(y), C.float(w), C.float(h), C.int(color))
}

const ink = 0xe2e9f1
const muted = 0x99aabb
const accent = 0x6ce5b1

func (u *ui) button(x, y, w, h int, label, action string) {
	rect(x, y, w, h, 0x29374a)
	text(x+10, y+8, w-16, label, ink)
	u.addControl(control{x: x, y: y, w: w, h: h, label: label, action: action})
}
func (u *ui) field(x, y, w int, name, value string, secret bool) {
	color := 0x1d2b3b
	if u.focus == name {
		color = 0x34485d
	}
	rect(x, y, w, 36, color)
	display := value
	if secret {
		display = strings.Repeat("•", len([]rune(value)))
	}
	if u.focus == name {
		r := []rune(display)
		pos := min(u.cursor, len(r))
		display = string(r[:pos]) + "|" + string(r[pos:])
		if u.selected {
			display = "[" + display + "]"
		}
	}
	// Keep the editing tail visible for long URL/search strings.
	r := []rune(display)
	maxChars := max(8, (w-24)/10)
	if len(r) > maxChars {
		start := 0
		if u.focus == name {
			start = max(0, min(u.cursor, len(r))-maxChars+5)
		}
		if start+maxChars > len(r) {
			start = len(r) - maxChars
		}
		display = string(r[start : start+maxChars])
	}
	text(x+8, y+7, w-16, display, ink)
	u.addControl(control{x: x, y: y, w: w, h: 36, field: name})
}
func (u *ui) setFocus(name string) {
	u.focus = name
	u.cursor = len([]rune(u.value()))
	u.selected = false
	if u.m.Current().Settings {
		positions := map[string]int{"invite": 231, "host": 229, "port": 229, "pin": 301, "code": 373}
		if y, ok := positions[name]; ok && u.height > 0 {
			if y-u.scroll < 148 {
				u.scroll = max(0, y-156)
			} else if y+36-u.scroll > u.height-118 {
				u.scroll = max(0, y+44-(u.height-118))
			}
		}
	}
}
func (u *ui) value() string {
	if u.focus == "url" {
		if u.m.Current().Settings {
			return ""
		}
		return u.m.Current().Input
	}
	return u.fields[u.focus]
}
func (u *ui) setValue(s string) {
	if u.focus == "" {
		return
	}
	if u.focus == "url" {
		if !u.m.Current().Settings {
			u.m.Current().Input = s
			u.m.Current().Target = ""
		}
	} else {
		u.fields[u.focus] = s
	}
}
func (u *ui) insert(s string) {
	if u.focus == "" {
		return
	}
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	r := []rune(u.value())
	if u.selected {
		r = nil
		u.cursor = 0
		u.selected = false
	}
	u.cursor = min(u.cursor, len(r))
	add := []rune(s)
	if len(r)+len(add) > 8192 {
		return
	}
	r = append(r[:u.cursor], append(add, r[u.cursor:]...)...)
	u.cursor += len(add)
	u.setValue(string(r))
}
func (u *ui) event(e C.WolfEvent) {
	if u.remoteEvent(e) {
		return
	}
	switch int(e._type) {
	case 1:
		u.quit = true
	case 2:
		for i := len(u.controls) - 1; i >= 0; i-- {
			c := u.controls[i]
			if int(e.x) >= c.x && int(e.x) < c.x+c.w && int(e.y) >= c.y && int(e.y) < c.y+c.h {
				if c.field != "" {
					u.setFocus(c.field)
				} else {
					u.action(c.action)
				}
				return
			}
		}
		u.menu = ""
	case 3:
		if e.text != nil {
			u.insert(C.GoString(e.text))
		}
	case 6:
		u.scroll = max(0, min(u.maxScroll, u.scroll-int(e.dy)*44))
	case 4:
		k := int(e.key)
		ctrl := e.ctrl != 0
		if ctrl {
			switch k {
			case 'q':
				u.quit = true
				return
			case 't':
				u.action("new")
				return
			case 'w':
				u.action("close-active")
				return
			case ',':
				u.action("settings")
				return
			case 'l':
				u.setFocus("url")
				u.selected = true
				return
			case 'r':
				u.action("reload")
				return
			case 'a':
				u.selected = true
				return
			case 'v':
				v := C.wolf_clipboard()
				if v != nil {
					u.insert(C.GoString(v))
					C.SDL_free(unsafe.Pointer(v))
				}
				return
			}
		}
		switch k {
		case int(C.SDLK_ESCAPE):
			u.menu = ""
			u.help = ""
			u.action("cancel")
		case int(C.SDLK_RETURN), int(C.SDLK_KP_ENTER):
			if u.m.Current().Settings {
				u.action("apply")
			} else {
				u.action("go")
			}
		case int(C.SDLK_TAB):
			names := []string{"url"}
			if u.m.Current().Settings {
				if u.manual {
					names = []string{"host", "port", "pin", "code"}
				} else {
					names = []string{"invite"}
				}
			}
			idx := 0
			for i, n := range names {
				if n == u.focus {
					idx = i
				}
			}
			delta := 1
			if e.shift != 0 {
				delta = -1
			}
			u.setFocus(names[(idx+delta+len(names))%len(names)])
		case int(C.SDLK_LEFT):
			u.cursor = max(0, u.cursor-1)
			u.selected = false
		case int(C.SDLK_RIGHT):
			u.cursor = min(len([]rune(u.value())), u.cursor+1)
			u.selected = false
		case int(C.SDLK_HOME):
			u.cursor = 0
			u.selected = false
		case int(C.SDLK_END):
			u.cursor = len([]rune(u.value()))
			u.selected = false
		case int(C.SDLK_PAGEDOWN):
			u.scroll = min(u.maxScroll, u.scroll+180)
		case int(C.SDLK_PAGEUP):
			u.scroll = max(0, u.scroll-180)
		case int(C.SDLK_BACKSPACE), int(C.SDLK_DELETE):
			r := []rune(u.value())
			u.cursor = min(u.cursor, len(r))
			if u.selected {
				u.setValue("")
				u.cursor = 0
				u.selected = false
			} else if k == int(C.SDLK_BACKSPACE) && u.cursor > 0 {
				r = append(r[:u.cursor-1], r[u.cursor:]...)
				u.cursor--
				u.setValue(string(r))
			} else if k == int(C.SDLK_DELETE) && u.cursor < len(r) {
				u.setValue(string(append(r[:u.cursor], r[u.cursor+1:]...)))
			}
		}
	}
}
func (u *ui) action(a string) {
	if u.m.Client.Log != nil {
		u.m.Client.Log.Log(audit.Entry{Component: "frontend", Event: "ui_action", Status: a})
	}
	if strings.HasPrefix(a, "menu:") {
		name := strings.TrimPrefix(a, "menu:")
		if u.menu == name {
			u.menu = ""
		} else {
			u.menu = name
		}
		return
	}
	u.menu = ""
	if strings.HasPrefix(a, "tab:") {
		i, _ := strconv.Atoi(strings.TrimPrefix(a, "tab:"))
		u.blurRemote()
		u.m.SelectTab(i)
		u.scroll = 0
		u.setFocus("url")
		return
	}
	if strings.HasPrefix(a, "close:") {
		i, _ := strconv.Atoi(strings.TrimPrefix(a, "close:"))
		u.m.CloseTab(i)
		u.setFocus("url")
		return
	}
	if strings.HasPrefix(a, "help:") {
		u.help = strings.TrimPrefix(a, "help:")
		u.focus = ""
		return
	}
	var err error
	switch a {
	case "new":
		u.m.NewTab()
		u.setFocus("url")
	case "close-active":
		u.m.CloseTab(u.m.Active)
		u.setFocus("url")
	case "settings":
		u.m.Settings()
		u.scroll = 0
		if u.manual {
			u.setFocus("host")
		} else {
			u.setFocus("invite")
		}
	case "quit":
		u.quit = true
	case "back":
		u.m.History(-1)
	case "forward":
		u.m.History(1)
	case "cancel":
		u.m.Cancel()
	case "go", "reload":
		t := u.m.Current()
		resolved, e := Resolve(t.Input, u.m.Client.Config.SearchTemplate)
		if e != nil {
			err = e
		} else if !strings.HasPrefix(strings.ToLower(strings.TrimSpace(t.Input)), "http://") && !strings.HasPrefix(strings.ToLower(strings.TrimSpace(t.Input)), "https://") && t.Target != resolved {
			t.Target = resolved
			u.m.Notice = "Search resolved above. Press Go again to send this URL to the node."
		} else {
			err = u.m.Navigate()
		}
	case "apply":
		err = u.applyForm()
		if err == nil {
			u.loadForm()
		}
	case "discover", "ping", "renew":
		if u.m.busy {
			break
		}
		err = u.applyForm()
		if err == nil {
			u.m.Operation(a, "")
		}
	case "enrol", "connect":
		if u.m.busy {
			u.m.Notice = "Connection setup is already running."
			break
		}
		code := strings.TrimSpace(u.fields["code"])
		if strings.HasPrefix(strings.ToLower(code), "wolftunnel://") {
			u.fields["invite"] = code
			a = "connect"
		}
		if a == "connect" && strings.TrimSpace(u.fields["invite"]) != "" {
			raw := strings.TrimSpace(u.fields["invite"])
			if len(raw) == 64 && !strings.Contains(raw, ":") {
				u.manual = true
				u.fields["code"] = raw
				code = raw
				err = u.applyForm()
				if err == nil && u.m.Client.Config.Fingerprint == "" {
					err = &nodeclient.Error{Code: "NODE_IDENTITY_UNVERIFIED"}
				}
			} else {
				code, err = u.m.ImportInvitation(raw)
				if err == nil {
					u.loadForm()
				}
			}
		} else {
			err = u.applyForm()
		}
		if err == nil {
			u.enrolling = true
			u.m.Operation("connect", code)
		}
	case "manual":
		u.manual = !u.manual
		u.scroll = 0
		if u.manual {
			u.setFocus("host")
		} else {
			u.setFocus("invite")
		}
	case "request-details":
		u.resultDetails = !u.resultDetails
	case "disconnect":
		u.m.Disconnect()
	case "forget":
		err = u.m.Forget()
		u.fields["code"] = ""
		u.fields["invite"] = ""
		u.manual = false
		u.loadForm()
	case "search":
		c := u.m.Client.Config
		if c.SearchTemplate == nodeclient.Google {
			c.SearchTemplate = nodeclient.DuckDuckGo
		} else {
			c.SearchTemplate = nodeclient.Google
		}
		err = u.m.Configure(c)
	case "details":
		u.m.Client.Config.Details = !u.m.Client.Config.Details
		err = u.m.Client.Save(u.m.Root)
	case "help-close":
		u.help = ""
	}
	if err != nil {
		u.m.Client.Report("ui_action_failed", err)
		code := nodeclient.ErrorCode(err)
		if code == "NODE_UNREACHABLE" {
			u.m.Notice = err.Error()
		} else {
			u.m.Notice = ConnectionMessage(code)
		}
	}
}

func (u *ui) applyForm() error {
	c := u.m.Client.Config
	c.Host = strings.TrimSpace(u.fields["host"])
	c.Port, _ = strconv.Atoi(u.fields["port"])
	host, port, err := nodeclient.NormalizeEndpoint(c.Host, c.Port)
	if err != nil {
		return err
	}
	c.Host, c.Port = host, port
	c.Fingerprint = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(u.fields["pin"]), ":", ""))
	n, _ := strconv.Atoi(u.fields["timeout"])
	c.Timeout = time.Duration(n) * time.Second
	if c == u.m.Client.Config {
		return nil
	}
	return u.m.Configure(c)
}

// Keep platform diagnostics free of input, credentials and response payloads.
func Diagnostic(err error) { fmt.Fprintln(os.Stderr, "Wolf Client:", err) }
