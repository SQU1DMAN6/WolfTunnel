package production

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestBuiltNodeBrowserDiagnosis(t *testing.T) {
	bin := os.Getenv("WOLF_NODE_BIN")
	if bin == "" {
		t.Skip("set WOLF_NODE_BIN to validate the built diagnostic command")
	}
	for _, tc := range []struct{ name, code, stderr string }{
		{"real_sandbox", "", ""},
		{"missing_tool", "BROWSER_TOOL_MISSING", ""},
		{"namespace_denied", "BROWSER_NAMESPACE_DENIED", "bwrap: Creating new namespace failed: Permission denied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Second)
			defer cancel()
			root := filepath.Join(t.TempDir(), "node")
			command := exec.CommandContext(ctx, bin, "-state-dir", root, "-diagnose-browser")
			if tc.code != "" {
				tools := t.TempDir()
				if tc.stderr != "" {
					for name, body := range map[string]string{"bwrap": "exit 0", "systemctl": "exit 1", "systemd-run": "printf '%s\\n' '" + tc.stderr + "' >&2\nprintf '%s\\n' 'secret-token https://private.example/' >&2\nexit 1"} {
						if e := os.WriteFile(filepath.Join(tools, name), []byte("#!/bin/sh\n"+body+"\n"), 0700); e != nil {
							t.Fatal(e)
						}
					}
				}
				command.Env = append(os.Environ(), "PATH="+tools)
			}
			output, err := command.CombinedOutput()
			if tc.code == "" {
				if err != nil || !strings.Contains(string(output), `"stage":"blank_pixels","status":"passed"`) {
					t.Fatalf("real diagnostic failed: %v\n%s", err, output)
				}
			} else if err == nil || !strings.Contains(string(output), tc.code) {
				t.Fatalf("missing diagnosis: %v\n%s", err, output)
			}
			raw, err := os.ReadFile(filepath.Join(root, "logs", "node.log"))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "secret-token") || strings.Contains(string(raw), "private.example") {
				t.Fatal("raw native output logged")
			}
			if strings.Contains(string(output), "secret-token") || strings.Contains(string(output), "private.example") {
				t.Fatal("raw native output printed")
			}
			if artifacts := os.Getenv("WOLF_ARTIFACT_DIR"); artifacts != "" {
				if e := os.MkdirAll(artifacts, 0700); e != nil {
					t.Fatal(e)
				}
				for suffix, data := range map[string][]byte{".log": output, "-node.jsonl": raw} {
					if e := os.WriteFile(filepath.Join(artifacts, "doctor-"+tc.name+suffix), data, 0600); e != nil {
						t.Fatal(e)
					}
				}
			}
			found := false
			for _, line := range strings.Split(string(raw), "\n") {
				if line == "" {
					continue
				}
				var record struct{ Event, Stage, Code, Remediation string }
				if e := json.Unmarshal([]byte(line), &record); e != nil {
					t.Fatal(e)
				}
				if tc.code != "" && record.Event == "browser_start_failed" && record.Code == tc.code && record.Stage != "" && record.Remediation != "" {
					found = true
				}
				if tc.code == "" && record.Stage == "blank_pixels" && record.Event == "browser_start_passed" {
					found = true
				}
			}
			if !found {
				t.Fatal("current node log missing result", string(raw))
			}
		})
	}
}
