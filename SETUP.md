# Set up a Wolf Node

Wolf Node runs the browser and streams its pixels to Wolf Clients. It now manages
its own pinned Chromium-based renderer: **you do not install Chromium or create a
browser configuration to get started**. First launch downloads the runtime once;
later launches reuse it. A deployment bundle can include it for offline startup.

These instructions target Linux **amd64 or ARM64** with systemd, including Ubuntu
24.04+ servers. On ARM64, use the ARM64 Go toolchain and build on that machine;
the node automatically downloads the native ARM64 renderer. Use an ordinary login account
for the node; `sudo` is only for installing system prerequisites and enabling
startup. No desktop environment or SDL libraries are needed on the node.

## 1. Build the node

Install Go 1.27+ from [Go downloads](https://go.dev/dl/) and `make`, then run this
from the WolfTunnel source directory:

```sh
make node
```

This produces the node executable without building the desktop client. You may
also receive a prebuilt node or the deployment bundle described below.

The host still needs bubblewrap, a working systemd user session/cgroup v2, and
common Linux browser runtime libraries. On Ubuntu 24.04+ or Debian 13, install them with:

```sh
sudo apt update
sudo apt install bubblewrap dbus-user-session libpam-systemd ca-certificates fonts-liberation fonts-noto-cjk fonts-noto-color-emoji libnss3 libnspr4 libglib2.0-0t64 libudev1 libgcc-s1 libatk1.0-0t64 libatk-bridge2.0-0t64 libatspi2.0-0t64 libcups2t64 libdrm2 libdbus-1-3 libx11-6 libxcb1 libxcomposite1 libxdamage1 libxext6 libxfixes3 libxrandr2 libgbm1 libxkbcommon0 libasound2t64 libcairo2 libpango-1.0-0 libgtk-3-0t64 libvulkan1
```

These are isolation, networking, graphics and font libraries; they do not install
a system browser. A server with an existing desktop may already have most of
them. If you just installed user-session support, log out and log back in.
The host must permit unprivileged user/PID/network namespaces and Chromium's
sandbox. Restricted containers that prohibit these features will not work;
use a suitably configured Linux host or VM instead of disabling the sandbox.

Ubuntu's [AppArmor policy](https://documentation.ubuntu.com/security/security-features/privilege-restriction/apparmor/#apparmor-unprivileged-user-namespace-restrictions)
can also restrict these namespaces. The host policy must allow bubblewrap and
Chromium's nested namespace sandbox, including creation of the private network
namespace. If the kernel journal reports `profile="unprivileged_userns"` denying
`setpcap` or `net_admin` for `bwrap`, install WolfTunnel's scoped launcher policy
once, from this repository **as the normal node account**:

```sh
make install-sandbox-policy
```

This explicit installation uses sudo to copy the distribution's `bwrap` to a
root-owned, group-executable WolfTunnel path and load its AppArmor profile. It
leaves AppArmor enabled and keeps other applications' bwrap policy unchanged.
Ordinary builds/startup never install or load host policy. Keep the node running
as its normal account. Rerun this installation after upgrading bubblewrap so the
private copy receives the distribution's fixes. If the service account uses a
different group, specify `NODE_GROUP=that_group`. The helper's parent directories
must be root-owned and have no group/other write permission.

The profile permits user namespaces for this launcher and its helper/Chromium
children; it is a compatibility allowance, not an additional restrictive
AppArmor sandbox. WolfTunnel continues to enforce its own namespaces, capability
drop, resource limits and verified Chromium namespace/seccomp sandbox. See
[the exact policy and troubleshooting steps](docs/BROWSER-TROUBLESHOOTING.md#ubuntu-apparmor-denies-setpcap-or-net_admin).

An SSH login is enough for the node: keep `dbus-user-session` and `libpam-systemd`
installed and log in as the node account so its systemd user manager is available.
The desktop client needs an actual graphical session; running it in a plain SSH
terminal will not create a usable desktop window.

## 2. Start it

```sh
./build/wolfnode
```

The node provisions/verifies its renderer, creates private state and default
settings, and listens on TCP **13300**. First launch needs internet access to the
official renderer download; allow roughly 200 MiB of download and at least 1 GiB
of free disk space during installation. A normal restart needs no download.

Wait for `node ready on ...`. The startup output includes the state directory and
node **certificate SHA-256**, which clients use to verify the node. This is
separate from the renderer executable hash, which WolfTunnel manages internally.

New installations use:

| Item | Default |
|---|---|
| Node identity, client authorisations and logs | `~/.local/share/wolftunnel/node/` |
| Saved listener/public-address preferences | `~/.local/share/wolftunnel/node/settings.json` |
| Automatically generated browser configuration | `~/.local/share/wolftunnel/node/browser.json` |
| Downloaded renderer | `~/.local/share/wolftunnel/node/renderer/` |

Existing installations retain their previous default identity/state directory.
`XDG_DATA_HOME` selects another data location; an explicit `-state-dir` is also
supported. Reuse the same account, environment and state location for service
startup and administrative commands.

The default listener accepts connections on the host's available interfaces. For
a local-only node, set this once:

```sh
./build/wolfnode -listen 127.0.0.1:13300
```

Listener preferences are saved. Subsequent starts need no listener flag. Do not
start a second node on the same port while the first is running.

## 3. Give a client an invitation

In another terminal under the same account:

```sh
umask 077
./build/wolfnode -issue-enrolment > "$HOME/client.invite"
```

This prints a **complete invitation**, containing address, certificate pin and a
single-use code. Copy the whole `wolftunnel://...` value to the intended client
over a trusted channel. A bare code alone cannot verify a new node's identity.
The default invitation uses a local address when no public address is configured.

For clients on other machines, supply their reachable server address once:

```sh
./build/wolfnode -issue-enrolment -invite-address node.example.net:13300 > "$HOME/client.invite"
```

Replace `node.example.net` with your server's public IP/hostname, or its LAN IP
for LAN-only hosting. That address is remembered for future invitations. Open
inbound TCP 13300 in the host/provider firewall, and forward it at the router if
necessary. Outbound DNS and public TCP 80/443 must work. Both input and pixels use
the node's TLS port; expose no browser debugging port. This is a native TLS
service, not a website to put behind an ordinary HTTP reverse proxy.

Issue a fresh invitation for each installation. Invitations expire after 30 days;
credentials last 90 days and renew on connect while still valid and near expiry.
For compatibility, `-issue-enrolment -code-only` emits a bare code for manual setup.

## 4. Keep it running after logout/reboot

Install the node at a stable path, then stop the foreground instance with Ctrl+C:

```sh
sudo install -m 0755 build/wolfnode /usr/local/bin/wolfnode
install -d -m 0700 "$HOME/.config/systemd/user"
cat > "$HOME/.config/systemd/user/wolfnode.service" <<'EOF_SERVICE'
[Unit]
Description=WolfTunnel browser node

[Service]
Type=simple
ExecStart=/usr/local/bin/wolfnode
Restart=on-failure
RestartSec=5
TimeoutStopSec=30
UMask=0077

[Install]
WantedBy=default.target
EOF_SERVICE
sudo loginctl enable-linger "$(id -un)"
systemctl --user daemon-reload
systemctl --user enable --now wolfnode.service
```

The user service reuses the saved defaults. [Lingering](https://www.freedesktop.org/software/systemd/man/latest/loginctl.html)
keeps its user manager available after logout and at boot. Run `systemctl --user`
as the node account, without `sudo`. If you use a custom `XDG_DATA_HOME` or legacy
state location, keep the service pointed to that same environment/location.
Installing just the executable may trigger a one-time private runtime download
if it previously used a renderer alongside the build executable.

Use these when administering the service:

```sh
journalctl --user -u wolfnode.service -n 30 --no-pager
systemctl --user restart wolfnode.service
systemctl --user stop wolfnode.service
```

They show startup errors, restart the node after a change, or stop it. Node audit
logs are in its state directory's `logs/` folder.

The current combined node log is `logs/node.log` under the state path printed at
startup (normally `~/.local/share/wolftunnel/node/logs/node.log`). It includes
browser startup stages, specific failures and remedies. Hourly JSONL files also
remain available; reading one older hourly file can miss the current failure.
If clients connect but pages cannot render, run `wolfnode -diagnose-browser` as
the node account with the same state location/environment as the service. See
[browser troubleshooting](docs/BROWSER-TROUBLESHOOTING.md) for each failure and fix.

## Optional: deploy the renderer with the node

```sh
make node-package
```

This builds `build/wolfnode-linux-amd64.tar.gz` on amd64 or
`build/wolfnode-linux-arm64.tar.gz` on ARM64, with the executable and its pinned
renderer. Extract it together on the destination host and run the included
`wolfnode`, leaving `renderer/` beside it. The node verifies the complete bundle
and uses it without a renderer download or system Chromium installation. The
Linux isolation/runtime-library requirements above still apply. Keep upstream
runtime notices with the bundle.

To package an ARM64 node on an amd64 development machine, use
`make node-package GOOS=linux GOARCH=arm64`. The installer runs on the build host
and provisions the ARM64 package without executing ARM64 code there. Transfer
the resulting ARM64 archive to the ARM64 host. This command replaces
`build/wolfnode` with the target binary; `make node` rebuilds the native version.

## Optional: adjust browser defaults

The generated browser configuration already enables pixel streaming. Typical
settings are two simultaneous sessions, one per client, four tabs per session,
15 fps, JPEG quality 82, up to 1920×1200, and 4 MiB/s per session. Browser memory
is capped at 1 GiB per session, CPU at 150% and tasks at 384. Start with roughly
4 GiB host RAM/two CPU cores for this small configuration; actual website load
may require more.

For example, the tunable portion of the generated `browser.json` can use:

```json
{
  "max_sessions": 2,
  "max_per_client": 1,
  "max_tabs": 4,
  "memory_mb": 1024,
  "total_memory_mb": 2048,
  "tasks": 384,
  "fps": 15,
  "quality": 82,
  "bandwidth": 4194304,
  "refresh": 2000000000
}
```

Edit settings in the generated file and restart the node. Values omitted from a
partial configuration use defaults; the managed renderer supplies its own
`binary` and `sha256`. Do not copy a system browser path or certificate pin into
those fields. Durations are nanoseconds, so `refresh` above means two seconds.
`total_memory_mb` must cover `max_sessions × memory_mb`. Keep configuration files
mode 0600 and their state directory mode 0700. No configuration edit is necessary
for ordinary use.

Renderer updates arrive with an updated WolfTunnel runtime manifest/release.
Keep WolfTunnel and the OS updated. A cache whose contents no longer match the
pinned manifest is rejected rather than silently used. Keep private state backed
up: deleting it changes the node identity and client authorisations. Certificates
last one year and require deliberate identity rotation and new client trust.

## Connect a Wolf Client and browse

1. Launch a WolfTunnel v0.6 client. First launch opens connection settings; paste
   the complete invitation and choose **Connect**. If building the desktop client,
   install Go 1.27+, a C compiler, pkg-config, SDL3 **3.4.18** and SDL3_ttf
   **3.2.2**, then use `make build` and `./build/wolfclient`.
   On an ARM64 desktop, use ARM64 builds of these libraries and compile locally;
   amd64 shared libraries cannot be used by an ARM64 client. If the distribution
   packages other SDL versions, build the pinned upstream releases first. See
   [native frontend dependencies](frontend/README.md) for the source build steps.
2. Enter a public HTTP/HTTPS URL and choose **Go**. The node runs the browser;
   the client displays its pixels. Mouse, keyboard, scrolling, tabs and resize
   operate the remote page. The client needs no local browser engine.
3. Later launches reconnect using saved credentials. Client state defaults to
   `~/.local/share/wolftunnel/client/`. Its timestamped diagnostic log is
   **`~/.local/share/wolftunnel/logs/client.log`**. Existing default installations
   keep their identity; custom state directories place logs under their own
   `logs/client.log`.

For a bare code, enter the node address and the owner's verified certificate pin
in manual setup. Pasted `host:port` addresses are accepted. A code pasted into the
invitation field switches to manual setup when necessary; the client never trusts
a discovered certificate automatically. Codes are only for new enrolments.

If a connection fails, open `client.log` for its timestamp, event, error code and
safe explanation. Connection refused, pin mismatch, missing approval and browser
errors are recorded without invitation secrets, browsing URLs or typed text.
To revoke an installation, use its public Client ID from Settings diagnostics:

```sh
wolfnode -revoke CLIENT_ID
```

A working node needs Linux amd64 or ARM64, the isolation/runtime dependencies listed above,
a maintained managed renderer, adequate resources, a reachable TCP address and
private persistent state. Missing isolation must be fixed on the host; never run
the browser as root or disable sandboxing to bypass an error.

Sessions are ephemeral: disconnecting discards their browser state. Defaults also
close idle sessions after two minutes and cap their runtime at 30 minutes. Audio,
file transfer and clipboard/profile sharing remain outside v0.6. Websites see the
node's egress IP; TLS protects browsing contents between client and node, while
timing, volume, logins and fingerprints still limit anonymity. The node remains
trusted.

## Managed-network connections

Direct **tls-tcp** is the default. If the client network requires an approved
explicit proxy, choose **http-connect** in client Settings and enter its
`http://proxy.example.net:8080` or `https://proxy.example.net:8443` address. Save,
then connect using the same invitation and verified node certificate pin.
Alternatively, set it once on the client:

```sh
wolfclient -transport http-connect -proxy https://proxy.example.net:8443
```

This saves the choice and opens the client. It requests a standard CONNECT
tunnel, then verifies the node's TLS 1.3 identity inside it. HTTPS proxies also
need a certificate trusted by the client's operating system. Proxy credentials,
PAC, SOCKS, NTLM and Kerberos authentication are unsupported. Environment proxy
variables are not used. A denied or unavailable proxy never triggers a direct
fallback. To return to direct connections, select **tls-tcp** in Settings and
clear the proxy field (or use `-transport tls-tcp -proxy ''`).

Connection establishment has a 20-second default budget, five-second stage
limits and two bounded retries of transient establishment failures. `-retries 0`
disables retries; the maximum is three. Authentication, browser actions and
identity failures are never replayed. A lost stream ends its browser session;
reconnect and navigate again to begin with fresh website state. **End session**
also closes the browser while retaining the installation's saved credentials.

A network administrator must approve the node destination, port and application.
Zscaler deployments can apply routing, endpoint, CONNECT and TLS-inspection
policies; selecting this transport does not approve an untrusted node. TLS
inspection that replaces the node certificate is rejected by its pin. Obtain an
approved policy compatible with end-to-end pinning rather than changing the pin
to an inspection certificate. Client diagnostics distinguish DNS, TCP, proxy,
TLS, identity, authentication, browser and display failures; a timeout alone does
not identify their cause.
