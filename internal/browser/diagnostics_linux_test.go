//go:build linux

package browser

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"wolftunnel/internal/browserdiag"
	"wolftunnel/internal/browsernet"
)

func TestStartupFailureClassificationAndRedaction(t *testing.T) {
	cases := []struct {
		out, resource string
		timeout       bool
		stage, code   string
	}{
		{"bwrap: Creating new namespace failed: Permission denied", "", false, "cdp_version", "BROWSER_NAMESPACE_DENIED"},
		{"Failed to move to new namespace: PID namespaces supported, Network namespace supported, but failed: errno = Operation not permitted", "", false, "cdp_version", "BROWSER_NAMESPACE_DENIED"},
		{"Failed to connect to user scope bus via local transport: $DBUS_SESSION_BUS_ADDRESS and $XDG_RUNTIME_DIR not defined", "", false, "cdp_version", "BROWSER_USER_SESSION_UNAVAILABLE"},
		{"Failed to connect to bus: No medium found", "", false, "cdp_version", "BROWSER_USER_SESSION_UNAVAILABLE"},
		{"Failed to start transient scope unit: Access denied", "", false, "cdp_version", "BROWSER_CGROUP_UNAVAILABLE"},
		{"/browser/chrome: error while loading shared libraries: libnss3.so: cannot open shared object file", "", false, "cdp_version", "BROWSER_DEPENDENCY_MISSING"},
		{"version `GLIBC_2.38' not found", "", false, "cdp_version", "BROWSER_RUNTIME_INCOMPATIBLE"},
		{"bwrap: Unknown option --size", "", false, "cdp_version", "BROWSER_TOOL_INCOMPATIBLE"},
		{"bwrap: Failed to bind mount: permission denied", "", false, "cdp_version", "BROWSER_SANDBOX_FILESYSTEM"},
		{"WOLF_WORKER_CORE_LIMIT_FAILED", "", false, "cdp_version", "BROWSER_WORKER_FAILED"},
		{"WOLF_WORKER_PROXY_LISTEN_FAILED", "", false, "cdp_version", "BROWSER_WORKER_FAILED"},
		{"WOLF_WORKER_ENGINE_EXEC_DENIED", "", false, "cdp_version", "BROWSER_BINARY_INVALID"},
		{"WOLF_WORKER_ENGINE_EXEC_FORMAT", "", false, "cdp_version", "BROWSER_ARCH_MISMATCH"},
		{"WOLF_WORKER_ENGINE_NOT_FOUND", "", false, "cdp_version", "BROWSER_BINARY_INVALID"},
		{"pthread_create: Resource temporarily unavailable", "", false, "cdp_version", "BROWSER_TASK_LIMIT"},
		{"", "oom-kill", false, "cdp_version", "BROWSER_MEMORY_LIMIT"},
		{"", "pids-max", false, "cdp_version", "BROWSER_TASK_LIMIT"},
		{"malloc: Cannot allocate memory", "", false, "cdp_version", "BROWSER_MEMORY_LIMIT"},
		{"No space left on device", "", false, "cdp_version", "BROWSER_STORAGE_FAILED"},
		{"", "", true, "sandbox_verification", "BROWSER_START_TIMEOUT"},
		{"", "", false, "cdp_permissions", "BROWSER_CDP_FAILED"},
		{"process killed", "", false, "cdp_version", "BROWSER_START_FAILED"},
		// Missing system D-Bus is a common Chrome warning on a healthy headless host.
		{"[ERROR:dbus/bus.cc] Failed to connect to the bus: /run/dbus/system_bus_socket", "", false, "cdp_version", "BROWSER_START_FAILED"},
	}
	for _, tc := range cases {
		t.Run(tc.code+"/"+tc.out, func(t *testing.T) {
			e := classifyStartup(tc.stage, tc.out+"\nhttps://private.example/path?token=secret-code", tc.timeout, tc.resource, errors.New("secret-code"))
			if e.Error() != tc.code {
				t.Fatalf("got %s want %s", e, tc.code)
			}
			d := Diagnosis(e)
			if d.Stage != tc.stage || d.Remedy == "" || d.Summary == "" || strings.Contains(d.Evidence, "secret") || strings.Contains(d.Evidence, "private.example") {
				t.Fatal("unsafe or incomplete diagnostic", d)
			}
		})
	}
	d := Diagnosis(classifyStartup("cdp_permissions", "secret", false, "", &commandError{method: "Browser.setPermission", code: -32601}))
	if d.Code != "BROWSER_CDP_FAILED" || !strings.Contains(d.Evidence, "-32601") {
		t.Fatal(d)
	}
}

func TestStartupObserverReportsPreflightFailure(t *testing.T) {
	c := DefaultConfig()
	c.Binary = "/missing/chrome"
	c.SHA256 = strings.Repeat("0", 64)
	if os.Geteuid() == 0 {
		t.Skip("ordinary account test")
	}
	tools := t.TempDir()
	for _, name := range []string{"bwrap", "systemd-run", "systemctl"} {
		if e := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\nexit 0\n"), 0700); e != nil {
			t.Fatal(e)
		}
	}
	t.Setenv("PATH", tools)
	var events []StartupDiagnostic
	_, err := StartObserved(context.Background(), c, nil, func(d StartupDiagnostic) { events = append(events, d) })
	if err == nil || err.Error() != "BROWSER_BINARY_INVALID" {
		t.Fatal(err)
	}
	last := events[len(events)-1]
	if last.Stage != "renderer_integrity" || last.Status != "failed" || last.Remedy == "" {
		t.Fatal(events)
	}
	if _, ok := browserdiag.Lookup(last.Code); !ok {
		t.Fatal("unknown safe code")
	}
}

func TestStartupDiagnosticsAreBounded(t *testing.T) {
	b := &startupBuffer{enabled: true}
	b.Write([]byte(strings.Repeat("secret ", 10000)))
	if len(b.String()) != 16384 {
		t.Fatal("unbounded native output")
	}
	b.disable()
	b.Write([]byte("more"))
	if b.String() != "" {
		t.Fatal("startup output retained after page access")
	}
}

func TestSandboxVerificationWaitsForReportAndDistinguishesRejection(t *testing.T) {
	for _, tc := range []struct {
		name, report, code string
		delay              time.Duration
	}{
		{"slow_ready", "Seccomp-BPF sandbox Yes\nLayer 1 Sandbox Namespace", "", 1100 * time.Millisecond},
		{"rejected", "Seccomp-BPF sandbox No\nNamespace Sandbox Yes", "BROWSER_SANDBOX_REJECTED", 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client, server := net.Pipe()
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			adapter := newCDP(client, client)
			defer adapter.close()
			started := time.Now()
			done := make(chan struct{})
			go func() {
				defer close(done)
				defer server.Close()
				reader := bufio.NewReader(server)
				for {
					raw, e := reader.ReadBytes(0)
					if e != nil {
						return
					}
					var request packet
					if json.Unmarshal(raw[:len(raw)-1], &request) != nil {
						return
					}
					result := any(map[string]string{})
					switch request.Method {
					case "Target.createTarget":
						result = map[string]string{"targetId": "sandbox"}
					case "Target.attachToTarget":
						result = map[string]string{"sessionId": "sandbox-session"}
					case "Runtime.evaluate":
						text := ""
						if time.Since(started) >= tc.delay {
							text = tc.report
						}
						result = map[string]any{"result": map[string]string{"value": text}}
					}
					encoded, _ := json.Marshal(result)
					response, _ := json.Marshal(packet{ID: request.ID, Result: encoded})
					if _, e = server.Write(append(response, 0)); e != nil {
						return
					}
				}
			}()
			b := &Browser{cdp: adapter}
			err := b.verifySandbox(ctx)
			if tc.code == "" && err != nil {
				t.Fatal("slow sandbox report rejected", err)
			}
			if tc.code != "" && (err == nil || err.Error() != tc.code) {
				t.Fatal("sandbox failure lost", err)
			}
			if tc.code != "" && !strings.Contains(Diagnosis(err).Evidence, "seccomp-BPF sandbox confirmed=false") {
				t.Fatal("missing sandbox evidence")
			}
			adapter.close()
			<-done
		})
	}
}

func TestRealRendererTaskLimitProducesActionableDiagnosis(t *testing.T) {
	cfg := integrationConfig(t)
	cfg.Tasks = 32
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	proxy, err := browsernet.New(ctx, browsernet.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close()
	renderer, err := StartObserved(ctx, cfg, proxy, func(d StartupDiagnostic) {
		if d.Status == "failed" {
			t.Logf("%+v", d)
		}
	})
	if renderer != nil {
		renderer.Close()
		t.Fatal("task-limited renderer unexpectedly started")
	}
	if err == nil || err.Error() != "BROWSER_TASK_LIMIT" {
		t.Fatal("real task limit not identified", err)
	}
}

func TestBrowserCheckHonorsConfiguredViewportLimits(t *testing.T) {
	cfg := integrationConfig(t)
	cfg.MaxWidth = 320
	cfg.MaxHeight = 200
	cfg.MaxPixels = 64000
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	evidence := ""
	if err := Check(ctx, cfg, func(d StartupDiagnostic) {
		if d.Stage == "blank_pixels" && d.Status == "passed" {
			evidence = d.Evidence
		}
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(evidence, "320x200") {
		t.Fatal("diagnostic exceeded configured viewport", evidence)
	}
}
