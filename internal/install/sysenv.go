package install

import (
	"errors"
	"fmt"
	"io/fs"
	"strings"
)

// The environment file of the network extension of the system Tailscale app.
const (
	// SystemAppContainer is the container of the network extension. It
	// exists only when the system app ran once.
	SystemAppContainer = "/private/var/root/Library/Containers/io.tailscale.ipn.macsys.network-extension"
	// SystemAppEnvPath is the file with the environment of the network
	// extension. The extension reads it at its start.
	SystemAppEnvPath = SystemAppContainer + "/Data/tailscaled-env.txt"
	// SystemAppProcess is the process name for killall. launchd starts the
	// extension again after the kill.
	SystemAppProcess = "io.tailscale.ipn.macsys.network-extension"
)

// bindByRoute is the one setting that Install adds to the file of another
// app. The network extension binds each dial to the default interface. With
// this setting it looks up the route of each dial, so the dial follows the
// routes of this daemon. It is a stopgap part: underlay.Parts names the
// upstream issue that retires it. Remove the line when the extension stops
// binding by default.
const (
	bindByRouteKey = "TS_BIND_TO_INTERFACE_BY_ROUTE"
	bindByRoute    = bindByRouteKey + "=true"
)

// ErrEnvNotRegular means that the environment file of the system app is not
// a regular file. Install does not follow a link in a file of another app.
var ErrEnvNotRegular = errors.New("the environment file of the system app is not a regular file")

// setSystemAppEnv makes the environment file of the system app hold
// bindByRoute. It writes the file only when the content changes. It does
// nothing when the system app has no container.
func (in Installer) setSystemAppEnv(r *reporter) error {
	_, err := in.FS.Lstat(SystemAppContainer)
	if errors.Is(err, fs.ErrNotExist) {
		r.note("the system Tailscale app is not installed: skipped %s", SystemAppEnvPath)
		return nil
	}
	if err != nil {
		return fmt.Errorf("checking %s: %w", SystemAppContainer, err)
	}

	perm := plistPerm
	var old []byte
	switch st, err := in.FS.Lstat(SystemAppEnvPath); {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return fmt.Errorf("checking %s: %w", SystemAppEnvPath, err)
	case !st.Mode.IsRegular():
		return fmt.Errorf("%w: %s", ErrEnvNotRegular, SystemAppEnvPath)
	default:
		perm = st.Mode.Perm()
		if old, err = in.FS.ReadFile(SystemAppEnvPath); err != nil {
			return fmt.Errorf("reading %s: %w", SystemAppEnvPath, err)
		}
	}

	data, changed := withBindByRoute(old)
	if !changed {
		return nil
	}
	if err := in.FS.WriteFile(SystemAppEnvPath, data, perm); err != nil {
		return fmt.Errorf("writing the environment file of the system app: %w", err)
	}
	r.printf("wrote %s", SystemAppEnvPath)
	r.printf("restart the network extension: sudo killall %s", SystemAppProcess)
	return nil
}

// withBindByRoute returns content with the line bindByRoute, and tells
// whether it changed content. It keeps the other lines and their order. A
// line with the same key and another value gets the new value. Without such
// a line, the result ends with the new line.
func withBindByRoute(content []byte) ([]byte, bool) {
	var out []string
	found, changed := false, false
	if len(content) > 0 {
		for line := range strings.SplitSeq(strings.TrimSuffix(string(content), "\n"), "\n") {
			key, _, _ := strings.Cut(strings.TrimSpace(line), "=")
			if strings.TrimSpace(key) == bindByRouteKey {
				found = true
				if strings.TrimSpace(line) != bindByRoute {
					line, changed = bindByRoute, true
				}
			}
			out = append(out, line)
		}
	}
	if !found {
		out, changed = append(out, bindByRoute), true
	}
	if !changed {
		return content, false
	}
	return []byte(strings.Join(out, "\n") + "\n"), true
}
