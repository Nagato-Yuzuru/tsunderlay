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

Run `tsunderlay check` as your user, with the same `--control-url` flags as
for `install`. It changes nothing.

When all is well, the output looks like this, with exit status 0:

```text
lan:     en0 via 192.168.2.1
vpn:     utun5 via 198.18.64.1
tunnels: utun5: global 0/1 and 128.0/1; routes 198.18.64.0/20; tsunderlay follows it
control: controlplane.tailscale.com -> 192.200.0.0/24
plan:    none
retire:  R1 (scoped control routes, for the Rex node): ...
```

- `lan` and `vpn`: the physical interface and the VPN that tsunderlay found.
- `tunnels`: each tunnel interface and its routes.
- `control`: the addresses of the control server.
- `plan`: the routes that tsunderlay would add. `none` means that all routes
  are in place.
- `retire`: the upstream fix that retires each part. See
  [When to remove](../README.md#when-to-remove-tsunderlay).

Other lines:

| Line | Meaning | What to do |
| --- | --- | --- |
| `plan:` with `/sbin/route` lines, while the daemon runs | The daemon did not add the routes yet, or it failed. After a failure, it tries again within one minute. | Run `check` again after some seconds. If the plan stays, look for `ERROR` lines in the [log](#logs). |
| `blocked:` | A route that tsunderlay did not add is in the place of a needed route. tsunderlay does not delete it. | If you do not need that route, run the delete command on the line. |
| `vpn:     ambiguous` | `0/1` and `128.0/1` are on different tunnels. tsunderlay changes nothing. | Use one full-tunnel VPN at a time. |
| `no answer` under `control:` | A control host did not resolve. The daemon keeps the routes that it added before. | Check DNS and the `--control-url` values. |
| `warning: control prefix … is not in the list of …` | DNS returned an address that is not on the [Tailscale firewall page](https://tailscale.com/kb/1082/firewall-ports). | Make sure that your DNS is not filtered or changed by a network or a VPN. If the page is out of date, ignore the warning. |
| `warning: <iface> routes … into …` | Another VPN sends Tailscale addresses (`100.64.0.0/10`) or the control server into its tunnel. tsunderlay does not fix this. | Exclude these prefixes from that VPN, or turn it off. |
| `note:` | A default route scoped to the physical interface. Each socket bound to it bypasses the VPN. | If you added it as a workaround, run the delete command on the line. |
| `the installed daemon differs from this binary` | The daemon runs another version. | Run `sudo tsunderlay install`. |
| `not installed` | The daemon is not installed. This is not an error. | Follow [Install](../README.md#install). |

Warnings and the `note:` line do not change the exit status.

| Exit status | Meaning |
| --- | --- |
| 0 | No change is necessary. |
| 1 | A change is necessary, or a line above other than a warning or `note:` needs your action. |
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
