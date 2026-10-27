# WolfTunnel v3 (application v0.5 / v0.6)

v0 and v2 remain unchanged. Browser channels use a dedicated 48-byte big-endian
header: ASCII `WolfTunnelv3` (12), version 3 (1), message type (1), flags (2),
payload length (4), random session ID (16), sequence (8), magic F00DBEEF (4).
Control payloads are strict JSON, at most 64 KiB. Pixel payloads are binary, at
most 8 MiB. Unknown flags/types/versions, trailing JSON and inappropriate fields
fail closed. Sequence starts at 1 independently in each direction/channel;
control sequences are contiguous. Pixel sequences increase; holes are permitted
only because each transmitted update is an independent complete refresh.

Types 1–25: HELLO, NODE_IDENTITY, AUTHENTICATE, AUTH_RESULT, SESSION_CREATE,
SESSION_CREATED, PIXEL_ATTACH, SESSION_CLOSE, SESSION_STATUS, NAVIGATE,
NAVIGATION_STATUS, INPUT_EVENT, INPUT_ACK, VIEWPORT_SET, VIEWPORT_ACK,
PIXEL_UPDATE, FRAME_REFRESH, CANCEL, PING, PONG, ERROR, TAB_CREATE, TAB_SELECT,
TAB_CLOSE, HISTORY. The exact schemas are the reference Go `Control` structure
and per-type allowed-field tables in `internal/protocol/v3`.

Both persistent channels require TLS 1.3 and the same owner-verified certificate
pin. HELLO offers [3,2]; v3 selects the highest common version, 3. Existing v2
diagnostic operations negotiate separately with their existing codec. Browser
requests never silently fall back to metadata-only browsing.

Each channel authenticates an Ed25519 signature over 32 TLS exporter bytes
(label `WolfTunnel-v3`), message type, header session ID and canonical JSON with
signature omitted. Authentication includes client ID, role and requested session.
Session creation starts with an all-zero ID. The server returns a random 128-bit
ID. Pixel attachment requires fresh authentication by the same client for that
ID; the ID itself grants no authority. Subsequent messages rely on TLS integrity,
sequence validation and ownership/expiry/revocation checks, without per-keypress
signatures. Loss of either channel tears down the session.

PIXEL_UPDATE uses full-refresh flag 1. Its 48-byte binary payload prefix is:
encoding (1: JPEG, 2: PNG), reserved (3 zero bytes), width u32, height u32,
capture timestamp u64 (Unix nanoseconds), last applied input sequence u64,
capture/encoding duration u32 (microseconds), tab ID (16 bytes); encoded image
bytes follow. Offsets are 0 encoding, 1–3 reserved, 4 width, 8 height,
12 timestamp, 20 input sequence, 28 duration, 32 tab ID, 48 image.
CSS and output pixels use DPR 1. Decoders validate dimensions/pixel budgets
before allocating. Viewport changes invalidate old-size frames and require a
complete new refresh. FRAME_REFRESH requests an immediate independent image.

Each session owns one sandboxed browser process with isolated tabs and an
ephemeral profile. Only the selected page captures pixels and receives input.
Browser-control methods are fixed server operations, never arbitrary CDP.
Input has distinct pointer/button/wheel/key-down/key-up/text/focus events;
coordinates are CSS viewport pixels. Bounded input queues preserve urgent event
order; replaceable movement is coalesced. Overload closes the affected session
rather than silently losing key/button releases. Image queues hold one latest
recoverable image. Navigation, startup, idle and write deadlines bound work.

Security/error codes are fixed categories, never raw browser diagnostics. No
paths, queries, screenshots, keyboard contents or browser console messages are
written to application logs. INPUT_ACK carries the acknowledged control sequence,
node validation/dispatch overhead in `dispatch_us`, and separate CDP/browser work
in `browser_us`. PONG acknowledges the PING sequence in `ack`. ERROR permits an
optional tab ID. NAVIGATION_STATUS may carry the current public HTTP(S) target
for the native address bar; it stays encrypted and is never logged.

Nodes implementing detailed startup diagnoses advertise
`browser-start-diagnostics` in NODE_IDENTITY capabilities. A supporting client
includes that single capability in its signed AUTHENTICATE payload only after
seeing the advertisement. The new field is optional; no header/version change is
required. The node validates the capability list and signature before enabling
specific browser-start error codes. Older clients omit it and receive the legacy
`BROWSER_START_FAILED`; the node retains the specific local diagnosis. New clients
connecting to older nodes send the original authentication schema. Error text
and mitigation are derived from a local fixed catalog, never arbitrary peer text.

Independent images permit sequence gaps without a missing base. Stale or repeated
sequences fail closed. Resize mismatches request a complete FRAME_REFRESH and
drop the stale-size image. Equal captures are suppressed; a configurable periodic
refresh (default two seconds) and explicit FRAME_REFRESH restore complete state.

Explicit controlled-test screenshots are artifacts,
not operational logging. Audio, uploads, downloads, local clipboard/profile
sharing and arbitrary host control are excluded.

## v0.6 input capability and transport

Nodes advertise `input-v06` in NODE_IDENTITY. Only when present does a client
send optional `repeat: true` on key_down or a `kind: "pinch"` input with viewport
x/y and relative `scale` (0.25–4). Other input kinds cannot carry nonzero scale;
repeat is invalid on key_up/text. All numeric fields must be finite; coordinates,
UTF-8, length and modifier/code allowlists remain validated. Unknown fields
still fail strict decoding. A new client omits the extension for old nodes;
old clients ignore the extra advertised capability and retain original inputs.
The authentication capability list remains `browser-start-diagnostics` only;
`input-v06` adds no authorization or signature schema. Wire version stays 3.

Direct TCP and explicit HTTP/HTTPS CONNECT carry these exact same TLS-protected
frames. CONNECT is setup outside node TLS, not a new Wolf wire protocol. Each
role gets its own tunnel and TLS-exporter-bound proof. The client independently
checks node TLS/pin after every connector returns. There is no raw framed
plaintext mode. Local `CONTROL_STREAM_FAILED` and `PIXEL_STREAM_FAILED` diagnoses
are client-side categories and are not new server error schema fields.
