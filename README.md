# FtR WolfTunnel

*Current release: Build v0.2*

*Author: Quan Thai*

TL;DR: WolfTunnel is a native SDL3 desktop client + a separate Go Wolf Node. The client enrols with a verified node, submits an HTTP GET, and shows real HTTP status + bounded response metadata. It does NOT render websites, execute JS, or play media. Chromium, media streaming, cookies, and uploads remain outside this milestone. It's gonna be done later, don't worry.


## Build and test

Install Go 1.27.x, a C compiler, pkg-config, SDL3 **3.4.18** and SDL3_ttf **3.2.2** development/runtime libraries. The frontend embeds DejaVu Sans 2.37 and its licence. No third-party Go modules required.

```sh
make build    # builds wolfnode + wolfclient
make test     # run tests
make race     # with race detector
make fuzz     # four fuzz campaigns
make smoke    # smoke test
```

Build just the node (no SDL or cgo):

```sh
CGO_ENABLED=0 go build -o build/wolfnode ./cmd/wolfnode
```

## Usage Information

Check `USAGE.md` file in the project root for usage details.

## Credentials and privacy

Each client installation has its own Ed25519 key. Credentials last 30 days; still-valid credentials nearing expiry renew on connect. The existing key can recover lost enrolment metadata without reusing a code. Expired or revoked keys cannot recover or renew.

To revoke one installation, use its public Client ID from Settings → Diagnostics:

```sh
./build/wolfnode -state-dir /tmp/wolftunnel-node -revoke CLIENT_ID
```

Revocation affects subsequent operations and does not rotate other clients' credentials. It does not retroactively terminate an already-ised fetch; that fetch remains subject to its deadline.

**Forget node** (Settings → Diagnostics): removes local node configuration only. Does NOT revoke the server-side record or regenerate the client identity.

State directories require Unix `0700` permissions and private files use `0600`. Keep protected backups of keys; a missing, expired or insecure node identity fails startup rather than being silently replaced. The node certificate has a one-year lifetime; rotation is deliberate and requires re-ification.

Private client keys are protected files, not OS keychain-backed or encrypted at rest. Linux is the validated platform; other platforms need the protections and packaging described in the development guide.

Audit logs contain security events, request IDs, client IDs, destination hosts, HTTP statuses and durations. They omit paths, queries, fragments, private keys, enrolment codes and bodies. Logs rotate at 1 MiB, retain at most seven days by default, and are additionally capped at 64 files. Deletion does not erase backups or provider snapshots.

Client request history is memory-only; keys and selected settings persist.

TLS 1.3 and explicit pinning protect client-to-node contents. They do not hide the node IP, timing or traffic volume. Websites see the node's outbound IP; the node operator and hosting environment remain trusted. A compromised node or privileged host can observe processed data. This is not a general proxy or a claim of complete anonymity.

No production service, host firewall or provider rule is changed by this project. Controlled deployment requires a separate review of the service account, egress policy, state path, resource limits, recovery and rollback. See the development guide before exposing a node beyond loopback.

_FtR WolfTunnel, Version 0.2, October 2026, Written by Quan Thai_
