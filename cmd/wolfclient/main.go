package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"image/png"
	"os"
	"strings"
	"time"

	"wolftunnel/frontend"
	"wolftunnel/internal/enrolment"
	"wolftunnel/internal/nodeclient"
	v3 "wolftunnel/internal/protocol/v3"
	"wolftunnel/internal/storage"
)

func main() {
	if err := run(); err != nil {
		frontend.Diagnostic(err)
		os.Exit(1)
	}
}
func run() error {
	root, err := storage.DefaultDir(".wolftunnel-client")
	if err != nil {
		return err
	}
	state := flag.String("state-dir", root, "private client state directory")
	smoke := flag.Bool("smoke-test", false, "run native input/tab smoke checks and exit")
	smokeView := flag.String("smoke-view", "request", "final smoke-test view: request, settings or manual")
	browserSmoke := flag.String("browser-smoke", "", "controlled browser fixture URL for timed pixel/input smoke")
	smokeTimeout := flag.Duration("smoke-timeout", 25*time.Second, "native/browser smoke timeout")
	metadataOnly := flag.Bool("metadata", false, "legacy v2 metadata-only native UI")
	captureURL := flag.String("capture", "", "capture remote browser URL to a private PNG and exit")
	captureBlank := flag.Bool("capture-blank", false, "capture a new blank remote browser without web navigation")
	outputFile := flag.String("output", "", "private PNG path for remote capture")
	screenshot := flag.String("screenshot", "", "save a native smoke-test BMP")
	windowSize := flag.String("window-size", "", "optional window width x height, e.g. 1100x700")
	uiScale := flag.Float64("ui-scale", 0, "UI scale: 0 for display DPI, or 0.75–3")
	host := flag.String("host", "", "node host for command-line setup")
	port := flag.Int("port", 0, "node port for command-line setup")
	pin := flag.String("pin", "", "out-of-band verified node certificate SHA-256")
	discover := flag.Bool("discover", false, "print UNVERIFIED public fingerprint and exit")
	codeFile := flag.String("enrol-code-file", "", "enrol with a private code file; never a command-line secret")
	inviteFile := flag.String("invite-file", "", "import a private owner-issued invitation file and connect")
	ping := flag.Bool("test-connection", false, "test authenticated connectivity and exit")
	target := flag.String("fetch", "", "fetch metadata for an HTTP URL and exit")
	renew := flag.Bool("renew", false, "renew the current credential and exit")
	flag.Parse()
	cli := *discover || *codeFile != "" || *inviteFile != "" || *ping || *target != "" || *renew || *captureURL != "" || *captureBlank
	if !cli {
		var width, height int
		if *windowSize != "" {
			if n, err := fmt.Sscanf(*windowSize, "%dx%d", &width, &height); err != nil || n != 2 || width < 520 || height < 420 || width > 8192 || height > 8192 {
				return fmt.Errorf("window size must be 520x420 to 8192x8192")
			}
		}
		return frontend.Run(frontend.Options{StateDir: *state, Smoke: *smoke, Screenshot: *screenshot, Width: width, Height: height, UIScale: *uiScale, SmokeView: *smokeView, MetadataOnly: *metadataOnly, BrowserSmoke: *browserSmoke, SmokeTimeout: *smokeTimeout})
	}
	c, err := nodeclient.Load(*state)
	if err != nil {
		return err
	}
	if *host != "" {
		c.Config.Host = *host
	}
	if *port != 0 {
		c.Config.Port = *port
	}
	if *pin != "" {
		p := strings.ToLower(strings.ReplaceAll(*pin, ":", ""))
		if c.Config.Fingerprint != "" && c.Config.Fingerprint != p {
			return fmt.Errorf("NODE_IDENTITY_CHANGED: use a separate state directory for an explicitly verified replacement node")
		}
		c.Config.Fingerprint = p
	}
	if err = c.Config.Validate(); err != nil {
		return err
	}
	ctx := context.Background()
	if *inviteFile != "" {
		info, err := os.Lstat(*inviteFile)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 2048 {
			return fmt.Errorf("invitation must be a private regular file, at most 2048 bytes")
		}
		data, err := os.ReadFile(*inviteFile)
		if err != nil {
			return err
		}
		invitation, err := enrolment.Parse(string(data))
		if err != nil {
			return err
		}
		if c.Config.Fingerprint != "" && c.Config.Fingerprint != invitation.Fingerprint {
			return &nodeclient.Error{Code: "NODE_IDENTITY_CHANGED"}
		}
		c.Config.Host = invitation.Host
		c.Config.Port = invitation.Port
		c.Config.Fingerprint = invitation.Fingerprint
		if err = c.Enrol(ctx, invitation.Code); err != nil {
			return err
		}
		if err = c.Save(*state); err != nil {
			return err
		}
		rtt, err := c.Ping(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("Connected; invitation accepted; identity verified; RTT %d ms\n", rtt.Milliseconds())
	}
	if *discover {
		fp, err := nodeclient.Discover(ctx, c.Config)
		if err != nil {
			return err
		}
		fmt.Println("UNVERIFIED certificate SHA-256:", fp)
		return nil
	}
	if *codeFile != "" {
		info, err := os.Lstat(*codeFile)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 256 {
			return fmt.Errorf("enrolment file must be private (0600), regular, at most 256 bytes")
		}
		code, err := os.ReadFile(*codeFile)
		if err != nil {
			return err
		}
		if err = c.Enrol(ctx, strings.TrimSpace(string(code))); err != nil {
			return err
		}
		if err = c.Save(*state); err != nil {
			return err
		}
		fmt.Println("Enrolled client:", c.Config.ClientID)
	}
	if *renew {
		if err = c.Renew(ctx); err != nil {
			return err
		}
		if err = c.Save(*state); err != nil {
			return err
		}
		fmt.Println("Credential renewed")
	}
	if *ping {
		rtt, err := c.Ping(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("Connected; identity verified; RTT %d ms\n", rtt.Milliseconds())
	}
	if *target != "" {
		out, err := c.Fetch(ctx, nodeclient.ID(), *target)
		if err != nil {
			return err
		}
		return json.NewEncoder(os.Stdout).Encode(out)
	}
	if *captureURL != "" || *captureBlank {
		if *outputFile == "" {
			return fmt.Errorf("-output is required for capture")
		}
		bounded, cancel := context.WithTimeout(ctx, *smokeTimeout)
		defer cancel()
		stream, err := c.OpenBrowser(bounded, nodeclient.BrowserOptions{Width: 1920, Height: 1080})
		if err != nil {
			return err
		}
		defer stream.Close()
		tab := v3.NewID().String()
		if *captureBlank {
			err = stream.Select(tab)
		} else {
			err = stream.Navigate(tab, *captureURL)
		}
		if err != nil {
			return err
		}
		var latest *nodeclient.BrowserFrame
		ready := *captureBlank
		var completed time.Time
		for {
			select {
			case <-bounded.Done():
				return fmt.Errorf("BROWSER_CAPTURE_TIMEOUT")
			case <-stream.Done():
				return fmt.Errorf("BROWSER_CAPTURE_DISCONNECTED")
			case e := <-stream.Events:
				if e.Type == v3.Error {
					return fmt.Errorf("%s", e.Control.Code)
				}
				if e.Type == v3.NavigationStatus && e.Control.Code != "" {
					return fmt.Errorf("%s", e.Control.Code)
				}
				if e.Type == v3.NavigationStatus && e.Control.State == "Completed" {
					ready = true
					completed = time.Now()
				}
			case frame := <-stream.Frames:
				copy := frame
				latest = &copy
			}
			if latest != nil && ready && (*captureBlank || time.Since(completed) > time.Second) {
				file, err := os.OpenFile(*outputFile, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
				if err != nil {
					return err
				}
				err = png.Encode(file, latest.Image)
				closeErr := file.Close()
				if err != nil {
					return err
				}
				if closeErr != nil {
					return closeErr
				}
				fmt.Printf("Remote pixels captured: %dx%d; encoded_bytes=%d decode_us=%d\n", latest.Update.Width, latest.Update.Height, len(latest.Update.Data), latest.DecodeUS)
				return nil
			}
		}
	}
	return nil
}
