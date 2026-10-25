//go:build linux

package audit

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestNativeDiagnosticsNeverPersistRawInput(t *testing.T) {
	code, detail := nativeDiagnostic("Failed to load plugin 'libdecor-gtk.so': failed to init")
	if code != "NATIVE_DECORATION_WARNING" || detail == "" {
		t.Fatal("native decoration diagnostic lost")
	}
	_, detail = nativeDiagnostic("secret-code https://example.test/private?token=secret")
	if strings.Contains(detail, "secret") || strings.Contains(detail, "example.test") {
		t.Fatal("native stderr copied sensitive contents")
	}
}

func TestNativeStderrCapturePipeline(t *testing.T) {
	if os.Getenv("WOLF_NATIVE_LOG_HELPER") == "1" {
		logger, e := NewClient(os.Getenv("WOLF_NATIVE_LOG_DIR"), time.Hour)
		if e != nil {
			t.Fatal(e)
		}
		stop, e := CaptureNativeStderr(logger)
		if e != nil {
			t.Fatal(e)
		}
		fmt.Fprintln(os.Stderr, "Failed to load plugin libdecor-gtk.so: failed to init secret-code")
		fmt.Fprintln(os.Stderr, "https://private.example/path?credential=secret")
		stop()
		return
	}
	dir := filepath.Join(t.TempDir(), "logs")
	command := exec.Command(os.Args[0], "-test.run=^TestNativeStderrCapturePipeline$")
	command.Env = append(os.Environ(), "WOLF_NATIVE_LOG_HELPER=1", "WOLF_NATIVE_LOG_DIR="+dir)
	output, e := command.CombinedOutput()
	if e != nil {
		t.Fatalf("native capture subprocess failed: %s", output)
	}
	if !strings.Contains(string(output), "secret-code") {
		t.Fatal("terminal diagnostics were not preserved")
	}
	records, e := os.ReadFile(filepath.Join(dir, "client.log"))
	if e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(string(records), "NATIVE_DECORATION_WARNING") || !strings.Contains(string(records), "NATIVE_RUNTIME_DIAGNOSTIC") || strings.Contains(string(records), "secret") || strings.Contains(string(records), "private.example") {
		t.Fatal("native capture was incomplete or leaked raw text", string(records))
	}
}
