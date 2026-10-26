package session_test

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wolftunnel/internal/browser"
	"wolftunnel/internal/node"
	"wolftunnel/internal/nodeclient"
)

// Exercise authenticated production startup and both logs. Only the external
// launcher is failed deterministically; no sandbox is weakened or skipped.
func TestBrowserStartupDiagnosisReachesClientAndCurrentNodeLog(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("ordinary node account required")
	}
	for _, tc := range []struct{ name, stderr, systemctl, code string }{
		{"namespace", "bwrap: Creating new namespace failed: Permission denied", "exit 1", "BROWSER_NAMESPACE_DENIED"},
		{"user-session", "Failed to connect to bus: No medium found", "exit 1", "BROWSER_USER_SESSION_UNAVAILABLE"},
		{"missing-library", "/browser/chrome: error while loading shared libraries: libnss3.so: cannot open shared object file", "exit 1", "BROWSER_DEPENDENCY_MISSING"},
		{"memory", "renderer exited", "printf 'Result=oom-kill\\n'", "BROWSER_MEMORY_LIMIT"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tools := t.TempDir()
			scripts := map[string]string{"bwrap": "exit 0", "systemctl": tc.systemctl, "systemd-run": "printf '%s\\n' '" + tc.stderr + "' >&2\nprintf '%s\\n' 'https://private.example/path?token=secret-code' >&2\nexit 1"}
			for name, script := range scripts {
				if e := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\n"+script+"\n"), 0700); e != nil {
					t.Fatal(e)
				}
			}
			t.Setenv("PATH", tools)
			root := filepath.Join(t.TempDir(), "node")
			s, e := node.Open(root)
			if e != nil {
				t.Fatal(e)
			}
			bin, e := os.Executable()
			if e != nil {
				t.Fatal(e)
			}
			cfg := browser.DefaultConfig()
			cfg.Binary = bin
			cfg.SHA256 = browserHash(t, bin)
			if e = s.EnableBrowser(cfg); e != nil {
				t.Fatal(e)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			listener, e := net.Listen("tcp", "127.0.0.1:0")
			if e != nil {
				t.Fatal(e)
			}
			done := make(chan error, 1)
			go func() { done <- s.Serve(ctx, listener) }()
			defer func() {
				cancel()
				if e := <-done; e != nil {
					t.Error(e)
				}
			}()
			clientRoot := filepath.Join(t.TempDir(), "client")
			c, e := nodeclient.Load(clientRoot)
			if e != nil {
				t.Fatal(e)
			}
			c.Config.Port = listener.Addr().(*net.TCPAddr).Port
			c.Config.Fingerprint = s.Fingerprint
			code, e := s.Auth.IssueCode()
			if e != nil {
				t.Fatal(e)
			}
			if e = c.Enrol(ctx, code); e != nil {
				t.Fatal(e)
			}
			if _, e = c.OpenBrowser(ctx, nodeclient.BrowserOptions{}); nodeclient.ErrorCode(e) != tc.code {
				t.Fatalf("got %v want %s", e, tc.code)
			}
			if !strings.Contains(nodeclient.FailureDetail(e), "Node owner:") {
				t.Fatal("client lacks mitigation")
			}
			for _, path := range []string{filepath.Join(root, "logs", "node.log"), filepath.Join(clientRoot, "logs", "client.log")} {
				raw, e := os.ReadFile(path)
				if e != nil {
					t.Fatal(e)
				}
				text := string(raw)
				if !strings.Contains(text, tc.code) || strings.Contains(text, "secret-code") || strings.Contains(text, "private.example") || strings.Contains(text, code) {
					t.Fatal("diagnosis missing or secret logged", path, text)
				}
				if filepath.Base(path) == "node.log" && (!strings.Contains(text, "cdp_version") || !strings.Contains(text, "remediation") || !strings.Contains(text, "exit_status")) {
					t.Fatal("node diagnostic incomplete", text)
				}
			}
		})
	}
}
