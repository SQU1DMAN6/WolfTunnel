//go:build linux

package browser

import (
	"bytes"
	"context"
	"fmt"
	"image/jpeg"
	"time"

	"wolftunnel/internal/browsernet"
	v3 "wolftunnel/internal/protocol/v3"
)

// Check uses the exact production launcher and sandbox. It opens only about:blank
// in a disposable context; no public URL, enrolment or listening node is required.
func Check(parent context.Context, c Config, observe func(StartupDiagnostic)) error {
	started := time.Now()
	if observe != nil {
		original := observe
		observe = func(d StartupDiagnostic) { d.DurationMS = time.Since(started).Milliseconds(); original(d) }
	}
	ctx, cancel := context.WithTimeout(parent, c.Startup+15*time.Second)
	defer cancel()
	reportFailure := func(stage, code, evidence string) error {
		err := StartFailure(stage, code, evidence)
		if observe != nil {
			observe(Diagnosis(err))
		}
		return err
	}
	if observe != nil {
		observe(StartupDiagnostic{Stage: "egress_proxy", Status: "checking"})
	}
	p, err := browsernet.New(ctx, browsernet.Options{})
	if err != nil {
		return reportFailure("egress_proxy", "BROWSER_EGRESS_FAILED", "Temporary directory or Unix socket creation failed.")
	}
	defer p.Close()
	b, err := StartObserved(ctx, c, p, observe)
	if err != nil {
		return err
	}
	defer b.Close()
	id := v3.NewID().String()
	width, height := min(800, c.MaxWidth), min(600, c.MaxHeight)
	if width*height > c.MaxPixels {
		width = max(320, c.MaxPixels/height)
		height = min(height, c.MaxPixels/width)
	}
	if observe != nil {
		observe(StartupDiagnostic{Stage: "blank_pixels", Status: "checking", Scope: b.scope})
	}
	if err = b.NewPage(ctx, id); err != nil {
		return reportFailure("blank_pixels", "BROWSER_CDP_FAILED", "Could not create a private blank page.")
	}
	if err = b.Select(ctx, id, width, height); err != nil {
		return reportFailure("blank_pixels", "BROWSER_CDP_FAILED", "Could not select/size the private blank page.")
	}
	data, _, _, err := b.Capture(ctx)
	if err != nil {
		return reportFailure("blank_pixels", "BROWSER_CDP_FAILED", "Could not capture the private blank page.")
	}
	image, err := jpeg.DecodeConfig(bytes.NewReader(data))
	if err != nil || image.Width != width || image.Height != height {
		return reportFailure("blank_pixels", "BROWSER_CDP_FAILED", "Blank screenshot was invalid or had unexpected dimensions.")
	}
	if observe != nil {
		observe(StartupDiagnostic{Stage: "blank_pixels", Status: "passed", Scope: b.scope, Evidence: fmt.Sprintf("Sandboxed about:blank rendered a valid %dx%d JPEG.", width, height)})
	}
	return nil
}
