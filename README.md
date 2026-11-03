# tsunderlay

On macOS, Tailscale goes offline when a full-tunnel VPN is on, both in the
Tailscale app and in apps that embed Tailscale, e.g. Rex. tsunderlay is a
small daemon that fixes this without turning off the VPN. It adds the routes
that Tailscale needs to reach its control server, and it adds them again after
each VPN reconnect, Wi-Fi change, or reboot.

## Symptoms

While the full-tunnel VPN is on:

- Tailscale cannot log in. This happens in the Tailscale app and in apps that
  embed Tailscale (tsnet), e.g. Rex.
- The Tailscale app shows `could not connect to the '<region>' relay server`.

Tailscale works again when the VPN is off. It breaks again after a VPN
reconnect, a Wi-Fi change, or a reboot.

Tested with an OpenVPN-based full-tunnel VPN, the Tailscale macOS app, and
Rex. Any VPN that adds `0.0.0.0/1` and `128.0.0.0/1` on a utun interface can
cause the same problem, e.g. an OpenVPN client with `redirect-gateway def1`.

## Why not a script

A one-time fix does not last. I first put the routes in a script. The script
worked until the next VPN reconnect, Wi-Fi change, or reboot. Then the VPN
client or macOS deleted the routes, the addresses changed, and I had to run
the script again.

Some workarounds add a default route scoped to `en0`
(`route add -ifscope en0 default <gateway>`). Tailscale then works, but all
of its traffic leaves through `en0` and bypasses the VPN. So does the traffic
of each other app that binds to `en0`. tsunderlay adds a scoped route only
for the addresses of the control server.

## Is this your problem?

These two checks are read-only and need no root.

1. Look for the split default:

   ```sh
   netstat -rn -f inet | grep -E '^(0/1|128\.0/1)'
   ```

   You have the problem when both lines are on the same utun interface (some
   columns removed here):

   ```text
   0/1                198.18.64.1        utun5
   128.0/1            198.18.64.1        utun5
   ```

2. Ask tsunderlay what it would change. This runs it without an install:

   ```sh
   go run github.com/Nagato-Yuzuru/tsunderlay@latest check
   ```

   Without Go, get the binary as in [step 1 of Install](#install), and run
   `./tsunderlay check`.

   The `plan:` lines show the routes that tsunderlay would add.

## Why it happens

Tailscale on macOS binds its connections to the physical interface (e.g.
`en0`). A full-tunnel VPN leaves no route for such a connection, so
Tailscale cannot reach its control server or a DERP relay.

tsunderlay adds one route for each kind of client:

- **R1, for apps that embed Tailscale (e.g. Rex):** a route on `en0` to the
  control server, used only to log in. After login, a
  [node attribute](#install) moves all
  traffic to the VPN.
- **R2, for the Tailscale macOS app:** a host route to the VPN gateway. With
  the env file line that `install` adds, the app then binds its connections
  to the VPN.

The VPN client and macOS delete these routes on each reconnect, Wi-Fi change,
or reboot. tsunderlay adds them again within about one second.

## Install

You need macOS.

1. **Get the binary.** Download it from the latest release:

   ```sh
   curl -fsSLO https://github.com/Nagato-Yuzuru/tsunderlay/releases/latest/download/tsunderlay-darwin-universal.tar.gz
   tar -xzf tsunderlay-darwin-universal.tar.gz
   ```

   The binary is not notarized by Apple. A file that you download with
   `curl` runs. If you download it with a browser, macOS blocks it: run
   `xattr -d com.apple.quarantine tsunderlay`.

   The daemon runs this file as root. To make sure that the release workflow
   of this repository built it from the tag, verify its attestation with the
   [GitHub CLI](https://cli.github.com):

   ```sh
   gh attestation verify tsunderlay-darwin-universal.tar.gz --repo Nagato-Yuzuru/tsunderlay
   ```

   Or build it from source with Go:

   ```sh
   go install github.com/Nagato-Yuzuru/tsunderlay@latest
   ```

   In the next steps, use `./tsunderlay` for the downloaded file. For the Go
   build, use `tsunderlay`, or the full path
   `"$(go env GOPATH)/bin/tsunderlay"` if `sudo` cannot find it.

2. **Install the daemon.**

   ```sh
   sudo tsunderlay install
   ```

   With a self-hosted control server (e.g. Headscale), give one
   `--control-url` for each control server that a node on this Mac uses:

   ```sh
   sudo tsunderlay install --control-url https://headscale.example.com
   ```

3. **If you use the Tailscale app:** restart its network extension. The node
   goes offline for a few seconds.

   ```sh
   sudo killall io.tailscale.ipn.macsys.network-extension
   ```

4. **If you use an app that embeds Tailscale (e.g. Rex):** the app runs its
   own Tailscale node, and it does not read the env file of the Tailscale
   app. So the setting must come from the tailnet: give the node a node
   attribute in your tailnet policy file, e.g.:

   ```hujson
   "nodeAttrs": [
     {
       "target": ["tag:rex"],
       "attr":   ["https://tailscale.com/cap/debug-disable-bind-conn-to-interface"],
     },
   ],
   ```

   The node gets the attribute from the control server only after it logs
   in. To log in, it still needs the route that tsunderlay adds. After that,
   the attribute moves all of its traffic to the VPN.

   For `target`, use a tag that you gave the node, or its Tailscale IP. A
   tagged node is no longer owned by your user, and the IP changes when the
   node registers again. Then restart the node (for Rex: restart the server
   from the Rex app).

   Do not give the attribute to the Tailscale app, and do not use the variant
   with the `-apple-ext` suffix.

   > [!WARNING]
   > Tailscale does not support `debug-` attributes. A release can change or
   > remove it. The embedded node then goes offline again under the VPN, and
   > tsunderlay cannot detect this.

5. **Verify.**

   ```sh
   tsunderlay check
   ```

   Expect `plan:    none` and exit status 0. For other output, see
   [Troubleshoot](docs/operations.md#troubleshoot).

> [!NOTE]
> If you added a default route scoped to `en0` as a workaround, delete it:
> `sudo route -n delete -ifscope en0 default`. `check` prints a `note:` line
> when it finds one.

### What install changes

`install` prints each change. You can run it again at any time: it changes
only what differs.

| What | Where |
| --- | --- |
| A root-owned copy of the binary. The daemon runs as root, so it does not run a binary that your user can change. | `/Library/PrivilegedHelperTools/io.github.nagato-yuzuru.tsunderlay` |
| The launchd job. It starts the daemon at boot and after a crash. | `/Library/LaunchDaemons/io.github.nagato-yuzuru.tsunderlay.plist` |
| The newsyslog rule that rotates the log. | `/private/etc/newsyslog.d/io.github.nagato-yuzuru.tsunderlay.conf` |
| The line `TS_BIND_TO_INTERFACE_BY_ROUTE=true` in the env file of the Tailscale app. Skipped when the app is not installed. | `/private/var/root/Library/Containers/io.tailscale.ipn.macsys.network-extension/Data/tailscaled-env.txt` |

## Upgrade

Get the new binary as in [step 1 of Install](#install), and install it
again:

```sh
sudo ./tsunderlay install
```

The daemon runs its own copy of the binary, so the upgrade has no effect until
this step. For a Go build, run `go install …@latest` and
`sudo tsunderlay install`.

## Uninstall

```sh
sudo tsunderlay uninstall
```

It removes the daemon and the routes that it added. For what it keeps, see
[Operations](docs/operations.md#uninstall).

## Not supported

- A VPN with a kill switch that blocks other traffic with pf. A route cannot
  help there.
- IPv6. tsunderlay handles IPv4 only.
- Two tunnels that each hold one half of the split default. `check` shows
  `vpn: ambiguous`, and tsunderlay changes nothing.
- A VPN that replaces the global default route. tsunderlay adds no R1, and the
  `lan` line of `check` shows `none`.

## When to remove tsunderlay

tsunderlay is a stopgap. Remove each part when its upstream fix ships:

- **R2 and the env file line (the Tailscale app):** when Tailscale on macOS
  uses the VPN interface under a full-tunnel VPN.
  [tailscale#16652](https://github.com/tailscale/tailscale/issues/16652)
  tracks this.
- **R1 and the node attribute (apps that embed Tailscale, e.g. Rex):** when
  the app starts its embedded node with interface binding off. The fix for
  tailscale#16652 can also fix this, after the app updates its Tailscale
  library. Watch the changelog of the app.

When both are fixed, run `sudo tsunderlay uninstall`, and remove the node
attribute from the tailnet policy.

To confirm a fix before you remove a part, see
[Operations](docs/operations.md#confirm-a-fix).

## Documentation

- [Operations](docs/operations.md): troubleshoot, logs, uninstall, and how to
  confirm an upstream fix.
- [Telemetry](docs/telemetry.md): the fields of the log.
- [Contributing](CONTRIBUTING.md): development and tests.
