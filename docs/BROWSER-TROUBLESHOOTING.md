# Browser startup troubleshooting

A successful connection or enrolment proves that node TLS and authentication
work. Browser processes start separately when a browsing session opens. Installing
runtime packages does not establish a working systemd user session, namespace
permission or sufficient process/memory limits.

## Get the actual failure

Update both node and client to this revision. On the node, rebuild with `make node`
and restart its running process/service so it uses the new executable. On the
client machine, rebuild the desktop client with `make build` and relaunch it.
Older clients still receive the compatible generic failure; the updated node
always records the specific diagnosis locally.

Run this **as the node account**, using its normal state location and service
login environment:

```sh
./build/wolfnode -diagnose-browser
```

If using an installed executable, use `wolfnode -diagnose-browser`. Reuse any
custom `-state-dir` or `XDG_DATA_HOME` used by the running node. This command does
not bind port 13300, require a client, enrol another identity or visit a website.
It starts the production sandbox, verifies Chromium's namespace/seccomp sandbox
and captures a private blank JPEG (up to 800×600 within node limits). It exits with status 0 on success and
nonzero on failure. A missing managed runtime may be provisioned as on normal
startup. The temporary renderer is cleaned up automatically.

The command prints safe JSON checkpoints. A failed record includes:

```json
{
  "stage": "cdp_version",
  "status": "failed",
  "code": "BROWSER_NAMESPACE_DENIED",
  "error": "The host denied creation of a browser sandbox namespace.",
  "remediation": "Check AppArmor denials, unprivileged user namespaces and container restrictions. Allow bubblewrap and Chromium's nested user/PID/network namespaces in the relevant host profiles; keep sandboxing enabled.",
  "evidence": "Namespace/sandbox creation was denied or unavailable; AppArmor/kernel/container policy must be checked on the host.",
  "exit_status": "exit code 1"
}
```

`stage` is the last initialization checkpoint; `code` and `evidence` identify the
observed failure. For example, waiting for `cdp_version` can fail because
bubblewrap rejected a namespace before Chromium could answer. The example above
is illustrative and does not establish the cause of a particular remote failure.

## Read the current log

Node startup and the diagnostic command print the exact active state/log paths.
The usual current log is:

```sh
tail -n 80 "$HOME/.local/share/wolftunnel/node/logs/node.log"
```

Legacy or custom state locations have their own `logs/node.log`. This stable file
receives events from every hour; older `wolfnode-YYYY-MM-DDTHH.jsonl` files are
hourly archives. Reading just yesterday's file misses current failures. Timestamps
are UTC. A log warning on the terminal means the node could not write diagnostics;
check free disk space and ownership/mode of its state directory.

Logs include the stage, fixed failure code, safe explanation, mitigation, launcher
exit/engine signal, scope ID and elapsed time. Host facts record architecture,
user-bus/runtime-directory presence, cgroup v2 presence and available namespace
sysctl values. A flag indicating AppArmor namespace restrictions is context, not
proof that AppArmor caused this particular failure.

The client log is normally
`~/.local/share/wolftunnel/logs/client.log`. New clients display and record the
specific category and mitigation. No arbitrary stderr, invitation, URL, typed
input, page content or environment value is copied into these diagnostics.

## Fix the reported category

| Code | What was observed | Mitigation |
|---|---|---|
| `BROWSER_USER_SESSION_UNAVAILABLE` | systemd-run could not contact the user bus | After installing `dbus-user-session`/`libpam-systemd`, log out of SSH and log back in as the node account. Run `systemctl --user status` without sudo. Use the user service and lingering for operation after logout. An already-open SSH shell may retain the old environment. |
| `BROWSER_NAMESPACE_DENIED` | Namespace/sandbox creation was denied or unavailable | Inspect kernel/AppArmor denials and the container's namespace policy. The host must allow bubblewrap and Chromium's nested user, PID and network namespaces. Adjust the relevant application profiles with the host administrator. |
| `BROWSER_CGROUP_UNAVAILABLE` | systemd rejected scope creation or resource properties | Verify the user manager, cgroup v2 and delegated memory/pids/cpu controllers. A restricted container may need host-side delegation or a suitable VM. Check systemd's unit error in the journal. |
| `BROWSER_DEPENDENCY_MISSING` | The dynamic loader named a missing shared library | Read the library basename in `evidence`; install its native ARM64 or amd64 runtime package. For example, `libnss3.so` comes from `libnss3`, `libgbm.so.1` from `libgbm1`, and `libasound.so.2` from `libasound2t64` on Ubuntu 24.04+. |
| `BROWSER_RUNTIME_INCOMPATIBLE` | A required GLIBC/GLIBCXX symbol version was unavailable | Use a renderer and runtime built for the distribution's ABI. Keep the distribution maintained; avoid copying individual libc files from another OS. |
| `BROWSER_TASK_LIMIT` | Thread/fork creation failed or a cgroup task-limit event occurred | Check `tasks` in browser.json and `TasksMax` on the node's service and parent user slice. The default renderer budget is 384 tasks; the supported maximum is 512. Reduce concurrent sessions or raise supported host/session limits. |
| `BROWSER_MEMORY_LIMIT` | cgroup/systemd recorded an OOM kill, or allocation failed | Check both per-session and parent service/host memory. Free memory or adjust `memory_mb` and `total_memory_mb` together. Total memory must cover `max_sessions × memory_mb`. A signal alone is never treated as proof of OOM. |
| `BROWSER_SANDBOX_REJECTED` | Chromium's own sandbox report did not confirm both required sandboxes | Read the namespace/seccomp boolean evidence. Check host kernel support and the renderer's AppArmor profile. Use the reviewed engine; keep both sandboxes enabled. |
| `BROWSER_START_TIMEOUT` | A startup stage exceeded its deadline | Check the stage and resource pressure first. A slow host can raise `startup` in browser.json up to `60000000000` nanoseconds (60 seconds). The default is 20 seconds. Update the client too, so it waits for the node's result. |
| `BROWSER_BINARY_INVALID` | File/format/permissions or renderer execution failed | Check readability, executable bits and `noexec` mounts. The renderer and helper must be accessible within the launcher. Restore the reviewed managed bundle if needed. |
| `BROWSER_ARCH_MISMATCH` | ELF CPU/class or executable format did not match | Use the ARM64 engine on ARM64, amd64 on amd64. The managed runtime selects this automatically. Check manual browser configuration or copied deployment bundles. |
| `BROWSER_PIN_MISMATCH` | Engine bytes did not match the configured executable pin | Restore the approved runtime or deliberately verify/update a custom executable pin. This is separate from the node certificate pin; preserve node identity/state. |
| `BROWSER_TOOL_MISSING` | bwrap/systemd-run/systemctl was unavailable | Install the isolation tools and expose them through the service's PATH. The node log names the missing tool. |
| `BROWSER_TOOL_INCOMPATIBLE` | A required launcher option was rejected | Use a maintained bubblewrap/systemd release supporting the launcher options. Do not remove namespace or limit options to make it start. |
| `BROWSER_SANDBOX_FILESYSTEM` | bubblewrap could not bind/mount/create its private filesystem | Check mount permissions, access to the renderer/helper, and host/container restrictions. |
| `BROWSER_STORAGE_FAILED` | Private temporary storage reported no space | Check host disk/memory and `temp_mb` (supported range 64–512 MiB), plus temporary-directory permissions. |
| `BROWSER_EGRESS_FAILED` | Private proxy directory/socket creation failed | Check temporary storage, permissions and file-descriptor limits. Do not expose a public browser debug/proxy port. |
| `BROWSER_WORKER_FAILED` | The isolated helper could not disable core dumps or bind private loopback | Read the fixed worker marker explanation. Check host core-limit policy, private loopback availability and helper execution. |
| `BROWSER_IPC_FAILED` | The node could not create pipes or execute the launcher | Check file-descriptor/process limits and systemd-run execution permissions. |
| `BROWSER_CDP_FAILED` | Private Chromium-control initialization failed | Read the CDP stage and numeric error. Check the reviewed engine version and exit status. No page-supplied CDP command/text is exposed. |
| `BROWSER_CONFIG_INVALID` | Limits or browser configuration were invalid | Restore valid settings using SETUP.md; keep consistent memory reservations and bounded limits. |
| `BROWSER_ROOT_FORBIDDEN` | The node account was root | Start the node as a normal user, using its own systemd user service. |
| `BROWSER_START_FAILED` | A startup exit did not match a more specific supported diagnosis | Preserve the failed JSON record, scope, engine exit/signal and timestamp. Check the service/kernel journal and report these safe details; the cause is not yet established. |

For a user-service deployment, these commands help compare parent resource limits
and find related logs:

```sh
systemctl --user status wolfnode.service --no-pager
systemctl --user show wolfnode.service -p TasksMax -p MemoryMax
journalctl --user -u wolfnode.service --since '-10 min' --no-pager
sudo journalctl -k --since '-10 min' --no-pager
```

The last command is for host-level evidence such as AppArmor `DENIED` events or
OOM records and may require administrator access. If running in the foreground,
there may be no `wolfnode.service`; use the failed diagnostic's `scope` to locate
its systemd records. Running a diagnostic from SSH tests that shell's environment;
compare it with the service if their results differ.

[Ubuntu documents application-specific user-namespace permission requirements](https://documentation.ubuntu.com/security/security-features/privilege-restriction/apparmor/#apparmor-unprivileged-user-namespace-restrictions).
A package install alone cannot grant those permissions. WolfTunnel does not
modify AppArmor/sysctls, run Chromium as root or add `--no-sandbox`. After fixing
the observed host condition, rerun the diagnostic and reconnect the client.
