package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"wolftunnel/internal/audit"
	"wolftunnel/internal/browser"
	"wolftunnel/internal/enrolment"
	"wolftunnel/internal/node"
	"wolftunnel/internal/renderer"
	"wolftunnel/internal/storage"
)

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}
func run() error {
	root, err := storage.StateDir("node")
	if err != nil {
		return err
	}
	state := flag.String("state-dir", root, "private node state directory")
	listen := flag.String("listen", ":13300", "TCP listener")
	issue := flag.Bool("issue-enrolment", false, "create a single-use 30-day code and exit; stdout is secret")
	inviteAddress := flag.String("invite-address", "", "with -issue-enrolment, output one setup invitation for host:port (secret)")
	revoke := flag.String("revoke", "", "revoke a client ID and exit")
	browserConfig := flag.String("browser-config", "", "private browser JSON configuration")
	browserBin := flag.String("browser-bin", "", "pinned Chromium/Chromium-based executable")
	prepare := flag.Bool("prepare-renderer", false, "provision the managed renderer and exit")
	diagnose := flag.Bool("diagnose-browser", false, "check the actual sandboxed browser and blank pixels, print safe JSON checkpoints, and exit")
	rendererDir := flag.String("renderer-dir", "", "managed renderer cache; saved for later node launches")
	rendererArchive := flag.String("renderer-archive", "", "offline copy of the pinned renderer archive")
	rendererPlatform := flag.String("renderer-platform", runtime.GOOS+"-"+runtime.GOARCH, "with -prepare-renderer only, provision a release for linux-amd64 or linux-arm64")
	codeOnly := flag.Bool("code-only", false, "with -issue-enrolment, emit a raw code instead of a complete invitation")
	browserHash := flag.String("browser-sha256", "", "expected browser executable SHA-256")
	flag.Parse()
	if os.Geteuid() == 0 && !*prepare && !*issue && *revoke == "" {
		err := browser.StartFailure("node_account", "BROWSER_ROOT_FORBIDDEN", "Root browser startup is rejected before opening state or provisioning a runtime. Re-run as the original node account.")
		d := browser.Diagnosis(err)
		browser.RecordDiagnostic(nil, d, "", "")
		if *diagnose {
			_ = json.NewEncoder(os.Stdout).Encode(d)
		}
		return err
	}
	if *diagnose && (*prepare || *issue || *revoke != "") {
		return fmt.Errorf("-diagnose-browser cannot be combined with administrative operations")
	}
	if *rendererPlatform != runtime.GOOS+"-"+runtime.GOARCH && !*prepare {
		return fmt.Errorf("-renderer-platform is only valid with -prepare-renderer; normal startup uses the native CPU")
	}
	*state, err = filepath.Abs(*state)
	if err != nil {
		return err
	}

	provided := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { provided[f.Name] = true })
	settings, fresh, err := node.LoadSettings(*state)
	if err != nil {
		return err
	}
	if provided["listen"] {
		settings.Listen = *listen
	} else {
		*listen = settings.Listen
	}
	if *inviteAddress != "" {
		settings.PublicAddress = *inviteAddress
	}
	if *rendererDir != "" {
		settings.RendererDir, err = filepath.Abs(*rendererDir)
		if err != nil {
			return err
		}
	}
	if *browserConfig != "" {
		settings.BrowserConfig, err = filepath.Abs(*browserConfig)
		if err != nil {
			return err
		}
		settings.ManagedRenderer = false
	}
	managedBase := settings.RendererDir
	if managedBase == "" {
		managedBase = filepath.Join(*state, "renderer")
	}
	if *prepare {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cancel()
		installed, e := renderer.EnsureFor(ctx, managedBase, *rendererArchive, *rendererPlatform)
		if e != nil {
			return e
		}
		fmt.Printf("Renderer ready: %s\nExecutable: %s\nSHA-256: %s\n", installed.Version, installed.Binary, installed.SHA256)
		return nil
	}
	if *issue && *revoke != "" {
		return fmt.Errorf("choose one administrative operation")
	}
	s, err := node.Open(*state)
	if err != nil {
		return err
	}
	if *diagnose {
		fmt.Fprintf(os.Stderr, "Browser diagnostics\nState: %s\nCurrent node log: %s\nPlatform: %s-%s\n", *state, filepath.Join(*state, "logs", "node.log"), runtime.GOOS, runtime.GOARCH)
	}
	if *issue {
		if !*codeOnly && *inviteAddress == "" {
			*inviteAddress = settings.InvitationAddress()
		}
		var invitation enrolment.Invitation
		if *inviteAddress != "" {
			host, port, err := net.SplitHostPort(*inviteAddress)
			if err != nil {
				return fmt.Errorf("invite address must be host:port")
			}
			invitation.Host = host
			invitation.Port, err = strconv.Atoi(port)
			invitation.Fingerprint = s.Fingerprint
			invitation.Code = strings.Repeat("0", 64)
			if err != nil || invitation.Validate() != nil {
				return fmt.Errorf("invalid invitation address")
			}
		}
		code, err := s.Auth.IssueCode()
		if err != nil {
			return err
		}
		s.Audit.Log(audit.Entry{Event: "enrolment_code_issued"})
		if err = settings.Save(*state); err != nil {
			return err
		}
		if *inviteAddress != "" && !*codeOnly {
			invitation.Code = code
			fmt.Println(invitation.String())
		} else {
			fmt.Println(code)
		}
		return nil
	}
	if *revoke != "" {
		if err = s.Auth.Revoke(*revoke); err != nil {
			return err
		}
		s.Audit.Log(audit.Entry{Event: "client_revoked", ClientID: *revoke})
		fmt.Println("Client revoked")
		return nil
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	bc := browser.DefaultConfig()
	configPath := settings.BrowserConfig
	if configPath == "" {
		configPath = filepath.Join(*state, "browser.json")
	}
	if err = storage.Read(configPath, &bc); err != nil {
		if !os.IsNotExist(err) || settings.BrowserConfig != "" {
			return err
		}
		err = nil
	}
	if fresh && bc.Binary != "" {
		settings.ManagedRenderer = false
	}
	if *browserBin != "" {
		bc.Binary = *browserBin
		bc.SHA256 = *browserHash
		settings.ManagedRenderer = false
		settings.BrowserConfig = ""
		configPath = filepath.Join(*state, "browser.json")
	}
	if settings.ManagedRenderer {
		s.Audit.Log(audit.Entry{Event: "renderer_preparing", Component: "renderer"})
		fmt.Fprintln(os.Stderr, "Preparing WolfTunnel renderer (first launch may download the pinned runtime)...")
		installing, cancel := context.WithTimeout(ctx, 5*time.Minute)
		var installed renderer.Installation
		var bundled bool
		if settings.RendererDir == "" && *rendererArchive == "" {
			installed, bundled, err = renderer.Bundled(installing)
		}
		if err == nil && !bundled {
			installed, err = renderer.Ensure(installing, managedBase, *rendererArchive)
		}
		cancel()
		if err != nil {
			s.Audit.Log(audit.Entry{Event: "renderer_failed", Component: "renderer", Code: "RENDERER_UNAVAILABLE"})
			return err
		}
		bc.Binary, bc.SHA256 = installed.Binary, installed.SHA256
		s.Audit.Log(audit.Entry{Event: "renderer_ready", Component: "renderer", Status: installed.Version})
	}
	if !bc.Valid() || bc.Binary == "" {
		err := browser.StartFailure("configuration", "BROWSER_CONFIG_INVALID", "")
		browser.RecordDiagnostic(s.Audit, browser.Diagnosis(err), "", "")
		return err
	}
	if configPath == filepath.Join(*state, "browser.json") || settings.ManagedRenderer {
		if err = storage.Write(configPath, bc); err != nil {
			return err
		}
	}
	if err = settings.Save(*state); err != nil {
		return err
	}
	if *diagnose {
		encoder := json.NewEncoder(os.Stdout)
		return browser.Check(ctx, bc, func(d browser.StartupDiagnostic) {
			browser.RecordDiagnostic(s.Audit, d, "", "")
			_ = encoder.Encode(d)
		})
	}
	if err = s.EnableBrowser(bc); err != nil {
		return err
	}
	ln, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	fmt.Printf("WolfTunnel v0.6 node ready on %s\nState: %s\nCurrent node log: %s\nCertificate SHA-256: %s\nEnrolment requires an owner-issued code. Browser sessions use protocol v3.\nReady means TLS listener ready; browser startup is checked when a browser session opens.\n", ln.Addr(), *state, filepath.Join(*state, "logs", "node.log"), s.Fingerprint)
	if err = s.Serve(ctx, ln); err != nil {
		return err
	}
	return nil
}
