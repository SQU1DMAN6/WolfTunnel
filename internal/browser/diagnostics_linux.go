//go:build linux

package browser

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"wolftunnel/internal/audit"
	"wolftunnel/internal/browserdiag"
)

// StartupDiagnostic contains only fixed descriptions, validated dependency names,
// generated scope IDs and numeric results. It never contains raw process output.
type StartupDiagnostic struct {
	Stage      string `json:"stage"`
	Status     string `json:"status"`
	Code       string `json:"code,omitempty"`
	Summary    string `json:"error,omitempty"`
	Remedy     string `json:"remediation,omitempty"`
	Evidence   string `json:"evidence,omitempty"`
	Scope      string `json:"scope,omitempty"`
	ExitStatus string `json:"exit_status,omitempty"`
	DurationMS int64  `json:"duration_ms"`
}
type startupError struct{ diagnostic StartupDiagnostic }

func (e *startupError) Error() string { return e.diagnostic.Code }
func StartFailure(stage, code, evidence string) error {
	a, ok := browserdiag.Lookup(code)
	if !ok {
		code = "BROWSER_START_FAILED"
		a, _ = browserdiag.Lookup(code)
	}
	return &startupError{StartupDiagnostic{Stage: stage, Status: "failed", Code: code, Summary: a.Summary, Remedy: a.Remedy, Evidence: evidence}}
}
func Diagnosis(err error) StartupDiagnostic {
	var failure *startupError
	if errors.As(err, &failure) {
		return failure.diagnostic
	}
	return StartFailure("unknown", "BROWSER_START_FAILED", "").(*startupError).diagnostic
}
func RecordDiagnostic(logger *audit.Logger, d StartupDiagnostic, client, session string) {
	if logger != nil {
		logger.Log(audit.Entry{Component: "browser", Event: "browser_start_" + d.Status, Stage: d.Stage, Code: d.Code, Error: d.Summary, Remediation: d.Remedy, Evidence: d.Evidence, Scope: d.Scope, ExitStatus: d.ExitStatus, DurationMS: d.DurationMS, ClientID: client, SessionID: session})
	}
	if d.Status == "failed" {
		log.Printf("Browser startup failed: code=%s stage=%s evidence=%s exit=%s scope=%s. %s %s", d.Code, d.Stage, d.Evidence, d.ExitStatus, d.Scope, d.Summary, d.Remedy)
	}
}

var missingLibrary = regexp.MustCompile(`error while loading shared libraries: (lib[A-Za-z0-9_.+-]{1,100}\.so(?:\.[0-9]+)*):`)
var missingABI = regexp.MustCompile(`(?:GLIBC|GLIBCXX)_[0-9]+\.[0-9]+(?:\.[0-9]+)?`)
var engineExit = regexp.MustCompile(`WOLF_WORKER_ENGINE_(SIGNAL|EXIT_CODE)=([0-9]{1,3})(?:\s|$)`)

// Classification is intentionally conservative: namespace denial does not claim
// that AppArmor was the cause, and SIGKILL alone does not prove an OOM kill.
func classifyStartup(stage, output string, timedOut bool, scopeEvidence string, exit error) error {
	lower := strings.ToLower(output)
	code, evidence := "BROWSER_START_FAILED", "Renderer exited before initialization completed; raw stderr omitted."
	switch {
	case scopeEvidence == "oom-kill":
		code, evidence = "BROWSER_MEMORY_LIMIT", "systemd/cgroup recorded an OOM kill."
	case scopeEvidence == "pids-max":
		code, evidence = "BROWSER_TASK_LIMIT", "cgroup pids.events recorded a task-limit hit."
	case strings.Contains(lower, "resource temporarily unavailable") && (strings.Contains(lower, "pthread_create") || strings.Contains(lower, "fork")):
		code, evidence = "BROWSER_TASK_LIMIT", "Process/thread creation returned resource temporarily unavailable; inspect parent and session task limits."
	case strings.Contains(lower, "cannot allocate memory") || strings.Contains(lower, "out of memory"):
		code, evidence = "BROWSER_MEMORY_LIMIT", "The launcher or renderer reported memory allocation failure; inspect session and parent limits."
	case strings.Contains(lower, "unknown option") || strings.Contains(lower, "unrecognized option"):
		code, evidence = "BROWSER_TOOL_INCOMPATIBLE", "The launcher rejected a required command-line option."
	case missingLibrary.MatchString(output):
		code, evidence = "BROWSER_DEPENDENCY_MISSING", "Missing shared library: "+missingLibrary.FindStringSubmatch(output)[1]
	case strings.Contains(lower, "version") && strings.Contains(lower, "not found") && missingABI.MatchString(output):
		code, evidence = "BROWSER_RUNTIME_INCOMPATIBLE", "Unavailable ABI: "+missingABI.FindString(output)
	case strings.Contains(lower, "failed to connect to bus:") || strings.Contains(lower, "failed to connect to user scope bus") || strings.Contains(lower, "$dbus_session_bus_address and $xdg_runtime_dir not defined"):
		code, evidence = "BROWSER_USER_SESSION_UNAVAILABLE", "systemd-run could not connect to the user bus."
	case strings.Contains(lower, "failed to start transient scope") || strings.Contains(lower, "failed to set unit properties") || strings.Contains(lower, "failed to create cgroup"):
		code, evidence = "BROWSER_CGROUP_UNAVAILABLE", "systemd rejected the transient scope or its resource properties."
	case strings.Contains(lower, "bwrap:") && (strings.Contains(lower, "capset failed") || strings.Contains(lower, "setting up loopback") || strings.Contains(lower, "failed rtm_newlink")) && (strings.Contains(lower, "permission denied") || strings.Contains(lower, "operation not permitted")):
		code, evidence = "BROWSER_NAMESPACE_DENIED", "bubblewrap was denied namespace-local capability or loopback setup. If the kernel journal names unprivileged_userns with setpcap/net_admin, install the WolfTunnel AppArmor launcher policy."
	case strings.Contains(lower, "creating new namespace failed") || strings.Contains(lower, "failed to move to new namespace") || strings.Contains(lower, "failed to unshare") || strings.Contains(lower, "no permissions to create") || strings.Contains(lower, "no usable sandbox") || strings.Contains(lower, "userns_create"):
		code, evidence = "BROWSER_NAMESPACE_DENIED", "Namespace/sandbox creation was denied or unavailable; AppArmor/kernel/container policy must be checked on the host."
	case strings.Contains(lower, "bwrap:") && (strings.Contains(lower, "mount") || strings.Contains(lower, "bind") || strings.Contains(lower, "mkdir")):
		code, evidence = "BROWSER_SANDBOX_FILESYSTEM", "bubblewrap could not prepare a sandbox mount or directory."
	case strings.Contains(lower, "no space left on device"):
		code, evidence = "BROWSER_STORAGE_FAILED", "The launcher or renderer reported no space left on its storage."
	case strings.Contains(output, "WOLF_WORKER_ENGINE_EXEC_DENIED"):
		code, evidence = "BROWSER_BINARY_INVALID", "Kernel denied renderer execution; check executable permissions and noexec mounts."
	case strings.Contains(output, "WOLF_WORKER_ENGINE_EXEC_FORMAT"):
		code, evidence = "BROWSER_ARCH_MISMATCH", "Kernel rejected the executable format."
	case strings.Contains(output, "WOLF_WORKER_ENGINE_NOT_FOUND"):
		code, evidence = "BROWSER_BINARY_INVALID", "Renderer or its ELF interpreter is missing inside the sandbox."
	case strings.Contains(output, "WOLF_WORKER_CORE_LIMIT_FAILED") || strings.Contains(output, "WOLF_WORKER_PROXY_LISTEN_FAILED"):
		code, evidence = "BROWSER_WORKER_FAILED", "Isolated helper failed before launching the engine."
		if strings.Contains(output, "WOLF_WORKER_CORE_LIMIT_FAILED") {
			evidence = "Helper could not disable core dumps."
		} else {
			evidence = "Helper could not bind private loopback proxy port 18080."
		}
	case timedOut:
		code, evidence = "BROWSER_START_TIMEOUT", "Browser initialization deadline expired."
	case strings.HasPrefix(stage, "cdp_") && stage != "cdp_version":
		code, evidence = "BROWSER_CDP_FAILED", "Private CDP initialization failed at the recorded stage."
	}
	failure := StartFailure(stage, code, evidence).(*startupError)
	var command *commandError
	if errors.As(exit, &command) {
		failure = StartFailure(stage, "BROWSER_CDP_FAILED", "").(*startupError)
		failure.diagnostic.Evidence = fmt.Sprintf("Private CDP method failed with numeric code %d.", command.code)
	}
	return failure
}

// Capture resource evidence before destroying the transient scope. Reads are
// bounded and never include systemd journal text or arbitrary renderer stderr.
func (b *Browser) startupScopeEvidence() string {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out := &startupBuffer{enabled: true}
	cmd := exec.CommandContext(ctx, "systemctl", "--user", "show", b.scope, "-p", "Result", "-p", "ControlGroup")
	cmd.Stdout = out
	if cmd.Run() != nil {
		return ""
	}
	group := ""
	for _, line := range strings.Split(out.String(), "\n") {
		if line == "Result=oom-kill" {
			return "oom-kill"
		}
		if strings.HasPrefix(line, "ControlGroup=") {
			group = strings.TrimPrefix(line, "ControlGroup=")
		}
	}
	if group == "" || !strings.HasPrefix(group, "/") || filepath.Clean(group) != group || group == "/" {
		return ""
	}
	base := filepath.Join("/sys/fs/cgroup", group)
	for _, name := range []string{"memory.events", "pids.events"} {
		f, e := os.Open(filepath.Join(base, name))
		if e != nil {
			continue
		}
		var data [4096]byte
		n, _ := f.Read(data[:])
		f.Close()
		for _, line := range strings.Split(string(data[:n]), "\n") {
			fields := strings.Fields(line)
			if len(fields) != 2 {
				continue
			}
			count, _ := strconv.ParseUint(fields[1], 10, 64)
			if count > 0 && name == "memory.events" && fields[0] == "oom_kill" {
				return "oom-kill"
			}
			if count > 0 && name == "pids.events" && fields[0] == "max" {
				return "pids-max"
			}
		}
	}
	return ""
}

// Host facts are flags/counters only, never environment values or bus addresses.
func hostEnvironment() StartupDiagnostic {
	evidence := fmt.Sprintf("platform=%s-%s; runtime_dir_set=%t; bus_address_set=%t", runtime.GOOS, runtime.GOARCH, os.Getenv("XDG_RUNTIME_DIR") != "", os.Getenv("DBUS_SESSION_BUS_ADDRESS") != "")
	_, err := os.Stat("/sys/fs/cgroup/cgroup.controllers")
	evidence += fmt.Sprintf("; cgroup_v2=%t", err == nil)
	for _, setting := range []struct{ name, path string }{
		{"max_user_namespaces", "/proc/sys/user/max_user_namespaces"},
		{"unprivileged_userns_clone", "/proc/sys/kernel/unprivileged_userns_clone"},
		{"apparmor_restrict_unprivileged_userns", "/proc/sys/kernel/apparmor_restrict_unprivileged_userns"},
	} {
		value := "unavailable"
		if data, e := os.ReadFile(setting.path); e == nil {
			if n, e := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64); e == nil {
				value = strconv.FormatUint(n, 10)
			}
		}
		evidence += "; " + setting.name + "=" + value
	}
	return StartupDiagnostic{Stage: "host_environment", Status: "checked", Evidence: evidence}
}
