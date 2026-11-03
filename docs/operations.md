# Operations


## Upgrade

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

## Troubleshoot

Run `tsunderlay check` as your user. Give the same `--control-url` flags as
for `install`. `check` changes nothing. It prints these lines in this order:
`lan`, `vpn`, `tunnels` (one line for each tunnel interface, with its routes,
and a mark on the tunnel of Tailscale and on the tunnel that tsunderlay
follows), `control`, the warnings, `blocked`, `plan` (each change as a
`/sbin/route` command line), `retire` (each part with the upstream defect
that [retires it](../README.md#when-to-remove-tsunderlay)), and the state of the installed
daemon. It can also print a `note:` line.

| `check` output | Meaning | What to do |
| --- | --- | --- |
| `plan:` is not `none` while the daemon is installed | The daemon did not add the routes yet, or it failed to add them. After a failure, it tries again after a wait of up to one minute. | Run `check` again after some seconds. If the plan stays, read the [log](#logs) for `ERROR` lines. |
| `blocked:` | A route that tsunderlay did not add holds the place of a needed route, and the route can work. tsunderlay does not delete it. | If you do not need that route, run the delete command on the line. |
| `vpn:     ambiguous` | `0/1` and `128.0/1` are on different tunnels. tsunderlay does not know which tunnel Tailscale uses, so it changes nothing. | Disconnect one of the VPNs. |
| `no answer` under `control:` | A control host did not resolve. The daemon keeps the control routes that it added. | Check DNS and the `--control-url` values. |
| `warning:` control prefix not in the list | A control prefix is not on the [Tailscale firewall page](https://tailscale.com/kb/1082/firewall-ports). `check` compares only when each control host is a host of Tailscale. | Compare the DNS answer with that page. The routes still come from DNS. |
| `warning:` a tunnel routes into `100.64.0.0/10` or a control prefix | Another VPN sends Tailscale addresses or the control server into its tunnel. tsunderlay does not fix this. | Change the routes of that VPN. |
| `note:` interface-scoped default | A default route is bound to the physical interface. Sockets bound to it bypass the VPN. | Delete it if you added it by hand: see the [note in Install](../README.md#install). |
| `the installed daemon differs from this binary` | The daemon runs another version. | Run `sudo tsunderlay install`. |
| `not installed` | The daemon is not installed. This is not a failure. | Follow [Install](../README.md#install). |

The warnings and the `note:` line do not change the exit status.

| Exit status | Meaning |
| --- | --- |
| 0 | No change is necessary. |
| 1 | A change is necessary, the VPN state is ambiguous, a control host does not resolve, a route is blocked, or the installed daemon differs from this binary or cannot be checked. |
| 2 | An error. |

## Logs

The daemon writes one JSON object on each line to `/var/log/tsunderlay.log`.
It writes a line at start (with its version), at exit, and for each run that
changes a route or fails. A run that changes nothing writes no line.
newsyslog rotates the file when it is 1024 KB or more, and keeps 5 archives.
[docs/telemetry.md](telemetry.md) lists the fields.

## Uninstall

```sh
sudo tsunderlay uninstall
```

`uninstall` stops the daemon, removes the binary copy, the launchd job, and
the newsyslog rule, and deletes each route that the daemon added. The daemon
marks its routes with the `RTF_PROTO1` flag. Routes without the flag stay.

`uninstall` keeps:

- The env file line of the Tailscale app. The file belongs to the Tailscale
  app. `uninstall` tells you when the line is still there. To remove it,
  delete the line and restart the network extension ([step 3 of Install](../README.md#install)).
- The log `/var/log/tsunderlay.log`, as a record. Without the newsyslog rule,
  nothing rotates it. Delete it by hand when you do not need it.

## Confirm a fix

Stop the daemon first, or it adds the routes again:

```sh
sudo launchctl bootout system/io.github.nagato-yuzuru.tsunderlay
```

- Tailscale app: delete R2, remove the env file line, and restart the network
  extension ([step 3 of Install](../README.md#install)). Login and DERP work through the VPN, and
  peers on the same LAN connect directly.
- Embedded node: delete R1, remove the node attribute, and restart the node
  (for Rex: restart the server from the Rex app). The node logs in through
  the VPN.

`sudo tsunderlay install` starts the daemon again.
