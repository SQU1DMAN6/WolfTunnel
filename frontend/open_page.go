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
	"wolftunnel/internal/nodeclient"
)

// -open is a normal native browsing convenience. With an explicit screenshot it
// becomes a bounded snapshot operation; input injection requires a separate flag.
func (u *ui) openPage(o Options, stage *int, at *time.Time) (bool, error) {
	if u.m.Connection == "Error" {
		if e := u.captureDiagnostic(o.Screenshot); e != nil {
			return false, e
		}
		return false, &nodeclient.Error{Code: u.m.ConnectionError}
	}
	if *stage == 0 && u.m.Connection == "Connected" {
		u.m.Current().Input = o.OpenURL
		if e := u.m.Navigate(); e != nil {
			return false, e
		}
		*stage = 1
	}
	if o.Screenshot == "" {
		return false, nil
	}
	if *stage > 0 && u.m.Remote == nil && !u.m.opening {
		return false, &nodeclient.Error{Code: u.m.RemoteFailureCode}
	}
	tab := u.m.Current()
	if tab.Result.Code != "" {
		if e := u.captureDiagnostic(o.Screenshot); e != nil {
			return false, e
		}
		return false, &nodeclient.Error{Code: tab.Result.Code}
	}
	if tab.Frame == nil || u.view.W <= 0 || u.view.RemoteW <= 0 {
		return false, nil
	}
	if *stage == 1 && tab.State == "Completed" {
		*at = time.Now()
		*stage = 2
		return false, nil
	}
	if *stage == 2 && time.Since(*at) > 500*time.Millisecond {
		if o.InteractionSmoke {
			x, y := u.view.Local(100, 100)
			C.wolf_mouse(1, 1, C.float(x), C.float(y))
			C.wolf_mouse(0, 1, C.float(x), C.float(y))
			C.wolf_key_event(C.SDLK_PAGEDOWN, 1)
			C.wolf_key_event(C.SDLK_PAGEDOWN, 0)
		}
		*at = time.Now()
		*stage = 3
		_ = u.m.Remote.Refresh()
		return false, nil
	}
	if *stage == 3 && time.Since(*at) > 600*time.Millisecond {
		metrics := u.m.Remote.Metrics()
		if o.InteractionSmoke && (metrics.InputAcks < 4 || tab.RemoteFrame.Update.InputSequence == 0) {
			return false, nil
		}
		u.draw()
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
		if e = os.WriteFile(nodePath, tab.RemoteFrame.Update.Data, 0600); e != nil {
			return false, e
		}
		fmt.Printf("Native page snapshot: status=%d frames=%d bytes=%d input_acks=%d capture_us=%d decode_us=%d\n", tab.Result.Status, metrics.Frames, metrics.Bytes, metrics.InputAcks, tab.RemoteFrame.Update.CaptureUS, metrics.DecodeUS)
		return true, nil
	}
	return false, nil
}
