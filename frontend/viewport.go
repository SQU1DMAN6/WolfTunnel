package frontend

/*
#include "bridge.h"
*/
import "C"

import (
	"image"
	"image/draw"
	"runtime"
	"time"
	"unsafe"

	v3 "wolftunnel/internal/protocol/v3"
)

func (u *ui) viewportDraw() {
	t := u.m.Current()
	w, h := u.width, u.height
	area := viewRect{X: 0, Y: 140, W: float64(w), H: float64(max(0, h-228))}
	rect(0, 140, w, max(0, h-228), 0x101010)
	desiredW, desiredH := max(320, min(1920, w)), max(200, min(1200, h-228))
	if desiredW != u.desiredW || desiredH != u.desiredH {
		u.desiredW = desiredW
		u.desiredH = desiredH
		u.resizeAt = time.Now().Add(100 * time.Millisecond)
	}
	if !u.resizeAt.IsZero() && time.Now().After(u.resizeAt) {
		u.m.SetViewport(desiredW, desiredH)
		u.resizeAt = time.Time{}
	}
	if u.m.Remote == nil && !u.m.opening {
		u.m.ViewportWidth, u.m.ViewportHeight = desiredW, desiredH
	}
	if t.Frame == nil {
		label := "Remote browser"
		detail := "Enter a URL to open an isolated browser on the node."
		if t.State == "Loading" {
			detail = "Starting the remote browser and waiting for pixels…"
		}
		if t.Result.Code != "" {
			detail = ConnectionMessage(t.Result.Code)
		}
		headline(32, 174, w-64, label, ink)
		u.wrapped(32, 218, w-64, detail, muted, 3)
		text(32, 300, w-64, "Web content executes on the node. This viewport displays pixels only.", muted)
		if u.m.Connection != "Connected" {
			u.button(32, 260, 170, 36, "Connect node", "settings")
		}
		u.view = viewRect{}
		return
	}
	size := t.Frame.Bounds().Size()
	ratio := min(area.W/float64(size.X), area.H/float64(size.Y))
	area.RemoteW = size.X
	area.RemoteH = size.Y
	area.W = float64(size.X) * ratio
	area.H = float64(size.Y) * ratio
	area.X = (float64(w) - area.W) / 2
	area.Y = 140 + (float64(max(0, h-228))-area.H)/2
	u.view = area
	if u.textureTab != t.ID || u.textureSequence != t.FrameSequence {
		rgba := image.NewRGBA(image.Rect(0, 0, size.X, size.Y))
		draw.Draw(rgba, rgba.Bounds(), t.Frame, t.Frame.Bounds().Min, draw.Src)
		if len(rgba.Pix) > 0 {
			if C.wolf_pixels(unsafe.Pointer(&rgba.Pix[0]), C.int(size.X), C.int(size.Y), C.int(rgba.Stride)) != 0 {
				u.textureTab = t.ID
				u.textureSequence = t.FrameSequence
			}
		}
		runtime.KeepAlive(rgba)
	}
	setClip(0, 140, w, max(0, h-228))
	C.wolf_viewport(C.float(area.X), C.float(area.Y), C.float(area.W), C.float(area.H))
	setClip(0, 0, 0, 0)
}
func (u *ui) blurRemote() {
	if u.remoteFocus {
		_ = u.m.SendInput(v3.Input{Kind: "blur"})
	}
	u.remoteFocus = false
	u.heldButtons = 0
}
func (u *ui) remoteEvent(e C.WolfEvent) bool {
	if u.m.MetadataOnly || u.m.Current().Settings || u.m.Remote == nil {
		return false
	}
	typ := int(e._type)
	mods := 0
	if int(e.mod)&int(C.SDL_KMOD_ALT) != 0 {
		mods |= 1
	}
	if int(e.mod)&int(C.SDL_KMOD_CTRL) != 0 {
		mods |= 2
	}
	if int(e.mod)&int(C.SDL_KMOD_GUI) != 0 {
		mods |= 4
	}
	if int(e.mod)&int(C.SDL_KMOD_SHIFT) != 0 {
		mods |= 8
	}
	if typ == 10 {
		u.blurRemote()
		return true
	}
	x, y, inside := u.view.CSS(float64(e.x), float64(e.y))
	switch typ {
	case 2:
		if !inside {
			u.blurRemote()
			return false
		}
		u.remoteFocus = true
		u.focus = ""
		if int(e.button) < 1 || int(e.button) > 3 {
			return true
		}
		u.heldButtons |= 1 << (int(e.button) - 1)
		_ = u.m.SendInput(v3.Input{Kind: "down", X: x, Y: y, Button: int(e.button), Modifiers: mods})
		return true
	case 8:
		if int(e.button) < 1 || int(e.button) > 3 {
			return true
		}
		if !inside && u.heldButtons == 0 {
			return false
		}
		u.heldButtons &^= 1 << (int(e.button) - 1)
		_ = u.m.SendInput(v3.Input{Kind: "up", X: x, Y: y, Button: int(e.button), Modifiers: mods})
		return true
	case 7:
		if inside || u.heldButtons != 0 {
			_ = u.m.SendInput(v3.Input{Kind: "move", X: x, Y: y, Modifiers: mods})
			return true
		}
	case 6:
		if inside {
			_ = u.m.SendInput(v3.Input{Kind: "wheel", X: x, Y: y, DeltaX: float64(e.dx) * -80, DeltaY: float64(e.dy) * -80, Modifiers: mods})
			return true
		}
	case 3:
		if u.remoteFocus && e.text != nil {
			_ = u.m.SendInput(v3.Input{Kind: "text", Text: C.GoString(e.text)})
			return true
		}
	case 4, 9:
		if !u.remoteFocus {
			return false
		}
		k := int(e.key)
		if typ == 4 && e.ctrl != 0 && (k == 'l' || k == 't' || k == 'w' || k == 'q' || k == ',' || k == 'r') {
			u.blurRemote()
			return false
		}
		scan := ""
		if e.scan != nil {
			scan = C.GoString(e.scan)
		}
		code, key := physicalKey(scan, k)
		if code == "" {
			return true
		}
		kind := "key_down"
		if typ == 9 {
			kind = "key_up"
		}
		_ = u.m.SendInput(v3.Input{Kind: kind, Code: code, Key: key, Modifiers: mods})
		return true
	}
	return false
}
