package frontend

/*
#include "bridge.h"
*/
import "C"

import (
	"errors"
	"os"
	"unsafe"
)

// Explicit snapshots are private artifacts, never automatic operational logs.
func (u *ui) captureDiagnostic(path string) error {
	if path == "" {
		return nil
	}
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
	return os.Chmod(path, 0600)
}
