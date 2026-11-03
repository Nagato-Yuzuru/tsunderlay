# tsunderlay

On macOS, Tailscale goes offline when a full-tunnel VPN puts `0.0.0.0/1` and
`128.0.0.0/1` on a utun interface. tsunderlay is a small daemon that keeps the
routes that Tailscale needs in place while the VPN is up.

## Symptoms

- The Tailscale app shows
  `could not connect to the '<region>' relay server` (for example
  `'Hong Kong'`) after the VPN connects or reconnects.
- A Tailscale dial that is bound to the physical interface fails with
  `connect: network is unreachable`.
- An app that embeds Tailscale (tsnet), for example Rex, cannot log in while
  the VPN is up. It logs in when the VPN is off.
- Tailscale works again after the VPN disconnects. It breaks again after a
  VPN reconnect, a Wi-Fi change, or a reboot.

Confirmed with Astrill (OpenVPN mode), the Tailscale macOS app, and the
embedded node of Rex. Other VPNs can cause the same problem: any VPN that
adds the two routes `0.0.0.0/1` and `128.0.0.0/1` on a utun interface, for
example an OpenVPN client with `redirect-gateway def1`.

## Why I wrote this

I use a full-tunnel VPN and Tailscale on the same Mac. Each time the VPN
connected, Tailscale went offline. After some debugging, I found the cause
and a set of routes that fixed it.

First, I put the fix in a shell script. The script worked, but only until the
next network change. Each VPN reconnect, Wi-Fi change, or reboot deleted the
routes, and Tailscale went offline again. Each time, I had to see the problem
and run the script again. The addresses also changed: the LAN gateway, the VPN
gateway, and the addresses of the control server. A fixed list of routes
could not work.

The script also added a default route scoped to `en0`. That route was too
wide: every socket bound to `en0` bypassed the VPN, not only Tailscale.

tsunderlay does the work of that script, but it does not stop. It runs as a
launchd daemon, watches the routing table, and adds the routes again within
about one second. It reads each address from the live state each time. It adds
only the narrow routes that Tailscale needs.

## Is this your problem?

These two checks are read-only and need no root.

1. Look for the split default:

   ```sh
   netstat -rn -f inet | grep -E '^(0/1|128\.0/1)'
   ```

   You have the problem shape when the output has both `0/1` and `128.0/1`
   on the same utun interface.

2. Ask tsunderlay what it would change (see [Install](#install) for how to get
   the binary):

   ```sh
   tsunderlay check
   ```

   The `plan:` lines show the routes that tsunderlay would add.

## Why it happens

On macOS, a Tailscale client binds its dials to the physical interface (for
example `en0`), so that its traffic does not loop through its own tunnel.
Under a split default, the physical interface has no route to the internet
that a bound socket can use. The client cannot reach the control server or a
DERP relay.

tsunderlay adds two routes:

| Route | Shape | Client that uses it |
| --- | --- | --- |
| R1 | a route scoped to the physical interface, for each /24 of the control server, through the LAN gateway | an embedded node (tsnet), for example Rex. It uses R1 only to log in. After login, the [node attribute](#4-embedded-node-add-the-node-attribute) unbinds it. |
| R2 | a host route to the VPN gateway through the VPN interface | the Tailscale macOS app, together with the env file line that `install` adds |

With `TS_BIND_TO_INTERFACE_BY_ROUTE=true`, the Tailscale app looks up the
route of each dial and binds to the interface of that route. The lookup fails
for a point-to-point VPN gateway. R2 makes the lookup succeed.

The VPN client and macOS delete these routes on a VPN reconnect, a Wi-Fi
change, or a reboot. tsunderlay watches the routing table and adds the routes
again within about one second. It reads all values from the live routing
table and from DNS each time, and it stores no address.

## Install

Requirements: macOS and Go (the version in `go.mod`).

### 1. Install the binary

```sh
go install github.com/Nagato-Yuzuru/tsunderlay@latest
```

Result: `tsunderlay` is in `$(go env GOPATH)/bin`. If `sudo` cannot find it
in the next steps, use the full path `"$(go env GOPATH)/bin/tsunderlay"`.

### 2. Install the daemon

```sh
sudo tsunderlay install
```

With a self-hosted control server (for example Headscale), give one
`--control-url` for each control server that a Tailscale node on this Mac
uses. The flag replaces the default `https://controlplane.tailscale.com`:

```sh
sudo tsunderlay install --control-url https://headscale.example.com
```

Result: `install` prints one line for each change. It does these steps, and
each step changes only what differs:

- It copies the binary to
  `/Library/PrivilegedHelperTools/io.github.nagato-yuzuru.tsunderlay`
  (`root:wheel`, mode `0755`). The daemon runs as root, so it must not run a
  binary that your user can change. The Go and Homebrew bin directories
  belong to your user. `install` refuses a path that a user other than root
  can change.
- It writes the launchd job
  `/Library/LaunchDaemons/io.github.nagato-yuzuru.tsunderlay.plist`. launchd
  starts the daemon at boot and after a crash.
- It writes the newsyslog rule
  `/private/etc/newsyslog.d/io.github.nagato-yuzuru.tsunderlay.conf`, which
  rotates the log.
- It loads the job, or restarts the daemon when the binary or the job changed.
- It adds `TS_BIND_TO_INTERFACE_BY_ROUTE=true` to the env file of the
  Tailscale app:
  `/private/var/root/Library/Containers/io.tailscale.ipn.macsys.network-extension/Data/tailscaled-env.txt`.
  It skips this step when the Tailscale app is not installed.

### 3. Tailscale app: restart the network extension

When the env file changed, `install` prints the restart command. `install`
does not run it. Run it yourself:

```sh
sudo killall io.tailscale.ipn.macsys.network-extension
```

Result: the Tailscale node of this Mac goes offline for a few seconds, and
then connects again with the new setting.

### 4. Embedded node: add the node attribute

An embedded node (tsnet, for example Rex) needs a node attribute in the
tailnet policy. tsunderlay does not set it. Give
`https://tailscale.com/cap/debug-disable-bind-conn-to-interface` to the
embedded node. Select the node by a tag, not by its IP address:

```hujson
"nodeAttrs": [
  {
    "target": ["tag:rex"],
    "attr":   ["https://tailscale.com/cap/debug-disable-bind-conn-to-interface"],
  },
],
```

Replace `tag:rex` with the tag of your embedded node. Do not use the variant
of this attribute with the `-apple-ext` suffix. Do not give the attribute to
the Tailscale app.

Result: R1 lets the node log in. After login, the attribute stops the node
from binding to the physical interface, so control, DERP, and peer traffic
follow the VPN.

Risk: Tailscale does not support attributes with the `debug-` prefix. A
release can change or remove it. tsunderlay cannot see the attributes of a
node, so it cannot detect this. The symptom is the original problem: the
embedded node goes offline while the VPN is up.

### 5. Verify

```sh
tsunderlay check
```

Result: `plan:    none`, and the exit status is 0. For other output, see
[Troubleshoot](#troubleshoot).

> [!NOTE]
> Did you add an interface-scoped default route on the physical interface by
> hand or with a script as a workaround (`route add -ifscope en0 default …`)?
> Delete it after the install:
>
> ```sh
> sudo route -n delete -ifscope en0 default
> ```
>
> With that route, every socket that is bound to `en0` bypasses the VPN, not
> only Tailscale. tsunderlay does not delete it, because tsunderlay did not
> add it, and macOS can also make such a route. `check` prints a `note:` line
> when it finds one.

## Operations

### Upgrade

1. Install the new version:

   ```sh
   go install github.com/Nagato-Yuzuru/tsunderlay@latest
   ```

2. Copy it to the daemon and restart the daemon:

   ```sh
   sudo tsunderlay install
   ```

   `go install` replaces only the binary in your Go bin directory. The daemon
   runs its own copy, so the upgrade has no effect until this step. Until
   then, `check` tells you that the installed daemon differs and exits with
   status 1.

3. Confirm the version. `tsunderlay --version` prints the version of the
   binary. The daemon writes its version in its start event in the
   [log](#logs).

### Troubleshoot

Run `tsunderlay check` as your user. Give the same `--control-url` flags as
for `install`. `check` changes nothing. It prints these lines in this order:
`lan`, `vpn`, `tunnels` (one line for each tunnel interface, with its routes,
and a mark on the tunnel of Tailscale and on the tunnel that tsunderlay
follows), `control`, the warnings, `blocked`, `plan` (each change as a
`/sbin/route` command line), `retire` (each part with the upstream defect
that [retires it](#when-to-remove-tsunderlay)), and the state of the installed
daemon. It can also print a `note:` line.

| `check` output | Meaning | What to do |
| --- | --- | --- |
| `plan:` is not `none` while the daemon is installed | The daemon did not add the routes yet, or it failed to add them. After a failure, it tries again after a wait of up to one minute. | Run `check` again after some seconds. If the plan stays, read the [log](#logs) for `ERROR` lines. |
| `blocked:` | A route that tsunderlay did not add holds the place of a needed route, and the route can work. tsunderlay does not delete it. | If you do not need that route, run the delete command on the line. |
| `vpn:     ambiguous` | `0/1` and `128.0/1` are on different tunnels. tsunderlay does not know which tunnel Tailscale uses, so it changes nothing. | Disconnect one of the VPNs. |
| `no answer` under `control:` | A control host did not resolve. The daemon keeps the control routes that it added. | Check DNS and the `--control-url` values. |
| `warning:` control prefix not in the list | A control prefix is not on the [Tailscale firewall page](https://tailscale.com/kb/1082/firewall-ports). `check` compares only when each control host is a host of Tailscale. | Compare the DNS answer with that page. The routes still come from DNS. |
| `warning:` a tunnel routes into `100.64.0.0/10` or a control prefix | Another VPN sends Tailscale addresses or the control server into its tunnel. tsunderlay does not fix this. | Change the routes of that VPN. |
| `note:` interface-scoped default | A default route is bound to the physical interface. Sockets bound to it bypass the VPN. | Delete it if you added it by hand: see the [note in Install](#5-verify). |
| `the installed daemon differs from this binary` | The daemon runs another version. | Run `sudo tsunderlay install`. |
| `not installed` | The daemon is not installed. This is not a failure. | Follow [Install](#install). |

The warnings and the `note:` line do not change the exit status.

| Exit status | Meaning |
| --- | --- |
| 0 | No change is necessary. |
| 1 | A change is necessary, the VPN state is ambiguous, a control host does not resolve, a route is blocked, or the installed daemon differs from this binary or cannot be checked. |
| 2 | An error. |

### Logs

The daemon writes one JSON object on each line to `/var/log/tsunderlay.log`.
It writes a line at start (with its version), at exit, and for each run that
changes a route or fails. A run that changes nothing writes no line.
newsyslog rotates the file when it is 1024 KB or more, and keeps 5 archives.
[docs/telemetry.md](docs/telemetry.md) lists the fields.

### Uninstall

```sh
sudo tsunderlay uninstall
```

`uninstall` stops the daemon, removes the binary copy, the launchd job, and
the newsyslog rule, and deletes each route that the daemon added. The daemon
marks its routes with the `RTF_PROTO1` flag. Routes without the flag stay.

`uninstall` keeps:

- The env file line of the Tailscale app. The file belongs to the Tailscale
  app. `uninstall` tells you when the line is still there. To remove it,
  delete the line and restart the network extension (step 3 of Install).
- The log `/var/log/tsunderlay.log`, as a record. Without the newsyslog rule,
  nothing rotates it. Delete it by hand when you do not need it.

## Not supported

- A VPN with a kill switch that blocks other traffic with pf. A route cannot
  help there.
- IPv6. tsunderlay handles IPv4 only.
- Two tunnels that each hold one half of the split default. `check` shows
  `vpn: ambiguous`, and tsunderlay changes nothing.
- A VPN that replaces the global default route. tsunderlay adds no R1, and the
  `lan` line of `check` shows `none`.

## When to remove tsunderlay

tsunderlay is a stopgap. Each part exists because of one upstream defect.
The parts are independent: the embedded node does not use R2, and the
Tailscale app does not use R1.

| Part | Serves | Upstream fix that retires it | How to confirm |
| --- | --- | --- | --- |
| R1 | embedded node (Rex) | Rex starts the node with interface binding off, or loads a cached netmap before its first control dial. Watch the Rex changelog. | Delete R1. Restart the server from the Rex app. The node logs in, and its first control connection uses the VPN address. |
| Node attribute | embedded node (Rex) | Same as R1. | Same as R1, with the attribute removed. |
| R2 | Tailscale app | The bind-by-route lookup finds the interface of a point-to-point gateway ([tailscale#21198](https://github.com/tailscale/tailscale/issues/21198)). | Delete R2. Restart the network extension. Login and DERP use the VPN address. |
| Env file line | Tailscale app | The network extension stops binding by default, and peers on the same LAN still connect directly ([tailscale#21198](https://github.com/tailscale/tailscale/issues/21198)). | Remove the line. Restart the network extension. Login works, and peers on the same LAN connect directly. |

To delete a route for a test, stop the daemon first, or it adds the route
again:

```sh
sudo launchctl bootout system/io.github.nagato-yuzuru.tsunderlay
```

`sudo tsunderlay install` starts the daemon again.

When all parts are retired, run `sudo tsunderlay uninstall`, and remove the
node attribute from the tailnet policy.

## Development

Tools come from `mise.toml`: run `mise install` once. `mise run lint` runs
golangci-lint, and `mise run test` runs the unit tests. `mise run vm:e2e` runs
the end-to-end tests in a new tart guest. The end-to-end tests change routes
and interfaces, so `mise run e2e` runs them on this host only when the
root-owned file `/var/db/tsunderlay-e2e-disposable` marks the host as
disposable. No task makes this file on your host. `prek run --all-files` runs
the same checks as CI.
