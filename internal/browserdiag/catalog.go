// Package browserdiag defines fixed, non-sensitive browser failure explanations.
// Both peers use this catalog; arbitrary process/peer text never becomes UI text.
package browserdiag

type Advice struct{ Summary, Remedy string }

var catalog = map[string]Advice{
	"BROWSER_CONFIG_INVALID":           {"The node browser configuration is invalid.", "Restore valid browser.json limits; total_memory_mb must cover max_sessions times memory_mb."},
	"BROWSER_ROOT_FORBIDDEN":           {"The node tried to run the browser as root.", "Run wolfnode as an ordinary login account using its systemd user service."},
	"BROWSER_TOOL_MISSING":             {"A required node isolation tool is missing from PATH.", "Install bubblewrap and systemd, and ensure bwrap, systemd-run and systemctl are on the node service PATH."},
	"BROWSER_TOOL_INCOMPATIBLE":        {"The isolation launcher does not support a required option.", "Install a maintained bubblewrap/systemd version that supports WolfTunnel's launcher options; do not remove isolation or resource-limit flags."},
	"BROWSER_STORAGE_FAILED":           {"The browser could not allocate its private temporary storage.", "Check free disk/memory and the node's temporary-directory permissions. Adjust temp_mb within the supported budget if its tmpfs is full."},
	"BROWSER_BINARY_INVALID":           {"The renderer executable is missing, unsafe or not executable.", "Check the renderer file permissions and mount execution policy. Restore the pinned managed runtime; preserve node identity/state."},
	"BROWSER_ARCH_MISMATCH":            {"The renderer executable targets a different CPU architecture.", "Use the native managed runtime, or configure an executable built for the node CPU. Rebuild WolfTunnel on the target host."},
	"BROWSER_PIN_MISMATCH":             {"The renderer executable does not match its pinned SHA-256.", "Restore the reviewed runtime bundle or update the explicit browser pin after verifying its source. Do not change the node certificate pin."},
	"BROWSER_USER_SESSION_UNAVAILABLE": {"The node cannot reach its systemd user manager.", "Log out and reconnect by SSH as the node account after installing dbus-user-session and libpam-systemd. Check systemctl --user status; enable lingering for a persistent user service."},
	"BROWSER_CGROUP_UNAVAILABLE":       {"The node could not create its resource-limited systemd scope.", "Check systemctl --user status, cgroup v2 and delegated memory/pids/cpu controllers. Use a host or VM that permits user scopes."},
	"BROWSER_DEPENDENCY_MISSING":       {"A shared library needed by the renderer or isolation launcher is missing.", "Check the missing library named in node.log and install its matching native-architecture runtime package. Installing the other CPU architecture's libraries will not fix this."},
	"BROWSER_RUNTIME_INCOMPATIBLE":     {"The renderer requires an unavailable runtime library ABI.", "Use a supported native distribution/runtime, or a reviewed renderer built for this host. Check the GLIBC/GLIBCXX version in node.log."},
	"BROWSER_NAMESPACE_DENIED":         {"The host denied creation of a browser sandbox namespace.", "Check AppArmor denials, unprivileged user namespaces and container restrictions. Allow bubblewrap and Chromium's nested user/PID/network namespaces in the relevant host profiles; keep sandboxing enabled."},
	"BROWSER_SANDBOX_FILESYSTEM":       {"The browser sandbox could not prepare its private filesystem.", "Check bwrap mount permissions, runtime file access and host/container mount restrictions. The node account must be able to read the renderer and helper."},
	"BROWSER_SANDBOX_REJECTED":         {"Chromium did not confirm both its namespace and seccomp sandboxes.", "Check the host kernel's seccomp/user-namespace support and AppArmor profiles for the managed renderer. Do not use --no-sandbox or run it as root."},
	"BROWSER_MEMORY_LIMIT":             {"The browser hit a memory limit during startup.", "Free host memory, close other sessions, or raise memory_mb and total_memory_mb together within the host budget. Check the parent user/service memory limit too."},
	"BROWSER_TASK_LIMIT":               {"The browser could not create enough processes or threads.", "Check tasks in browser.json and TasksMax on the parent user/service scope. Raise the supported task budget or reduce concurrent sessions."},
	"BROWSER_START_TIMEOUT":            {"The sandboxed browser did not finish startup before the deadline.", "Check node.log for the last startup stage and resource pressure. On a slow host, increase startup in browser.json (nanoseconds, maximum 60000000000) and update the client too."},
	"BROWSER_EGRESS_FAILED":            {"The node could not create the browser's private egress proxy.", "Check the node account's temporary-directory permissions, disk space and file-descriptor limit. The private Unix socket must be usable inside the sandbox."},
	"BROWSER_WORKER_FAILED":            {"The isolated browser helper could not initialize.", "Check the worker stage in node.log, private loopback support, core-limit policy and access to the node/helper executable."},
	"BROWSER_IPC_FAILED":               {"The node could not create or initialize the browser control pipes.", "Check file-descriptor/process limits and the private helper's execution permissions; restart the node after correcting the host limit."},
	"BROWSER_CDP_FAILED":               {"The renderer's private browser-control initialization failed.", "Check the failed CDP stage and numeric result in node.log. Use WolfTunnel's reviewed renderer version and check whether the renderer exited or was killed."},
	"BROWSER_START_FAILED":             {"The sandboxed renderer exited during startup.", "Run wolfnode -diagnose-browser as the node account and inspect the current node.log startup stage and exit status. Check the matching user-service and kernel journal entries."},
}

func Lookup(code string) (Advice, bool) { a, ok := catalog[code]; return a, ok }
func Message(code string) string {
	a, ok := Lookup(code)
	if !ok {
		return ""
	}
	return a.Summary + " Node owner: " + a.Remedy
}
