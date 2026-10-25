# WolfTunnel v0.5 threat model

Reviewed against `doc/SPEC-v0_5-2026_10_25.md`, 25 October 2026. This extends the
v0.2 model; it does not replace the existing identity, storage or TLS safeguards.

## Assets, observers and trust

Protect navigation paths/queries, typed input, rendered pages, session cookies,
client private keys, node credentials and isolation between independent clients.
The Wolf Node, its operating system, operator and hosting environment remain
trusted: they necessarily process the browser's plaintext and pixels. The client
OS and its local key storage must also be trusted. A compromised endpoint or a
privileged host can observe the data it processes.

| Observer/adversary | Intended protection and remaining visibility |
|---|---|
| Client ISP/local network administrator | Pinned TLS 1.3 encrypts navigation/input/pixels. Node IP/port, timing, duration, sizes and volume remain visible. Traffic analysis/correlation can infer activity. |
| Destination website/public WWW | Browsing connections originate at the node. The client never fetches webpage resources directly or forwards local cookies/profile. Websites still see Chromium fingerprints, node IP, session storage, logins and user-entered information. |
| Other authenticated clients | Independent keys, fresh browser processes/contexts, ownership-bound channels, private profiles and namespace/resource boundaries prevent reading another session through supported APIs. A session ID is not a bearer credential. |
| Hostile webpage | Browser sandbox plus namespaces/cgroup and validating egress restrict host access and network reach. Fixed page-input methods provide no arbitrary CDP, shell or OS input endpoint. |
| Hostile unauthenticated peer | TLS/authentication, deadlines, frame caps, validated schemas, accepted-socket limits and session reservation precede expensive user browser work. |
| Revoked/expired installation | Creation and every operation fail authentication; idle authorisation checks also close existing sessions. Renewal cannot revive a revoked/expired key. |

This is a single trusted relay, not a mix network or a universal anonymity system.
It does not promise to hide intent from traffic correlation, account logins,
tracking or a malicious node. TLS does not hide the existence of WolfTunnel.
An ISP observing the node's egress may see destination IPs/DNS/TLS metadata and
unencrypted HTTP; a remote node's placement must be outside the client observer's
boundary for that separation to help. Local node tests demonstrate functionality,
not network anonymity. No cover traffic/padding is implemented.

## Transport and native interface

Both browser channels require TLS 1.3, explicit certificate-pin verification and
fresh Ed25519 proofs bound to the TLS exporter, session and channel role.
Changed/expired pins fail closed. There is no plaintext browsing fallback,
universal secret or automatic acceptance of discovered fingerprints. v2 remains
available for independently negotiated diagnostics/enrolment, not a browser
security downgrade.

Only compressed images and bounded metadata reach the native viewport. The client
has no webpage JavaScript engine. It decodes within fixed byte/dimension budgets,
clips images below the trusted native chrome and maps input into CSS viewport
coordinates. A site can mimic UI within its webpage, as in any browser; it cannot
paint over or replace the actual native address bar. Client OS clipboard/profile,
local browser cookies, location, microphone and arbitrary desktop events are not
forwarded. User-entered text/passwords are intentionally delivered to the remote
page and therefore visible to the trusted node and the receiving site.

## Browser compromise and egress

Chromium's namespace/seccomp sandbox stays enabled and is checked at startup.
The Linux launcher uses unprivileged bubblewrap namespaces and a per-session
systemd scope with hard memory/no-swap, CPU/tasks and runtime limits. No node
state or home is mounted. Fresh profiles/cookies/cache are bounded and destroyed
on session teardown. Read-only system libraries, fonts and the browser bundle
are available. Operators must keep the pinned engine and its supporting bundle,
libraries and host kernel maintained; an executable hash alone does not pin all
shared-library contents or guarantee absence of vulnerabilities.

There is no externally reachable debugging port. Fixed CDP calls travel over
private inherited pipes. Production clients cannot request Runtime.evaluate,
certificate bypasses, arbitrary commands or OS input. Tests use direct in-package
CDP introspection solely for controlled fixtures; a self-signed HTTPS service
worker fixture uses a test-only certificate override, absent from all production
configuration and wire schemas.

The browser network namespace has no outward interface. Its only path is a fixed
loopback bridge to its parent's private Unix proxy. Direct DNS, UDP/WebRTC and
other bypass attempts have no outward route. Each proxy connection validates the
scheme, port and every DNS answer, rejects private/mixed/metadata destinations,
and dials a checked numeric public address. Redirects and workers use that same
boundary; policy does not depend solely on interception of main-frame requests.
Public HTTP/HTTPS/WebSockets are supported. HTTPS CONNECT remains opaque; browser
TLS verification is enabled. A remote endpoint can still relay information on
its own network, as any public website can.

Ordinary HTTP form bodies remain bounded; multipart HTTP uploads are refused.
HTTPS request bodies are not inspected by the tunnelling proxy. Local files are
absent and browser file selection/downloads are denied. Browser-generated blobs
and user-entered form text can be submitted normally; this is not a DLP filter.
No download delivery, file transfer, clipboard sync, audio or local password
integration is implemented.

A Chromium/kernel vulnerability remains a residual risk. Context isolation alone
is not the boundary: separate browser processes, OS namespaces, private pipes,
restricted mounts and resource scopes add containment. This work is tested
engineering, not an independent sandbox penetration audit. Use a dedicated
restricted service account and review production exposure separately.

## Logging, persistence and deletion

Browser audit events contain allowlisted codes, public client/session IDs,
lifecycle timings and aggregate frame/byte/drop counts. They contain no URLs,
paths/queries, page contents, typed input, cookies, browser console or raw CDP
errors. Existing legacy metadata logs can contain destination hostnames.
Logs are protected and rotated; default retention is seven days, capped at
64 files, and node configuration cannot exceed 90 days. TLS certificates and
client keys persist; browser state/history is ephemeral.

Explicit capture/smoke commands intentionally write screenshots and controlled
fixtures. Treat real captures as sensitive and delete them when no longer needed.
Stopping a browser removes its live private tmpfs and proxy files. This is not
forensic erasure of host swap, storage snapshots, backups or hosting telemetry.
Private client keys are protected files, not encrypted/keychain-backed storage.

## Validation scope

Tests cover pin failure, exporter/role/session replay, wrong-client attachment,
strict frames/sequences, revocation, session/tab/viewport limits, bounded queues,
blocked browser pipes, active sandbox reports, cookies/storage across fresh
sessions, redirects, scripts/images/frames, allowed WebSockets, HTTPS and actual
service-worker DNS-rebinding to metadata, private files, cancellation, browser
exit and recovery. The controlled tests inject only a synthetic public upstream
route; production has no private-network override. An actual built node/client
pair also completed public HTTPS navigation.

See [the validation report](IMPLEMENTATION-v0_5-2026_10_25.md) for commands and
measured limits. WAN delay emulation is not a test of a geographically remote
node. IPv6 deployment, large-scale concurrent load, adversarial sandbox escape,
full IME/accessibility behavior, complex-site compatibility, production service
packaging and independent privacy/security assessment need further validation.
