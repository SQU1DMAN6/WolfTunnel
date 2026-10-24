# WolfTunnel protocol 0.2

Implementation decision: Go for both applications; a repository-owned cgo SDL3
adapter provides windowing, input and rendering. There is one frontend.

TLS 1.3 is mandatory. Node certificates use Ed25519. A client must supply an
out-of-band verified SHA-256 certificate fingerprint before enrolment or use.
Discovery is explicitly unverified and only retrieves a public certificate.

The 21-byte big-endian frame retains the v0 layout, with identifier
`WolfTunnelv2`, magic `0xF00DBEEF`, one-byte message type and four-byte length.
Payloads are JSON, at most 64 KiB. Unknown fields, trailing JSON, unknown types,
missing IDs and invalid fields are rejected. IDs are 1–64 ASCII alphanumeric,
underscore or hyphen characters. v0 frames receive a v0-framed negative ACK
(or PONG for a ping), with `status: unsupported_version` and an explicit upgrade
message readable by the original v0 client. Unknown newer versions receive a v2
`UNSUPPORTED_VERSION` error. The connection closes without fetching or accepting
legacy authentication. The original v0 codec and tests remain in
`internal/protocol/protocol.go`; there is no legacy fetch endpoint.
The rejection path drains at most the legacy 1 MiB limit without retaining or
parsing it, under the five-second handshake/exchange deadline, so original
clients can finish writing before receiving their readable upgrade response.

| Number | Message |
|---|---|
| 16 | HELLO (versions: [2]) |
| 17 | NODE_IDENTITY (version, fingerprint, capabilities) |
| 18 | ENROLMENT_REQUEST (public_key, code) |
| 19 | ENROLMENT_RESULT (client_id, expires) |
| 20 | AUTHENTICATE (client_id, signature) |
| 21 | AUTH_RESULT |
| 22 | HTTP_FETCH (method: GET, target) |
| 23 | HTTP_RESULT (metadata only) |
| 24 / 25 | PING / PONG |
| 26 | CANCEL (same request_id as active HTTP_FETCH) |
| 27 | ERROR (code, safe message) |
| 28 / 29 | RENEW / RENEW_RESULT |

All payloads use the shared `Envelope` schema, with a mandatory `request_id`.
The exchange is HELLO, NODE_IDENTITY, then one enrolment or authentication.
After authentication, one PING, RENEW or HTTP_FETCH is allowed per connection.
HTTP_FETCH may be followed by CANCEL; disconnect also cancels it. A fresh TLS
connection per operation bounds state and prevents cross-tab mutable sessions.

Client identity is an independent Ed25519 key pair. The client ID is the hex
SHA-256 of its public key. Owner-created enrolment codes contain 256 random
bits, expire after ten minutes and are single use; only their hash is stored.
Enrolment is disabled without an owner-created code. The node stores each
client's public key and a 30-day expiry. Revocation uses a separate persistent
marker checked on each authenticated operation; it cannot be undone by renewal.
Renewal proves possession of an unexpired key and extends expiry to 30 days.
Expired or revoked clients require owner intervention and a new client identity.

AUTHENTICATE and every subsequent operation sign the canonical JSON envelope
with `signature` blank, prefixed by the message byte and 32 bytes from TLS
exporter label `WolfTunnel-v2`. This binds proof to this TLS connection, message
type, request ID and all fields, preventing replay across connections or target
substitution. CANCEL is signed and must match the running request. No universal
token, client private key, cookies or arbitrary headers travel on the wire.

ERROR codes distinguish protocol, authentication and limit failures.
HTTP_RESULT has an HTTP status only when a remote response was received;
otherwise its `code` is NODE_TIMEOUT, NODE_DNS_ERROR, NODE_POLICY_REJECTED,
NODE_RESPONSE_TOO_LARGE, NODE_REDIRECT_LIMIT, NODE_CANCELLED or NODE_FETCH_ERROR.
Client connection failures use NODE_UNREACHABLE or NODE_IDENTITY_CHANGED.
Results include the negotiated version, node fingerprint and request ID.

## Setup and recovery follow-up

Owner invitations use `wolftunnel://HOST:PORT/enrol?pin=HEX&code=HEX`. This is a
private setup payload, not an HTTP request target or a new wire message. It is
accepted only through explicit trusted-source import. The code retains its
single-use, ten-minute policy; the pin is public identity data, not access proof.

The client can reconstruct its public client ID from the existing private key
and perform AUTHENTICATE followed by PING, restoring local credential metadata
only after proof succeeds. It never replaces or revives node records. Existing
nodes support this flow. A credential with fewer than seven valid days remaining
is renewed using the existing signed RENEW exchange.

New node builds distinguish ENROLMENT_CODE_REQUIRED, ENROLMENT_CODE_INVALID
(including unknown/consumed/wrong-node codes), ENROLMENT_CODE_EXPIRED,
ENROLMENT_ALREADY_ENROLLED, ENROLMENT_CLIENT_REVOKED, ENROLMENT_STORAGE_ERROR,
ENROLMENT_CAPACITY and ENROLMENT_INVALID_KEY. These are additive ERROR code values;
framing and message schemas remain version 2. Older ENROLMENT_DENIED responses
remain supported. Peer-supplied diagnostic codes are allowlisted before logging
or display; arbitrary error payload text is not logged.
