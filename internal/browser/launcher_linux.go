//go:build linux

package browser

import (
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const privateLauncher = "/usr/local/libexec/wolftunnel/bwrap"

// The dedicated pathname lets AppArmor grant namespace setup only to this
// root-installed entrypoint, without replacing the policy for /usr/bin/bwrap.
func selectLauncher() (string, error) {
	return launcherAt(privateLauncher)
}
func launcherAt(candidate string) (string, error) {
	if _, err := os.Lstat(candidate); os.IsNotExist(err) {
		return exec.LookPath("bwrap")
	} else if err != nil {
		return "", StartFailure("launcher_policy", "BROWSER_LAUNCHER_UNSAFE", "Cannot inspect the dedicated sandbox launcher.")
	}
	if err := trustedLauncher(candidate); err != nil {
		return "", err
	}
	if _, err := exec.LookPath(candidate); err != nil {
		return "", StartFailure("launcher_policy", "BROWSER_LAUNCHER_UNSAFE", "The node account cannot execute the dedicated launcher; check its installed group.")
	}
	return candidate, nil
}
func trustedLauncher(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return StartFailure("launcher_policy", "BROWSER_LAUNCHER_UNSAFE", "Sandbox launcher path must be absolute and canonical.")
	}
	for current := path; ; current = filepath.Dir(current) {
		info, err := os.Lstat(current)
		if err != nil || !trustedLauncherInfo(info, current == path) {
			return StartFailure("launcher_policy", "BROWSER_LAUNCHER_UNSAFE", "Dedicated launcher and parent directories must be root-owned, without symlinks or group/other write permission; no setuid/setgid launcher is allowed.")
		}
		if current == "/" {
			break
		}
	}
	return nil
}
func trustedLauncherInfo(info os.FileInfo, executable bool) bool {
	if info == nil {
		return false
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != 0 || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0022 != 0 {
		return false
	}
	if executable {
		return info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0 && info.Mode()&(os.ModeSetuid|os.ModeSetgid) == 0
	}
	return info.IsDir()
}

func profileCategory(raw string) string {
	label := strings.TrimSpace(raw)
	// Profiles may be stacked; disclose only known policy names, not arbitrary
	// administrator labels or namespace names from /proc.
	for _, name := range []string{"unprivileged_userns", "wolftunnel-bwrap", "bwrap"} {
		if label == name || strings.HasPrefix(label, name+" (") || strings.HasPrefix(label, name+"//&") || strings.Contains(label, "//&"+name+"//&") || strings.Contains(label, "//&"+name+" (") || strings.HasSuffix(label, "//&"+name) {
			return name
		}
	}
	if label == "unconfined" {
		return "unconfined"
	}
	return "other-or-unavailable"
}

// Hold bwrap in argument parsing so /proc exposes its actual exec-time profile
// before it creates a namespace. --version then exits without making a sandbox.
// This needs no journal access or privileged AppArmor interface.
func probeLauncherProfile(parent context.Context, launcher string) (string, error) {
	ctx, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	input, output, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer input.Close()
	defer output.Close()
	cmd := exec.CommandContext(ctx, launcher, "--args", "3")
	cmd.ExtraFiles = []*os.File{input}
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	if err = cmd.Start(); err != nil {
		return "", err
	}
	input.Close()
	data, _ := os.ReadFile(filepath.Join("/proc", strconv.Itoa(cmd.Process.Pid), "attr", "current"))
	_, _ = io.WriteString(output, "--version\x00")
	output.Close()
	err = cmd.Wait()
	if err != nil {
		return "other-or-unavailable", err
	}
	return profileCategory(string(data)), nil
}

func launcherPolicy(parent context.Context, launcher string) (StartupDiagnostic, error) {
	kind := "system"
	if launcher == privateLauncher {
		kind = "dedicated"
	}
	d := StartupDiagnostic{Stage: "launcher_policy", Status: "checked", Evidence: "launcher=" + kind}
	enabled, err := os.ReadFile("/sys/module/apparmor/parameters/enabled")
	if err != nil || strings.TrimSpace(string(enabled)) != "Y" {
		d.Evidence += "; apparmor=disabled-or-unavailable"
		return d, nil
	}
	label, probeErr := probeLauncherProfile(parent, launcher)
	if probeErr != nil {
		label = "other-or-unavailable"
	}
	d.Evidence += "; apparmor_profile=" + label
	restricted, _ := os.ReadFile("/proc/sys/kernel/apparmor_restrict_unprivileged_userns")
	if strings.TrimSpace(string(restricted)) == "1" {
		d.Evidence += "; restricted_userns=1"
	}
	return d, launcherPolicyFailure(label, strings.TrimSpace(string(restricted)) == "1")
}

func launcherPolicyFailure(label string, restricted bool) error {
	if label == "unprivileged_userns" || (label == "unconfined" && restricted) {
		return StartFailure("launcher_policy", "BROWSER_APPARMOR_SETUP_REQUIRED", "Launcher lacks an AppArmor userns allowance and would enter the capability-stripping unprivileged_userns profile. Install/load the WolfTunnel launcher policy and start from a normal login session.")
	}
	return nil
}
