//go:build linux

package browser

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

type launcherInfo struct {
	mode os.FileMode
	uid  uint32
}

func (i launcherInfo) Name() string       { return "bwrap" }
func (i launcherInfo) Size() int64        { return 0 }
func (i launcherInfo) Mode() os.FileMode  { return i.mode }
func (i launcherInfo) ModTime() time.Time { return time.Time{} }
func (i launcherInfo) IsDir() bool        { return i.mode.IsDir() }
func (i launcherInfo) Sys() any           { return &syscall.Stat_t{Uid: i.uid} }

func TestDedicatedLauncherTrustChecks(t *testing.T) {
	for _, tc := range []struct {
		name     string
		info     launcherInfo
		leaf, ok bool
	}{
		{"root executable", launcherInfo{0750, 0}, true, true},
		{"user executable", launcherInfo{0750, 1000}, true, false},
		{"group writable", launcherInfo{0770, 0}, true, false},
		{"world writable", launcherInfo{0752, 0}, true, false},
		{"setuid", launcherInfo{os.ModeSetuid | 0750, 0}, true, false},
		{"symlink", launcherInfo{os.ModeSymlink | 0755, 0}, true, false},
		{"not executable", launcherInfo{0644, 0}, true, false},
		{"root directory", launcherInfo{os.ModeDir | 0755, 0}, false, true},
		{"writable parent", launcherInfo{os.ModeDir | 0775, 0}, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := trustedLauncherInfo(tc.info, tc.leaf); got != tc.ok {
				t.Fatalf("got %t want %t", got, tc.ok)
			}
		})
	}
	p := filepath.Join(t.TempDir(), "bwrap")
	if e := os.Symlink("/usr/bin/bwrap", p); e != nil {
		t.Fatal(e)
	}
	if e := trustedLauncher(p); e == nil || e.Error() != "BROWSER_LAUNCHER_UNSAFE" {
		t.Fatal("symlink launcher accepted", e)
	}
	tools := t.TempDir()
	if e := os.WriteFile(filepath.Join(tools, "bwrap"), []byte("#!/bin/sh\nexit 0\n"), 0700); e != nil {
		t.Fatal(e)
	}
	t.Setenv("PATH", tools)
	if path, e := launcherAt(filepath.Join(t.TempDir(), "missing")); e != nil || path != filepath.Join(tools, "bwrap") {
		t.Fatal("system fallback broken", path, e)
	}
	if _, e := launcherAt(p); e == nil {
		t.Fatal("unsafe dedicated launcher silently fell back")
	}
}

func TestLauncherProfileClassification(t *testing.T) {
	for raw, want := range map[string]string{
		"unconfined\n":                                     "unconfined",
		"wolftunnel-bwrap (unconfined)":                    "wolftunnel-bwrap",
		"unprivileged_userns (enforce)":                    "unprivileged_userns",
		"bwrap (enforce)":                                  "bwrap",
		"wolftunnel-bwrap//&unprivileged_userns (enforce)": "unprivileged_userns",
		"unprivileged_userns//&wolftunnel-bwrap (enforce)": "unprivileged_userns",
		"personal-secret-label (enforce)":                  "other-or-unavailable",
	} {
		if got := profileCategory(raw); got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	}
}

func TestLauncherProfileProbeTerminatesWithoutSandbox(t *testing.T) {
	path, e := exec.LookPath("bwrap")
	if e != nil {
		t.Skip("bwrap unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	category, e := probeLauncherProfile(ctx, path)
	if e != nil {
		t.Fatal("bounded --args/--version probe failed", e)
	}
	if category == "" {
		t.Fatal("missing profile category")
	}
}

func TestRestrictedUnconfinedLauncherGetsConcreteSetupDiagnosis(t *testing.T) {
	for _, tc := range []struct {
		label            string
		restricted, fail bool
	}{
		{"unconfined", true, true}, {"unconfined", false, false},
		{"unprivileged_userns", true, true}, {"unprivileged_userns", false, true},
		{"wolftunnel-bwrap", true, false}, {"other-or-unavailable", true, false},
	} {
		err := launcherPolicyFailure(tc.label, tc.restricted)
		if (err != nil) != tc.fail {
			t.Fatalf("%s restricted=%t error=%v", tc.label, tc.restricted, err)
		}
		if err != nil && (err.Error() != "BROWSER_APPARMOR_SETUP_REQUIRED" || !strings.Contains(Diagnosis(err).Remedy, "make install-sandbox-policy")) {
			t.Fatal("setup remedy missing", err)
		}
	}
}
