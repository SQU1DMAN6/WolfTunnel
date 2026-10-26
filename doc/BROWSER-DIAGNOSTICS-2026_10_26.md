# Browser startup diagnostics — 26 October 2026

## Outcome and remote limitation

Implemented specific browser startup diagnostics for the reported condition:
node TLS/enrolment works, but Chromium cannot start. The previous launcher
captured bounded stderr and then discarded it when returning
`BROWSER_START_FAILED`. Operational logs were hourly files, so reading an older
file could show only past lifecycle events.

The updated node records current startup checkpoints, an observed failure
category, evidence, mitigation, exit status, scope and duration in a stable
private `logs/node.log`. New clients display/log the specific failure and matching
remedy. The existing hourly audit files remain available.

**The remote ARM64 host's actual failure is not yet established.** Its supplied
logs contain no browser startup evidence. Package installation alone does not
prove a usable systemd user bus, permitted nested namespaces or adequate task/
memory limits. This update provides the command and current log needed to
distinguish those conditions without guessing or disabling protections.

## Operator next step

Update/rebuild and restart the node, update/relaunch the client, then run on the
node **as the normal node account**:

```sh
./build/wolfnode -diagnose-browser
```

Use the installed `wolfnode` name if appropriate, and reuse the service's custom
state/environment. The command prints the active state/log paths and safe JSON
checkpoints. It exercises the real production sandbox and captures a private
blank JPEG (up to 800×600 within node limits) without a client, a website visit or a port 13300 listener.
Its exit status is nonzero when the check fails. Preserve the failed JSON record
and consult [the troubleshooting guide](../docs/BROWSER-TROUBLESHOOTING.md).

The default current node log is
`~/.local/share/wolftunnel/node/logs/node.log`; existing legacy/custom state retains
its own `logs/node.log`. All log timestamps are UTC. `node ready` now explicitly
means the TLS listener is ready; renderer startup is checked when a browser
session opens. New startup output names the current node log.

## Architectural and code changes

- `internal/browserdiag` contains **23 fixed failure categories** and corresponding
  remedies, shared by node, client logs and native UI. Categories distinguish
  configuration/account/tool/binary/hash/architecture failures, user-session and
  cgroup failures, missing libraries/ABI, namespace/filesystem/seccomp failures,
  memory/tasks/storage, egress/helper/pipe failures, CDP initialization, timeout
  and an explicit unknown-startup fallback.
- `browser.StartObserved` records checks of configuration, account, host facts,
  tools, executable integrity/ELF CPU, control pipes, scope launch, private CDP
  version/context/download/permission initialization, sandbox verification and
  target discovery. `browser.Check` reuses that path and verifies real blank
  JPEG pixels. It does not use a separate unsandboxed launch path.
- Host facts contain architecture, environment-variable presence flags, cgroup
  v2 availability and numeric namespace sysctl settings. They never copy bus
  addresses, arbitrary environment values or credentials.
- The isolated worker emits fixed early-failure markers and numeric engine
  exits/signals. Startup stderr remains capped at 16 KiB. Only supported patterns,
  validated library basenames, ABI versions and numeric CDP errors become safe
  diagnostic evidence. Unknown stderr is not copied into logs or client messages.
- Resource evidence is queried before scope cleanup. Confirmed cgroup OOM kills,
  task-limit events and explicit allocation/thread failures get distinct remedies.
  A killed process alone is not declared an OOM kill. Namespace failure does not
  falsely assert AppArmor was its confirmed cause.
- Audit `Entry` gains stage/evidence/remediation/scope/exit fields. Stable
  `node.log` uses mode 0600, existing 0700 directory protection, 1 MiB rotation,
  three backups and retention checks; symlinks/insecure files are rejected.
  Current failures also reach the terminal/user-service journal as safe summaries.
- Specific wire codes are capability-negotiated. Nodes advertise
  `browser-start-diagnostics`; new clients echo it in their signed AUTHENTICATE
  payload only when advertised. Old clients receive the compatible generic error,
  while node logs retain the specific diagnosis. New clients use the original
  schema with old nodes. Arbitrary peer/native error text remains prohibited.
- The client startup wait now covers the supported 60-second node startup budget
  plus authentication/result delivery. User/test parent deadlines still apply.
- A timing bug was fixed: sandbox verification previously allowed only about one
  second for Chromium's internal sandbox page to populate. It now waits within
  the configured startup deadline, accepts a fully confirmed sandbox, rejects an
  explicit negative report, and distinguishes a report timeout. A report delayed
  by 1.1 seconds passes its regression check.
- README, SETUP, architecture and protocol docs explain the current logs,
  capability negotiation and diagnostic command. The self-contained operator
  guide maps every category to a mitigation. USAGE.md remains unchanged/short.

No host AppArmor/sysctl changes, root browser execution, sandbox bypass, system
Chromium installation, authentication reset or certificate-pin change was added.
The managed runtime manifests and dependency versions are unchanged.

## Tests and observed results

Tests ran only on this amd64 development host. The actual managed renderer and
production sandbox were used where stated; no remote Wolf Node was contacted.

| Check | Result | Evidence under `build/diagnostics/` |
|---|---|---|
| `make build`, pure-Go `make node` | Pass | `build-final.log`, `pure-node-build.log` |
| `make test` including `go test ./...` and `go vet ./...` | Pass | `unit-vet-final.log` |
| ARM64 pure-Go node cross-build and target vet | Pass | `wolfnode-arm64`, `arm64-vet-final.log` |
| Classification/redaction cases | Pass: bus, namespace, library/ABI, cgroup, worker, tool, memory/task/storage, timeout, CDP and unknown exits | `diagnostic-tests.log`, `browser-tests.log` |
| Real authenticated TLS failure paths | Pass: specific namespace, user-bus, missing-library and recorded-OOM failures reach client and both current logs; raw sentinel/URL/invitation omitted | `diagnostic-tests.log` |
| Protocol compatibility and advice | Pass: specific errors require negotiation, legacy wire fallback remains valid, raw errors rejected, UI remedies are specific | `browser-tests.log`, `unit-vet-final.log` |
| Sandbox report delay/negative report | Pass: 1.1-second delayed positive report accepted; negative seccomp report rejected with boolean evidence | `browser-tests.log` |
| Real renderer with a temporary 32-task cgroup | Pass: returned `BROWSER_TASK_LIMIT`, engine signal 5, targeted task-budget remedy | `real-task-limit.log` |
| Built diagnostic command | Pass: real sandbox/blank pixels, missing tool and injected namespace denial; correct failure exit and stable log | `doctor-tests.log`, `production.log` |
| `make browser-test` | Pass: sandbox, rendering, keyboard/mouse/Unicode/forms, egress, crashes, TLS pixels, screenshots, slow consumer and control responsiveness | `browser-tests.log` |
| Race checks for browser/session/audit/nodeclient/v3 | Pass | `race.log` |
| Built local node/client public HTTPS browsing | Pass on loopback port 13300: HTTP 200, 4 frames, 87,565 encoded bytes, 4 mouse/keyboard acknowledgements; saved credentials and node restart passed | `production.log`, `public-native.png`, `public-native.node.jpg`, `client.log`, `node-audit.jsonl` |
| Bounded v3 control-schema fuzz campaign | Pass, 78,484 executions over 11 seconds | `fuzz-v3-control.log` |
| Diagnostic minimum viewport budget | Pass, valid 320×200 JPEG within configured limits | `doctor-viewport.log` |
| Formatting, diff whitespace and documentation links | Pass | Final working-tree verification |

The public-browsing test used the existing managed amd64 runtime and SDL's actual
software/dummy frontend. Its screenshot was inspected. These measurements are
local regression evidence, not ARM64 hardware performance measurements.
Fresh-runtime download testing was intentionally not enabled in this run because
this change does not alter provisioning or runtime pins. Optional browser tests
skip in plain `make test`; the explicit real-browser/production invocations above
ran them separately.

Artifacts are private, ignored local files. Temporary state, scopes and test
listeners were cleaned up; the normal host binaries remain available. No existing
user node/client state was used. See `results.json` and `cleanup.json` for the
machine-readable outcome and cleanup checks.

## Follow-up

The updated remote diagnostic should determine whether this ARM64 host has a
missing user bus, denied namespace, limit hit, explicit sandbox rejection or
another observed failure. If it still fails, the failed JSON record and current
log provide the next evidence needed. Native ARM64 Ubuntu sandbox/kernel/display
validation remains a deployment check; cross-building does not establish it.
