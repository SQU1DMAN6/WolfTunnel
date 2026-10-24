# WolfTunnel v0.2, innit

## Setup: get this show on the road

### Spin up a relay node

```sh
# Make fresh state dir, one-shot enrolment code, bound to loopback
# This is needed if you want to set up your own relay server.
./build/wolfnode -state-dir ~/.wolftunnel-node -issue-enrolment \
  -invite-address 127.0.0.1:13300 > ~/wolftunnel.invite
```

This pops up:
- `~/.wolftunnel-node/`
- Ed25519 identity + self-signed TLS cert
- Your node's public SHA-256 fingerprint (keep this, you'll verify it)
- A single-use enrolment code on stdout, saved to `~/wolftunnel.invite`

**Code expires in 30 days. Deal with it.**

Use the code to verify your WolfTunnel client before you connect to a Wolf Node. If you don't do this, you can't use WolfTunnel with a relay. Which means you can't use WolfTunnel at all.

### Run the client

```sh
./build/wolfclient -state-dir /tmp/wolftunnel-client
# Or, if you're looking for everyday use:
./build/wolfclient -state-dir ~/.wolftunnel-client
# Or even just:
./build/wolfclient
```

Default client path is `~/.wolftunnel-client`. Use `-state-dir` to keep things
separate.

## Connecting: it's not rocket science

### GUI way (Settings tab)

1. **Copy and paste the entire owner-issued invitation (remember the ~/wolftunnel.invite from earlier?) into the masked field**
   - Click **Connect**
   - Address, pin, enrolment and storage sort themselves out
   - Later launches: client reconnects with saved key
   - Valid creds renew auto on connect (convenient, right?)

2. **Existing setup mode**
   - Choose **setup**, enter node address/port/pin + fresh code
   - Click **Connect**
   - No separate Apply needed; Apply preserves pending codes
   - Pin identifies the node but doesn't authorise a new install
   - Optional **Discover**: UNVERIFIED fingerprint, check separately

### CLI way (for the power users)

```sh
# Invitation file (import the whole shebang./build/wolfclient -invite-file /tmp/wolftunnel.invite

# Enrolment code file (never a CLI secret)
./build/wolfclient -enrol-code-file /tmp/wolf-enrol-code

# Discover the fingerprint (UNVERIFIED, obvs)
./build/wolfclient -discover

# Test connection, just the ping
./build/wolfclient -test-connection

# Fetch metadata for a URL, print JSON to stdout
./build/wolfclient -fetch https://example.com/
```

## Fetching: GET stuff

### CLI:

```sh
./build/wolfclient -fetch https://example.com/
```

Prints the metadata JSON to stdout.

### GUI:

1. Enter an absolute `http://` or `https://` URL
2. Click **Go**
3. Other input becomes a URL-encoded Google or DuckDuckGo query
4. Resolved URL appears before a second Go sends it
5. Search providers receive these queries
6. Back/Forward redisplays local metadata history
7. Closing a tab cancels its request

**Heads up**: Node only accepts public destinations on HTTP port 80 or HTTPS
port 443. Loopback, private, link-local, cloud metadata + other prohibited
addresses are blocked — including in DNS answers and redirects. A local test URL
like `http://127.0.0.1/` should return `NODE_POLICY_REJECTED`, not an HTTP
status. A remote `404` or `502` stays a remote HTTP response; the node stays
connected. Response bodies are bounded, discarded, and never rendered or saved.

## Credentials & privacy: keep it sharp

- Each client installation has its own Ed25519 key
- Creds last 90 days; still-valid creds nearing expiry renew on connect
- Existing key recovers lost enrolment metadata without reusing a code
- Expired or revoked keys: cannot recover or renew. Period.

### Revoke a client

```sh
# From the node side
./build/wolfnode -state-dir /tmp/wolftunnel-node -revoke CLIENT_ID
```

Revocation affects subsequent ops. Does not rotate other clients' creds. Does
not retroactively terminate an already-ised fetch.

### Forget node locally

- Done via Settings → Diagnostics → Forget node
- Removes local node config only
- Does NOT revoke server-side record or regenerate client identity

## Recovery: 'cause stuff happens

### Near-expiry renewal

```sh
./build/wolfclient -state-dir /tmp/wolftunnel-client -renew
```

Connect renews valid creds within 7 days of expiry automatically. Or use the
`-renew` flag.

### Recover lost metadata

If you have the existing installation key:

```sh
./build/wolfclient -state-dir /tmp/wolftunnel-client -codeFile \
  /tmp/wolf-enrol-code
```

Connect proves possession and recovers lost client ID/expiry metadata.

### Expired or revoked

Cannot recover. Revoke and enrol a new installation with a new client state dir
and fresh code.

**Permissions reminder**: State dirs need 0700, private files need 0600. Keep
protected backups of keys. A missing/insecure identity fails startup, not
silently replaced.

## Node certificate rotation

The node cert has a one-year lifetime. Rotation is deliberate and requires:
1. Stop the service
2. Retain a protected backup of old state
3. Create a **new state directory**
4. Distribute the new public fingerprint through a trusted channel
5. Clients must explicitly forget the old node and verify the replacement

No silent renewal with a new fingerprint. Ever.

# And that's WolfTunnel.

Have fun.
Happy browsing.
