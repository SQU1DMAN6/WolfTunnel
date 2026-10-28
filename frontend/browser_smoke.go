package frontend

/*
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"time"
	"unsafe"
)

func (u *ui) browserSmoke(o Options, stage *int, clicked *time.Time) (bool, error) {
	if *stage == 0 && u.m.Connection == "Error" {
		return false, errors.New(u.m.ConnectionError)
	}
	if *stage == 0 && u.m.Connection == "Connected" {
		u.m.Current().Input = o.BrowserSmoke
		if e := u.m.Navigate(); e != nil {
			return false, e
		}
		*stage = 1
	}
	if *stage > 0 && u.m.Remote == nil && !u.m.opening {
		return false, fmt.Errorf("STREAM_CLOSED: %s", u.m.RemoteFailureCode)
	}
	t := u.m.Current()
	if t.Result.Code != "" {
		return false, errors.New(t.Result.Code)
	}
	if t.Frame == nil {
		return false, nil
	}
	size := t.Frame.Bounds().Size()
	r, g, b, _ := t.Frame.At(size.X-20, size.Y-20).RGBA()
	if *stage == 1 && b > r*2 && b > g && u.view.RemoteW > 0 && u.view.W > 0 {
		x, y := u.view.Local(50, 50)
		C.wolf_mouse(1, 1, C.float(x), C.float(y))
		C.wolf_mouse(0, 1, C.float(x), C.float(y))
		*clicked = time.Now()
		*stage = 2
		return false, nil
	}
	if *stage == 2 && g > r*2 && g > b {
		// Keep typing separate from physical key events; no clipboard is read.
		x, y := u.view.Local(50, 130)
		C.wolf_mouse(1, 1, C.float(x), C.float(y))
		C.wolf_mouse(0, 1, C.float(x), C.float(y))
		pushText("WolfTunnel ✓ Привет")
		*stage = 3
		return false, nil
	}
	if o.FixtureSmoke && *stage >= 3 {
		return u.fixtureSmoke(o, stage, clicked)
	}
	if *stage == 3 && u.m.Remote.LastTextSequence() > 0 && t.RemoteFrame.Update.InputSequence >= u.m.Remote.LastTextSequence() && time.Since(*clicked) > 300*time.Millisecond {
		C.wolf_key('t', C.SDL_KMOD_CTRL)
		*stage = 4
		return false, nil
	}
	if *stage == 4 && t.Target == "" && t.Frame != nil {
		C.wolf_key('w', C.SDL_KMOD_CTRL)
		*clicked = time.Now()
		*stage = 5
		return false, nil
	}
	if *stage == 5 && t.Target != "" && g > r*2 && g > b && time.Since(*clicked) > 300*time.Millisecond {
		u.draw()
		if o.Screenshot != "" {
			f, e := os.OpenFile(o.Screenshot, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
			if e != nil {
				return false, e
			}
			f.Close()
			path := C.CString(o.Screenshot)
			ok := C.wolf_capture(path)
			C.free(unsafe.Pointer(path))
			if ok == 0 {
				return false, errors.New("SCREENSHOT_FAILED")
			}
			if e = os.Chmod(o.Screenshot, 0600); e != nil {
				return false, e
			}
			nodePath := strings.TrimSuffix(o.Screenshot, ".bmp") + ".node.jpg"
			if e = os.WriteFile(nodePath, t.RemoteFrame.Update.Data, 0600); e != nil {
				return false, e
			}
		}
		if e := u.writePresentationMetrics(o.Screenshot); e != nil {
			return false, e
		}
		metrics := u.m.Remote.Metrics()
		fmt.Printf("Browser smoke passed: rendered pixels, JavaScript click, international text, tab close/resume; frames=%d bytes=%d capture_us=%d decode_us=%d dispatch_us=%d queue_us=%d\n", metrics.Frames, metrics.Bytes, t.RemoteFrame.Update.CaptureUS, metrics.DecodeUS, metrics.DispatchUS, metrics.InputQueueUS)
		return true, nil
	}
	return false, nil
}
