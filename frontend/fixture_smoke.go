package frontend

/*
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
	"unsafe"
)

// Runs only with the explicit -fixture-smoke test flag and a controlled page.
// All interactions pass through SDL and the normal production input pipeline.
func (u *ui) fixtureSmoke(o Options, stage *int, at *time.Time) (bool, error) {
	capture := func(name string) error {
		if o.Screenshot == "" {
			return nil
		}
		path := filepath.Join(filepath.Dir(o.Screenshot), name+".bmp")
		f, e := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
		if e != nil {
			return e
		}
		f.Close()
		u.draw()
		p := C.CString(path)
		ok := C.wolf_capture(p)
		C.free(unsafe.Pointer(p))
		if ok == 0 {
			return errors.New("SCREENSHOT_FAILED")
		}
		return os.WriteFile(filepath.Join(filepath.Dir(path), name+".node.jpg"), u.m.Current().RemoteFrame.Update.Data, 0600)
	}
	click := func(x, y float64) {
		lx, ly := u.view.Local(x, y)
		C.wolf_mouse(1, 1, C.float(lx), C.float(ly))
		C.wolf_mouse(0, 1, C.float(lx), C.float(ly))
	}
	move := func(x, y float64) { lx, ly := u.view.Local(x, y); C.wolf_move(C.float(lx), C.float(ly)) }
	switch *stage {
	case 3:
		if u.m.Remote.LastTextSequence() == 0 || u.m.Current().RemoteFrame.Update.InputSequence < u.m.Remote.LastTextSequence() {
			return false, nil
		}
		if e := capture("verification-pending"); e != nil {
			return false, e
		}
		click(330, 110)
		click(350, 161)
		pushText("WOLF")
		click(510, 161)
		x, y := u.view.Local(40, 245)
		C.wolf_mouse(1, 1, C.float(x), C.float(y))
		move(80, 245)
		move(190, 245)
		x, y = u.view.Local(190, 245)
		C.wolf_mouse(0, 1, C.float(x), C.float(y))
		*at = time.Now()
		*stage = 10
	case 10:
		if time.Since(*at) < 500*time.Millisecond {
			return false, nil
		}
		if e := capture("verification-complete-input"); e != nil {
			return false, e
		}
		x, y := u.view.Local(130, 380)
		C.wolf_wheel(C.float(x), C.float(y), -0.5, -1.25)
		*at = time.Now()
		*stage = 11
	case 11:
		if time.Since(*at) < 500*time.Millisecond {
			return false, nil
		}
		if e := capture("fractional-scroll"); e != nil {
			return false, e
		}
		u.blurRemote()
		C.wolf_resize(1024, 850)
		*at = time.Now()
		*stage = 12
	case 12:
		if time.Since(*at) < 600*time.Millisecond || u.m.Remote.Resizing() || u.m.Current().Frame.Bounds().Dy() != 622 {
			return false, nil
		}
		if e := capture("resized-client"); e != nil {
			return false, e
		}
		x, y := u.view.Local(400, 300)
		C.wolf_pinch(C.float(x), C.float(y), 0, 1)
		C.wolf_pinch(C.float(x), C.float(y), 1, 1.25)
		C.wolf_pinch(C.float(x), C.float(y), 2, 1)
		*at = time.Now()
		*stage = 13
	case 13:
		if time.Since(*at) < 500*time.Millisecond {
			return false, nil
		}
		if e := capture("native-pinch"); e != nil {
			return false, e
		}
		fmt.Printf("v0.6 local SDL fixture passed: frames=%d input_acks=%d viewport=%dx%d\n", u.m.Remote.Metrics().Frames, u.m.Remote.Metrics().InputAcks, u.view.RemoteW, u.view.RemoteH)
		return true, nil
	}
	return false, nil
}
