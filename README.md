# FtR WolfTunnel v0.1 (protocol prototype)

This is a small, dependency-free Go prototype for the first WolfTunnel milestone. It includes:

- `wolfnode`: a TLS-protected Wolf Node listener, default TCP port `13300`.
- `wolfclient`: a local browser-style UI with internal tabs, a URL/search bar, a Configuration tab, a bottom connection panel and a dummy relay-state view.
- `WolfTunnelv0` framing with the 4-byte magic `0xF00DBEEF`.
- Token authentication, client-side SHA-256 certificate pinning, URL metadata logging with a seven-day default retention period, and in-memory client configuration.

**This version does not fetch or render websites.** It sends a dummy request to the node and displays the acknowledgement. It does not yet implement Chromium, media capture, video encoding or remote browser execution.

## Requirements

- Go 1.23 or newer.
- OpenSSL for generating a node certificate and random access token.
- A modern desktop browser for the local UI.

There are no third-party Go module dependencies.

## Build

From this directory:

```sh
go test ./...
go vet ./...
go build -o wolfnode ./cmd/wolfnode
go build -o wolfclient ./cmd/wolfclient
```

## Run a Wolf Node

Run these commands in a private directory on the node host. Do not overwrite an existing certificate or token without a deliberate rotation plan.

```sh
umask 077
openssl req -x509 -newkey rsa:3072 -sha256 -days 365 -nodes \
  -keyout wolfnode.key -out wolfnode.crt \
  -subj "/CN=WolfNode" \
  -addext "subjectAltName=IP:129.150.63.22"
openssl rand -hex 32 > wolfnode.token
chmod 600 wolfnode.key wolfnode.token
```

Start the node:

```sh
export WOLFTUNNEL_TOKEN="$(cat wolfnode.token)"
./wolfnode -listen :13300 -cert wolfnode.crt -key wolfnode.key -log-dir ./logs -retention 168h
```

The node prints the SHA-256 fingerprint of its certificate at startup. Share the fingerprint and token with the client through a trusted channel. **Never share `wolfnode.key`.** The token is a shared secret; do not paste it into public issue trackers or commit it to source control.

The node accepts one framed request per TLS connection. It validates the token and target, logs a sanitised URL record, and returns an acknowledgement. It does not connect to the target website. Audit logs are JSON Lines files with restrictive permissions. Query parameters and fragments are removed from logged URLs; URL paths may still contain sensitive values, which is why retention is short. The node does not log the connecting client's IP address in application logs, though the operating system and hosting provider may still observe network metadata.

## Run the client

```sh
./wolfclient
```

Open `http://127.0.0.1:8787` if the browser does not open automatically. The UI binds to loopback only by default. Keep it loopback-only; do not expose the local UI to a LAN or public interface.

Open **Settings** and enter:

- Wolf Node address: `129.150.63.22`
- TCP port: `13300` (changeable)
- TLS certificate SHA-256 fingerprint printed by the node
- The shared token from `wolfnode.token`
- Google or DuckDuckGo as the search engine for plain-text input

Click **Apply in memory**, then **Test connection**. The token is not returned by the local configuration API and configuration is not written to disk. Closing the client process clears the in-memory settings. The search field is not placed into the browser's address bar or local storage.

For a request, enter a URL such as `https://example.com` or search terms. The client sends a dummy request to the Wolf Node. The node acknowledges receipt but does not fetch the URL; the UI explicitly shows this state instead of pretending to display a webpage.

## Wire protocol v0

All integers are unsigned big-endian. Every message has this 21-byte header:

| Field | Length | Value |
|---|---:|---|
| Protocol identifier | 12 bytes | ASCII `WolfTunnelv0` |
| Magic | 4 bytes | `0xF00DBEEF` |
| Message type | 1 byte | `1=request`, `2=ack`, `3=ping`, `4=pong` |
| Payload length | 4 bytes | JSON payload size, maximum 1 MiB |

The header is followed by the JSON payload. A request includes a request ID, shared token, method and target. The node returns an acknowledgement with status, state and `can_respond`. A ping checks authenticated reachability without logging a target URL.

TLS is mandatory. The client verifies the server certificate using an explicitly entered SHA-256 fingerprint; a mismatch is rejected. The shared token authenticates client requests to the node. Rotate the certificate pin and token deliberately when needed.

## Privacy and limitations

- TLS protects request contents in transit when the certificate pin and token are configured correctly.
- A network provider can still observe the node IP, port, connection timing and traffic volume. This prototype does not attempt traffic camouflage and cannot promise invisibility from an ISP.
- The node logs sanitised visited URL metadata for up to seven days by default. The retention option is configurable; use the shortest period that meets operational needs.
- The client is a local app shell. The destination website's HTML/CSS/JavaScript is not downloaded by this prototype.
- Browser state, cookies, forms, media and cache are not implemented because there is no remote browser yet. The UI itself keeps configuration and tab state in memory and uses `Cache-Control: no-store`.
- Do not deploy this to a production host until you have checked whether the selected port is already in use, confirmed the provider firewall, tested cleanup and resource limits, and established a rollback plan. Do not change existing firewall rules as part of this prototype setup without explicit review.

## Known limitations / next steps

1. Add protocol framing and URL-sanitisation tests.
2. Add node startup/readiness checks and a structured cleanup lifecycle.
3. Test local node/client end-to-end using a dedicated test certificate and token.
4. Add explicit per-connection and per-session quotas.
5. Only after the protocol is stable, begin isolated Chromium control and pixel capture.
