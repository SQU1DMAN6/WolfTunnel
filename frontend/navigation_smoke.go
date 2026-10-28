package frontend

/*
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"path/filepath"
	"strings"
	"time"
)

func (u *ui) navigationSmoke(o Options, stage *int, at *time.Time) (bool, error) {
	if u.m.Connection == "Error" {
		return false, errors.New(u.m.ConnectionError)
	}
	if *stage == 0 && u.m.Connection == "Connected" {
		u.m.Current().Input = o.BrowserSmoke
		if e := u.m.Navigate(); e != nil {
			return false, e
		}
		*stage = 1
	}
	if u.m.RemoteFailureCode != "" {
		return false, errors.New(u.m.RemoteFailureCode)
	}
	switch *stage {
	case 1:
		if u.m.Current().Frame == nil || u.m.Current().State != "Completed" || u.view.RemoteW == 0 {
			return false, nil
		}
		x, y := u.view.Local(70, 35)
		C.wolf_move(C.float(x), C.float(y))
		*stage = 2
	case 2:
		if !strings.Contains(u.m.LinkTarget, "case=normal") {
			return false, nil
		}
		if e := u.captureDiagnostic(filepath.Join(filepath.Dir(o.Screenshot), "link-hover.bmp")); e != nil {
			return false, e
		}
		C.SDL_SetModState(C.SDL_KMOD_CTRL)
		x, y := u.view.Local(70, 35)
		C.wolf_mouse(1, 1, C.float(x), C.float(y))
		C.wolf_mouse(0, 1, C.float(x), C.float(y))
		*stage = 3
		*at = time.Now()
	case 3:
		C.SDL_SetModState(0)
		if len(u.m.Tabs) < 2 || u.m.Current().Frame == nil || !strings.Contains(u.m.Current().Target, "case=normal") {
			return false, nil
		}
		if time.Since(*at) < 300*time.Millisecond {
			return false, nil
		}
		if len(u.m.Tabs) != 2 || !strings.Contains(u.m.Tabs[0].Target, "/links") {
			return false, errors.New("NEW_TAB_ORIGIN_CHANGED")
		}
		if e := u.captureDiagnostic(filepath.Join(filepath.Dir(o.Screenshot), "ctrl-click-new-tab.bmp")); e != nil {
			return false, e
		}
		if e := u.writePresentationMetrics(o.Screenshot); e != nil {
			return false, e
		}
		return true, nil
	}
	return false, nil
}
