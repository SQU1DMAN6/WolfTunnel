# WolfTunnel v0.5 usage

Build with `make build`. The native client needs SDL3 3.4.18 and SDL3_ttf 3.2.2;
it does not need a local browser. Browser-enabled nodes currently require Linux,
bubblewrap, a working unprivileged systemd user manager/cgroup v2, unprivileged
namespaces and a maintained Chromium engine. Run as an ordinary service user.

## Start a local browser node

Choose the actual ELF engine executable, not a shell launcher (for example,
`/usr/lib/chromium/chromium`; this validation machine has `/opt/brave-bin/brave`).
Pin a known maintained build from a trusted installation. Obtain its SHA-256 at
installation time; deliberate upgrades require updating the pin. Do not run as
root or disable Chromium's sandbox. No host firewall changes are needed for this
loopback example.

```sh
./build/wolfnode -state-dir /tmp/wolf-node-v05 -listen 127.0.0.1:13300 \
  -browser-bin /path/to/chromium/engine \
  -browser-sha256 VERIFIED_EXECUTABLE_SHA256
```

The node announces its certificate SHA-256. State uses mode 0700 directories and
0600 files. Use separate node/client directories; normal persistent defaults are
`~/.wolftunnel` and `~/.wolftunnel-client`. If browser dependencies, hash or
sandbox verification fail, browser requests fail closed; v2 diagnostics remain
available. Starting without a browser pin leaves browser sessions unavailable.

The test engine/version/hash is recorded in
[the validation lock](docs/browser-validation-lock.json). It is evidence of the
local build tested, not a permanent production update policy.

## Enrol and connect

In another terminal, issue a private invitation from the same node state:

```sh
umask 077
./build/wolfnode -state-dir /tmp/wolf-node-v05 -issue-enrolment \
  -invite-address 127.0.0.1:13300 > /tmp/wolf-v05.invite
./build/wolfclient -state-dir /tmp/wolf-client-v05 -invite-file /tmp/wolf-v05.invite
./build/wolfclient -state-dir /tmp/wolf-client-v05
```

Alternatively paste the complete owner-issued invitation into Settings and click
Connect. It contains address, pin and single-use enrolment authorisation. Obtain
it over a trusted channel. Manual setup still accepts address, verified pin and
a fresh code. A correct pin identifies the node; it cannot authorise an unknown
client. Discover displays an **unverified** pin until independently checked.

The existing policies remain: invitations expire after 30 days, credentials after
90 days, and valid credentials nearing expiry renew on connect. Saved independent
Ed25519 keys reconnect without asking for the invitation again. Expired/revoked
keys cannot renew or recover. Protect private state and backups.

## Browse

Enter an absolute HTTP/HTTPS URL and Go. The node runs JavaScript and returns
pixels. Click, move, scroll and type within the webpage viewport. Ctrl/Cmd+L
focuses the native address bar; Ctrl/Cmd+T/W opens/closes a tab; Ctrl/Cmd+R reloads;
Ctrl/Cmd+, opens Settings; Ctrl/Cmd+Q quits. Back/Forward now navigate remote page
history. Stop cancels navigation. Closing the window/disconnecting tears down the
remote browser. Reconnection starts fresh cookies/storage, not a restored profile.

The window adapts to display bounds and DPI. Its available page area determines
the logical remote viewport; resize does not restart the browser. Default limits
are 1920×1200 including the optional 1920×1200 logical preset; the initial default
is 1920×1080. Four browser tabs share one per-client process scope by default.
Use `-window-size 1100x700` or `-ui-scale 1.5` for explicit native UI overrides.

The node permits public destinations on ports 80/443. Private, loopback,
link-local, metadata and prohibited addresses are blocked at actual egress,
including subresources and redirects. A blocked destination is a policy failure;
a remote HTTP 404/502 remains a remote response. HTTPS certificates are checked
by the node's browser. Audio, local file upload/download, clipboard sharing,
extensions and password-manager/profile integration are unavailable.

## Diagnostics, captures and limits

```sh
./build/wolfclient -state-dir /tmp/wolf-client-v05 -test-connection
./build/wolfclient -state-dir /tmp/wolf-client-v05 -fetch https://example.com/
./build/wolfclient -state-dir /tmp/wolf-client-v05 -metadata
./build/wolfclient -state-dir /tmp/wolf-client-v05 \
  -capture https://example.com/ -output /tmp/wolf-page.png -smoke-timeout 25s
```

`-fetch` and `-metadata` retain legacy v2 behavior. Capture explicitly writes a
private PNG and exits; it is not operational logging. `-capture-blank` checks a
1920×1080 session without navigating a website. Smoke/capture operations are
bounded; `-browser-smoke` is reserved for the controlled fixture used in tests.

Browser limits can be overlaid using `-browser-config /private/browser.json`.
It must be a mode-0600 strict JSON file in protected storage; missing fields use
validated defaults. Durations are integer nanoseconds. For example:

```json
{"max_sessions":2,"max_per_client":1,"max_tabs":4,"fps":15,"quality":90,"refresh":2000000000,"max_frame_bytes":8388608}
```

Provide the engine and hash flags as above. See [ARCHITECTURE.md](ARCHITECTURE.md)
for all resource defaults and allowed hard limits. Increasing viewport/capture
limits requires measuring memory, bandwidth and latency. Missing isolation is
never worked around by adding `--no-sandbox` or root privileges.

Node/client audit files are under their separate `logs/` directories. Browser
logs contain fixed categories and public identifiers/counters, never page URLs,
text, screenshots or raw CDP diagnostics. Startup/sandbox failures have safe
codes. Check dependencies and the private node logs when browser startup fails.

```sh
./build/wolfnode -state-dir /tmp/wolf-node-v05 -revoke CLIENT_ID
```

Revocation terminates active browser sessions within the authorisation sweep
(one second plus bounded teardown), and rejects future operations. Forget node
removes local configuration only; it does not revoke the installation or rotate
its key. Recover lost metadata by reconnecting with the existing valid key.
Deliberate certificate rotation requires fresh protected state and out-of-band
verification; changed pins are never accepted automatically.

## Validation and privacy

```sh
make browser-smoke BROWSER=/path/to/chromium/engine
make browser-native BROWSER=/path/to/chromium/engine
python3 tools/smoke_v05.py --browser /path/to/chromium/engine --url https://example.com/
```

Tests use private temporary node/client state, ephemeral loopback ports and
per-session scopes. They clean up without touching host firewall/services.
Controlled fixture routing is injected only by Go tests, never a production
private-network bypass. `build/v0.5/` holds timed native/node pixel screenshots,
latency and encoding comparison results. `browser-native` needs a real desktop;
the other screenshot test can use SDL's dummy software driver.

The trusted remote node is the browsing egress identity. TLS hides requests,
input and returned pixels from the client's ISP/network administrator, who still
sees node address/timing/volume. Account logins, browser fingerprinting and cookies
can identify users to websites. A node on the same machine/network is useful for
functional tests and does not establish remote-node anonymity. See the
[threat model](docs/THREAT_MODEL-v0_5.md) and
[validation report](docs/IMPLEMENTATION-v0_5-2026_10_25.md).
