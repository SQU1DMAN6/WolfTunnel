# FtR WolfTunnel

Build v0.5 — remote browsing with a native SDL3 client and a separate Go Wolf Node.

The node runs an isolated Chromium engine, executes webpage JavaScript and sends
compressed viewport pixels over TLS 1.3. The native client displays those pixels
and sends browser input over an independent authenticated control connection.
It never runs a local webpage engine. Existing v2 enrolment and metadata tools
remain available; audio and file/clipboard transfer remain deferred.

Install Go 1.27.x, a C compiler, pkg-config, SDL3 **3.4.18** and SDL3_ttf
**3.2.2** development/runtime libraries. The font is embedded DejaVu Sans 2.37.
There are no third-party Go modules. Browser-enabled nodes currently require
Linux, unprivileged namespaces, bubblewrap, a working systemd user manager with
cgroup v2 delegation, and an explicitly pinned Chromium engine. Missing
isolation dependencies fail closed. The client does not need Chromium.

```sh
make build          # both executables
make node           # node without SDL/cgo
make test           # unit/legacy TLS tests + go vet
make race
make fuzz           # seven bounded fuzz campaigns
make smoke          # native UI smoke, no browser required
make browser-smoke BROWSER=/path/to/chromium/engine
make browser-native BROWSER=/path/to/chromium/engine  # desktop screenshot
```

`browser-smoke` also starts a temporary local node and built client, renders
controlled fixtures, tests browser input/pixels/isolation, measures latency and
closes all session resources. Browser integration tests intentionally skip in
plain `go test` unless `WOLF_BROWSER_BIN` is provided; the Makefile browser target
requires it. Screenshots and raw results go under ignored `build/v0.5/`.

See [USAGE.md](USAGE.md) for node/client setup, [ARCHITECTURE.md](ARCHITECTURE.md)
for execution boundaries, [Protocol v3](docs/PROTOCOL-v3.md) for the wire format,
and the [implementation and validation report](docs/IMPLEMENTATION-v0_5-2026_10_25.md)
for requirement coverage, measurements and remaining release considerations.

Each installation has its own Ed25519 key. Existing policy remains: owner-issued
single-use invitations expire after 30 days; credentials expire after 90 days.
The certificate pin identifies the node; an invitation authorises a new client.
Revocation terminates active browser sessions and does not rotate other clients.
Keys and state use private directories/files; protect backups too.

TLS protects URLs, inputs and pixels from the client's ISP/network administrator.
They still see the node address, connection timing and traffic volume. Websites
see the node's outbound network identity; logins, cookies and fingerprinting can
still identify a user. The node and its hosting environment are trusted. See the
[v0.5 threat model](docs/THREAT_MODEL-v0_5.md) for the precise guarantees and limits.

Author: Quan Thai. October 2026. See [LICENSE](LICENSE).
