package frontend

/*
#include "bridge.h"
*/
import "C"

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

type presentationSample struct {
	Sequence                                                        uint64 `json:"sequence"`
	ConversionUS, UploadUS, SubmitUS, PresentUS, DecodedToPresentUS int64
}

func (u *ui) present() {
	at := time.Now()
	ok := C.wolf_present()
	elapsed := time.Since(at).Microseconds()
	if u.pendingPresentation.Sequence == 0 || u.m.Remote == nil {
		return
	}
	if ok == 0 {
		u.m.Remote.PresentationFailed("RENDER_PRESENT_FAILED")
		return
	}
	sample := u.pendingPresentation
	sample.PresentUS = elapsed
	if !u.m.Current().RemoteFrame.DecodedAt.IsZero() {
		sample.DecodedToPresentUS = time.Since(u.m.Current().RemoteFrame.DecodedAt).Microseconds()
	}
	u.m.Remote.Presented(sample.Sequence, sample.ConversionUS, sample.UploadUS, sample.SubmitUS, sample.PresentUS)
	if len(u.presentationSamples) < 120 {
		u.presentationSamples = append(u.presentationSamples, sample)
	}
	u.pendingPresentation = presentationSample{}
}
func (u *ui) writePresentationMetrics(path string) error {
	if path == "" {
		return nil
	}
	data, e := json.MarshalIndent(struct {
		Boundary string
		Samples  []presentationSample
	}{"local decoded image to SDL submission/present return; excludes monitor scanout", u.presentationSamples}, "", "  ")
	if e != nil {
		return e
	}
	return os.WriteFile(filepath.Join(filepath.Dir(path), "native-presentation.json"), data, 0600)
}

func (u *ui) forgetPixels() { C.wolf_forget_pixels() }
