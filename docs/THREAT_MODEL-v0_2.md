# v0.2 threat model

The client and node are separate trust domains. TLS 1.3 with an explicitly
verified certificate pin protects their channel. Certificate discovery is a
public, unverified operation; a changed pin is a security failure. Pinning
authenticates the node, not the website or its content.

Independent Ed25519 client keys prove possession using connection-bound TLS
exporter signatures. Single-use, ten-minute owner codes authorise enrolment;
there is no source-embedded or permanent shared authentication secret. Per-client
expiry, revocation and signatures are checked on each operation. Anyone can
implement another protocol client; valid authorisation, not official binary
identity, is the boundary. One client's key cannot impersonate another client.

The node exposes no client-directory, other-session, body or result lookup API.
Tabs have independent request contexts, cancellations and local metadata history.
Device identity authenticates each connection; tabs never carry website cookies,
user-supplied headers or remote login credentials. Requests have no mutable
shared cookie jar. Bodies are bounded, discarded, and never executed or stored.

The outbound HTTP boundary allows GET over HTTP:80 or HTTPS:443 only. Userinfo,
unsupported schemes and prohibited IPs are rejected. All DNS answers are checked
before any dial; connections use the validated numeric IP, eliminating a second
lookup. Each redirect and each new connection repeats validation. Mixed public /
prohibited answers fail closed. Environment HTTP proxies are disabled. Private,
loopback, link-local, multicast, unspecified, shared CGNAT, documentation,
translation/tunnel and selected reserved ranges are blocked conservatively.
Explicit metadata/infrastructure addresses are blocked, including Azure
168.63.129.16; allocation changes require policy review. OS/network egress
restrictions remain a separate deployment requirement.

Transport errors, node policy/limit errors, and remote HTTP status codes are
distinct. A remote 200 does not mean content was rendered or safe. A remote 502
is identified as an HTTP response; missing remote responses never acquire a
fabricated status code. Response headers use an allowlist of Content-Type,
Content-Length and Content-Encoding. No Set-Cookie, authentication challenge,
hop-by-hop header, redirect Referer or user credential is forwarded.

Resource limits bound connections, fetch workers, per-client concurrency/rate,
frame size, header/body bytes, redirects and deadlines. Queues do not grow with
load. Cancellation and shutdown close sockets and stop fetches. These are
application limits, not complete protection against network-level denial of
service. A rate window can admit a burst at its boundary; unauthenticated peers
can consume a bounded number of handshake slots until timeout. Use service and
network controls for exposure beyond a controlled test environment.

Protected state uses private files, strict parsing and atomic replacement.
Existing identities are never silently replaced. Filesystem access as the owner
is the documented administrative boundary for issuing codes and revoking clients.
No covert monitoring API is added. Audit events omit paths, queries, secrets and
bodies; client browsing metadata remains in memory. Logs and persistent public
client IDs can still reveal operational relationships and must be protected.

The node operator, OS, privileged host users and hosting provider remain trusted.
A compromised node can observe decrypted requests and manipulate results.
Websites see the node IP; search engines receive queries. Endpoint IPs, timing and
traffic volume are visible to network observers. TLS does not provide anonymity,
traffic camouflage, browser-profile isolation or protection from a compromised
desktop. Client keys are file-protected, not keychain-backed. Deleting logs does
not erase backups, storage snapshots or provider telemetry. Production egress,
service hardening, cross-platform protection and independent security review are
separate from the local verification recorded in the implementation report.
