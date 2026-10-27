# WolfTunnel v0.6 threat model

The [v0.5 model](THREAT_MODEL-v0_5.md) remains the security foundation. This
review covers the v0.6 transport/input changes and local tests on 27 October 2026.

## Transport and visibility

Both direct and CONNECT modes require inner pinned TLS 1.3, certificate validity
and independent Ed25519 proofs bound to each TLS channel/session/role. Connector
implementations cannot remove the application-level pin check. Inspection that
substitutes a certificate fails closed; trusting a proxy CA does not trust that
proxy as the Wolf Node. There is no downgrade, automatic proxy/direct fallback,
post-authentication action replay or transparent restoration of website cookies.
HTTPS proxies additionally require ordinary hostname/system-root verification.

A proxy sees the requested node authority and traffic timing/volume; a plaintext
HTTP proxy also exposes CONNECT metadata on the local network. For direct/plain-proxy connections, node hostname/SNI where emitted is visible;
with an HTTPS proxy, a local observer sees the proxy connection instead.
Addresses, duration and encrypted traffic patterns remain observable.
This is not traffic-analysis resistance or hidden application intent. The node,
its host and the client OS remain trusted. Sites see the node's browsing egress
identity and can still identify accounts, cookies or browser characteristics.
A local node on the client's machine demonstrates correctness, not separation
from that machine's network observer. Endpoint monitoring can observe local input.

A blocked, untrusted destination cannot be made approved by selecting TCP or
CONNECT. This release has no verified Zscaler deployment results. It requires
administrator-approved routing/proxy and an inspection policy compatible with
the node pin. No VPN clone, protocol impersonation, padding or cover traffic is
implemented. Supported transport compatibility is distinct from network policy.

## Inputs, browser containment and state

Real user movement, buttons, Unicode, key repeat and bounded pinch scale are
fixed protocol operations. No arbitrary JS/CDP/control endpoint was added.
Local controlled fixture tests may inspect their own DOM; production clients
cannot request Runtime.evaluate. The native shell remains outside page pixels.
The verification fixture tests usable input/rendering, not CAPTCHA acceptance
or solving. No human-like noise, automation of third-party challenges or
fingerprint manipulation is included.

Namespace/cgroup containment, Chromium sandbox verification, public-address-only
egress, ownership, expiry, revocation and bounded frames/queues remain mandatory.
Loopback fixture access is injected only by tests; production egress still
rejects loopback/private/mixed DNS. End session destroys the isolated browser
and its website storage while preserving installation credentials. Overflow
ends the session instead of losing a release; blur/resize/tab changes release
held input. Session and generation checks reject stale channels/UI progress.

## Diagnostics and evidence

Operational logs contain fixed states/categories and aggregate counters, without
proxy response bodies, credentialed proxy URLs, invitations, typed text, page
paths/queries, pixels or cookies. Explicit screenshots and packet captures are
private test artifacts, not ordinary logs. Test captures use a private network
namespace containing only fixture traffic; host networking is unchanged.

Tests cover pin substitution even through an approved local proxy, connector pin
bypass, revocation, cross-client attachment, strict framing, private egress,
stream interruption, bounded queues and real browser teardown. These tests do
not constitute a new sandbox penetration audit. Physical trackpads/IME,
ARM64 runtime and live managed networks need separate testing. See the
[implementation report](../doc/IMPLEMENTATION-v0_6-2026_10_27.md).
