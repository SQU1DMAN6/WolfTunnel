# WolfTunnel quick usage

See [SETUP.md](SETUP.md) for node installation and hosting. These commands assume
`wolfnode` and `wolfclient` are available on your PATH.

## Start and connect

Run `wolfnode`. It provisions its own renderer on first launch and saves default
settings; installing system Chromium or creating browser configuration is not
required. It listens on TCP 13300.

Create a private invitation in another terminal under the same node account:

```sh
umask 077
wolfnode -issue-enrolment > "$HOME/client.invite"
```

For clients on other machines, set their reachable address once with
`-invite-address node.example.net:13300`. The node remembers it. Transfer the
complete `wolftunnel://...` invitation privately. Launch `wolfclient`, paste it in
Settings and choose **Connect**. Later launches reuse saved credentials.

Enter an HTTP/HTTPS URL and **Go**. Mouse, keyboard, scroll, tabs and resize work
through the remote browser. Back/Forward navigate history; Stop cancels loading.
`wolfclient -open https://example.com/` opens a page after connecting.

| Shortcut | Action |
|---|---|
| Ctrl/Cmd+L | Address bar |
| Ctrl/Cmd+T / W | Open / close tab |
| Ctrl/Cmd+R | Reload |
| Ctrl/Cmd+, | Settings |
| Ctrl/Cmd+Q | Quit |

## Defaults and connection errors

New state lives under `~/.local/share/wolftunnel/`: `node/` for the node and
`client/` for the client. The client log is **`logs/client.log`** there, with
timestamps, events and safe error explanations. Existing default installations
retain their identity; `XDG_DATA_HOME` and `-state-dir` allow other locations.
An explicit custom client state directory uses its own `logs/client.log`.

A bare enrolment code also needs the node address and owner-verified certificate
pin. Prefer the complete invitation. A changed pin is blocked until verified.
Invitations expire after 30 days; credentials last 90 days and renew while valid
and near expiry. To revoke an installation, run `wolfnode -revoke CLIENT_ID` under
the node account, using the same state location as its service.

Private state uses 0700 directories and 0600 files. Preserve it across restarts.
Browser state is temporary and disappears when its session closes. **End session**
closes it while retaining installation credentials; reconnect to browse again. Public ports
80/443 are supported; internal destinations are blocked. Audio, file transfer and
clipboard/profile sharing are unavailable. TLS protects contents; traffic timing
and volume, website logins and fingerprinting remain visible. The node is trusted.

If your network requires an approved HTTP/HTTPS proxy, select **http-connect**
and enter its address in Settings. Otherwise keep **tls-tcp**. Node pinning
remains mandatory; policy denial and TLS interception cannot be fixed by retrying.
No direct fallback or proxy authentication is supported. Settings diagnostics
and the client log show the failed stage. Pinch zoom works where SDL supplies
native pinch events; fractional horizontal/vertical scrolling is supported.
