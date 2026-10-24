# ARCHITECTURE.md — WolfTunnel v0.2

## Two processes, one goal

WolfTunnel has two separate executables that talk to each other:

- **wolfclient** — native SDL3 desktop app (the "frontend")
- **wolfnode** — Go HTTP service (the "node")

They communicate via a **documented, versioned binary protocol** over a TLS connection. Two processes doing a handshake.

## Protocol — v0 vs v2

- **21-byte big-endian frame**: identifier + magic + type + payload length
- **v0**: `Tunnelv0` identifier, `0xF00DBEEF` magic (12 + 4 + 1 + 4 bytes)
- **v2**: `Tunnelv2` identifier, same layout but new version byte
- Payloads are JSON, max 1 MiB (v0) or 64 KiB (v2)
- Unknown fields, trailing data, unknown types → rejected

**v2** is the new hotness. v0 clients get a readable "unsupported_version" NACK/PONG that tells them to upgrade. The old v0 codec and tests remain in `internal/protocol/protocol.go` for compatibility.

**Message types** (v2):
HELLO, NODE_IDENTITY, ENROLMENT_REQUEST, ENROLMENT_RESULT, AUTHENTICATE, AUTH, HTTP_FETCH, HTTP, PING, PONG, CANCEL, ERROR, RENEW, RENEW_RESULT

All payloads use the shared `Envelope` JSON schema with a mandatory `request_id`. IDs are 1–64 ASCII alphanumeric, underscore or hyphen.

## Components

### Client (`cmd/wolfclient/main.go` + `frontend/`)
- SDL3 native desktop window
- Model-View-UI pattern (`frontend/model.go`, `frontend/sdl.go`)
- Tab management: max 7 request tabs + 1 reserved Settings tab
- Connection handling, enrolment, authentication, fetch operations
- `nodeclient` library — shared Go client used by both executables
- Audit logging (`internal/audit`)

### Node (`cmd/wolfnode/main.go` + `internal/node/`)
- Go HTTP service, listens on TCP
- Auto-creates state directory, Ed25519 identity + TLS cert on first start
- TLS 1.3 mandatory, Ed25519 certificates
- Certificate fingerprint pinned and verified out-of-band by client
- Fetches approved HTTP GET requests (port 80/443 only)
- Validates: schemes, userinfo, ports, IP ranges, DNS answers, every redirect
- Rate limits, per-client concurrency limits, frame/header/body size limits
- Audit logging with rotation (1 MiB, 7 days, max 64 files)
- Node certificate: one-year lifetime, deliberate rotation required

### Internal packages

| Package | Purpose |
|---------|---------|
| `internal/protocol` | Message encode/decode, v0/v2 frame handling |
| `internal/node` | Node coordination, TLS, auth, limits, server loop |
| `internal/nodeclient` | Shared client library — Enrol, Ping   |   | Pingconnect, Renew, Ping   |   | PingPing, Discover, Fetch |
| `internal/auth` | Error codes, enrolment denial codes, expiry/revocation policy |
| `internal/enrolment` | Invitation code parsing/validation, URI encoding |
| `internal/fetch` | Outbound HTTP GET with validation, redirect handling |
| `internal/identity` | Ed25519 key pair generation, DER certificate |
| `internal/storage` | Private files, atomic replacement, permission enforcement |
| `internal/audit` | Audit events, retention/rotation, URL sanitisation |

### Storage — 'cause files need homes too

- **Node state** (`~/.wolftunnel/`): config.json, identity/node.json, credentials/clients/<client-id>.json, credentials/enrolments/<code-sha256>.json, credentials/revoked/<client-id>.json, logs/wolfnode-<UTC-hour>.jsonl
- **Client state** (`~/.wolftunnel-client/`): client-key.json, config.json (node address, verified pin, public client ID, expiry, UI preferences)

Both use `internal/storage` which:
- Enforces 0700 on directories, 0600 on private files
- Atomic replacement via temp file + link/rename
- Rejects insecure permissions
- Disallows symlinks, null bytes, trailing data

### Identity — Ed25519 all the way down

- Client: independent Ed25519 key pair; client ID = hex SHA-256 of public key
- Node: auto-generated Ed25519 identity + self-signed TLS certificate
- Both stored as base64-encoded in protected JSON files
- Codes are 256 random bits, single-use, expire   |   expireafter ten minutes; only their hash is stored

### Audit — logging without the drama

- Events include: request IDs, client IDs, destination hosts, HTTP statuses, durations
- Sanitised: omit paths, queries, fragments, private keys, enrolment codes, bodies
- Client logs use prefix `wolfclient-`, node logs use `wolfnode-`
- Rotation at 1 MiB, retain 7 days by default, capped at 64 files
- `HostOnly()` strips paths, keeping only `scheme://host`
- `SanitizeURL()` strips userinfo, queries, fragments

## Frontend — SDL3 + Go, but make it UI

- `frontend/sdl.go`: cgo SDL3/SDL_ttf adapter (pinned versions 3.4.18 / 3.2.2)
- `frontend/model.go`: `Model` struct with tabs, connection state, jobs channel
- `frontend/g.go` (UI event loop): handling, forms, input, tab navigation
- Font: embedded DejaVu Sans 2.37 (licensed included)
- No HTTP listening, browser engine, or webpage rendering

## Dependencies

| Dependency|Purpose|
|---|---|
|Go 1.27.x|Implementation language|
|SDL|CGO for SDL windowing|
|SDL|SDLdl3 3.4.18|SDL|SDL|SDL|sdl3-ttf 3.2.2|Font rendering|
|No third-party Go modules|Everything is in-tree|
