# tsunderlay

tsunderlay keeps Tailscale working on macOS while a full-tunnel VPN is up.
It is a small root daemon that adds two routes and adds them again each time
the system or the VPN client deletes them.

tsunderlay is a stopgap. Each part exists because of one upstream defect.
When all defects are fixed, uninstall it (see [Retirement](#retirement)).

## The problem

A full-tunnel VPN of this type adds a split default: `0.0.0.0/1` and
`128.0.0.0/1` on a utun interface. The system default route stays on the
physical interface (for example `en0`).

On macOS, a Tailscale client binds its dials to the physical interface, so
that its traffic does not loop through its own tunnel. Under a split default,
the physical interface has no route to the internet that a bound socket can
use. The client cannot reach the control plane or a DERP relay, and the node
goes offline.

tsunderlay adds two routes:

| Route | Shape | Client that uses it |
| --- | --- | --- |
| R1 | a route scoped to the physical interface, for each /24 of the control server, through the LAN gateway | an embedded node, for example the Tailscale node in Rex. It uses R1 only to log in. After login, the [node attribute](#tailnet-policy-requirement) unbinds it. |
| R2 | a host route to the VPN gateway through the VPN interface | the system Tailscale app, together with the env file line that `install` adds |

The system app with `TS_BIND_TO_INTERFACE_BY_ROUTE=true` looks up the route
of each dial and binds to the interface of that route. The lookup fails for
a point-to-point VPN gateway. R2 makes the lookup succeed.

tsunderlay reads all values from the live routing table and from DNS on each
run. It stores no address. It acts only when it finds a split default on one
tunnel. When the two halves of the split default are on different tunnels,
it does not change routes.

## Tailnet policy requirement

The embedded node needs a node attribute in the tailnet policy. tsunderlay
does not set it. Edit the policy by hand, or in the repository that applies
your policy.

Give `https://tailscale.com/cap/debug-disable-bind-conn-to-interface` to the
embedded node. Select the node by a tag, not by its IP address:

```hujson
"nodeAttrs": [
  {
    "target": ["tag:rex"],
    "attr":   ["https://tailscale.com/cap/debug-disable-bind-conn-to-interface"],
  },
],
```

Replace `tag:rex` with the tag of your embedded node.

Why: R1 lets the node log in. After login, the attribute stops the node from
binding to the physical interface, so control, DERP, and peer traffic follow
the VPN.

Do not use the variant of this attribute with the `-apple-ext` suffix. Do not
give the attribute to the system app.

Risk: Tailscale does not support attributes with the `debug-` prefix. A
release can change or remove it. tsunderlay cannot see the attributes of a
node, so it cannot detect this. The symptom is the original problem: the
embedded node goes offline while the VPN is up.

## Install

Requirements: macOS and Go (the version in `go.mod`).

```sh
go install github.com/Nagato-Yuzuru/tsunderlay@latest
sudo tsunderlay install
```

If `sudo` cannot find the binary, give the full path:
`sudo "$(go env GOPATH)/bin/tsunderlay" install`.

Self-hosted control server (for example Headscale): give one `--control-url`
for each control server that a Tailscale node on this Mac uses. The flag
replaces the default `https://controlplane.tailscale.com`:

```sh
sudo tsunderlay install --control-url https://headscale.example.com
```

`install` does these steps. Each step changes only what differs:

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
- It adds `TS_BIND_TO_INTERFACE_BY_ROUTE=true` to the env file of the system
  Tailscale app:
  `/private/var/root/Library/Containers/io.tailscale.ipn.macsys.network-extension/Data/tailscaled-env.txt`.
  It skips this step when the system app is not installed.

When the env file changed, `install` prints the command that restarts the
network extension of the system app. `install` does not run it, because the
restart drops the system node for some seconds. Run it yourself:

```sh
sudo killall io.tailscale.ipn.macsys.network-extension
```

## Upgrade

Install the new version, then run `install` again:

```sh
go install github.com/Nagato-Yuzuru/tsunderlay@latest
sudo tsunderlay install
```

`go install` replaces only the binary in your Go bin directory. The daemon
runs its own copy, so the upgrade has no effect until `install` copies the new
binary and restarts the daemon. Until then, `check` exits with status 1.

## Check

`check` prints what the daemon sees and what it would change. It changes
nothing, and any user can run it:

```sh
tsunderlay check
```

Give the same `--control-url` flags as for `install`.

The output tells:

- `lan`: the physical interface and its gateway.
- `vpn`: the tunnel and the gateway of the split default, `none`, or
  `ambiguous` when the two halves are on different tunnels. When the state is
  ambiguous, the daemon does not change routes.
- `control`: the control hosts and their /24 prefixes. When a host does not
  resolve, the next line tells so, and the daemon keeps its control routes.
- `blocked`: a needed route that a route of yours holds, with the command that
  deletes your route. tsunderlay does not delete a route that it did not add,
  unless that route cannot work.
- `plan`: each route change as a `/sbin/route` command line, or `none`.
- The state of the installed daemon: `not installed`, a line that tells you
  to run `sudo tsunderlay install` when the installed copy differs from this
  binary, or no line when it is the same.

`check` also gives information and warnings:

- It lists the other tunnels.
- It warns when a route on another tunnel covers `100.64.0.0/10` (the
  Tailscale address range) or a control prefix. tsunderlay does not fix this
  overlap.
- It warns when a resolved control prefix is not in the list on the
  [Tailscale firewall page](https://tailscale.com/kb/1082/firewall-ports).
  The routes still come from DNS.

`check` also prints each [retirement](#retirement) part with the upstream
issue that retires it.

Exit status:

| Status | Meaning |
| --- | --- |
| 0 | No change is necessary. |
| 1 | A change is necessary, the VPN state is ambiguous, a control host does not resolve, a route is blocked, or the installed daemon differs from this binary or cannot be checked. |
| 2 | An error. |

## Logs

The daemon writes one JSON object on each line to `/var/log/tsunderlay.log`.
It writes a line at start (with its version), at exit, and for each run that
changes a route or fails. A run that changes nothing writes no line.
newsyslog rotates the file when it is 1024 KB or more, and keeps 5
archives.

[docs/telemetry.md](docs/telemetry.md) lists the fields.

## Uninstall

```sh
sudo tsunderlay uninstall
```

`uninstall` stops the daemon, removes the binary copy, the launchd job, and
the newsyslog rule, and deletes each route that the daemon added. The daemon
marks its routes with the `RTF_PROTO1` flag. Routes without the flag stay.

`uninstall` keeps:

- The env file line of the system app. The file belongs to the system app.
  `uninstall` tells you when the line is still there. To remove it, delete the line and restart the network
  extension.
- The log `/var/log/tsunderlay.log`, as a record. Without the newsyslog rule,
  nothing rotates it. Delete it by hand when you do not need it.

## Migration from the first script

The first script (`tailscale-astrill.sh`) added a default route scoped to
`en0`. With that route, every socket bound to `en0` bypasses the VPN, not
only Tailscale. tsunderlay did not add that route, so it does not delete it.

1. Run the uninstall step of the first script if its LaunchDaemon is loaded.
2. Delete its route:

   ```sh
   sudo route -n delete -ifscope en0 default
   ```

3. [Install](#install) tsunderlay.

## Retirement

Each part serves one client and has one upstream fix. The parts are
independent: the embedded node does not use R2, and the system app does not
use R1.

| Part | Serves | Upstream fix that retires it | How to confirm |
| --- | --- | --- | --- |
| R1 | embedded node (Rex) | Rex starts the node with interface binding off, or loads a cached netmap before its first control dial. Watch the Rex changelog. | Delete R1. Restart the server from the Rex app. The node logs in, and its first control connection uses the VPN address. |
| Node attribute | embedded node (Rex) | Same as R1. | Same as R1, with the attribute removed. |
| R2 | system app | The bind-by-route lookup finds the interface of a point-to-point gateway ([tailscale#21198](https://github.com/tailscale/tailscale/issues/21198)). | Delete R2. Restart the network extension. Login and DERP use the VPN address. |
| Env file line | system app | The network extension stops binding by default, and peers on the same LAN still connect directly ([tailscale#21198](https://github.com/tailscale/tailscale/issues/21198)). | Remove the line. Restart the network extension. Login works, and peers on the same LAN connect directly. |

To delete a route for a test, stop the daemon first, or it adds the route
again:

```sh
sudo launchctl bootout system/io.github.nagato-yuzuru.tsunderlay
```

`sudo tsunderlay install` starts the daemon again.

When all parts are retired, run `sudo tsunderlay uninstall` and remove the
node attribute from the policy.

## Development

Tools come from `mise.toml`. Run `mise install` once.

| Task | Does |
| --- | --- |
| `mise run lint` | golangci-lint |
| `mise run test` | unit tests |
| `mise run vm:e2e` | all end-to-end tests in a new tart guest. The guest is deleted after a pass and kept after a failure. |
| `mise run e2e` | the end-to-end tests as root on this host |

The end-to-end tests add routes and interfaces. They run only on a host that
the root-owned file `/var/db/tsunderlay-e2e-disposable` marks as disposable.
No task makes this file on your host. Use `vm:e2e` on a development Mac.

`prek run --all-files` runs the same checks as CI.
