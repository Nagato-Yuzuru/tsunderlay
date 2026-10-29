package rib

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"syscall"
)

// Watch sends a value on wake for each message on the routing socket, until
// ctx ends. It does not parse the messages: the receiver reads the full table
// again. When wake is full, Watch drops the value, because one pending wake
// covers any number of messages.
func Watch(ctx context.Context, wake chan<- struct{}) (err error) {
	fd, err := syscall.Socket(syscall.AF_ROUTE, syscall.SOCK_RAW, syscall.AF_UNSPEC)
	if err != nil {
		return fmt.Errorf("opening the routing socket: %w", err)
	}
	// A non-blocking descriptor joins the runtime poller, so Close unblocks
	// Read.
	if err := syscall.SetNonblock(fd, true); err != nil {
		_ = syscall.Close(fd) //nolint:errcheck // the SetNonblock error is the one to report
		return fmt.Errorf("setting the routing socket non-blocking: %w", err)
	}
	f := os.NewFile(uintptr(fd), "routing socket")
	closeOnce := sync.OnceValue(f.Close)
	stop := context.AfterFunc(ctx, func() {
		_ = closeOnce() //nolint:errcheck // the deferred call below reports the same error
	})
	defer func() {
		stop()
		if cerr := closeOnce(); cerr != nil && !errors.Is(cerr, os.ErrClosed) {
			err = errors.Join(err, fmt.Errorf("closing the routing socket: %w", cerr))
		}
	}()

	// A route message is smaller than this. A longer one is truncated, which
	// does not matter for a wake-up.
	buf := make([]byte, 4096)
	for {
		n, err := f.Read(buf)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return fmt.Errorf("reading the routing socket: %w", err)
		}
		if n >= rtmTypeOffset+1 && isLookup(buf[rtmTypeOffset]) {
			continue
		}
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

// rtmTypeOffset is the offset of the message type in every routing message
// header (rt_msghdr, if_msghdr, ifa_msghdr): after a 2-byte length and a
// 1-byte version.
const rtmTypeOffset = 3

// isLookup reports whether a message type is a lookup that does not change
// the table. macOS sends the lookups of all processes to every routing
// socket, and the system app with TS_BIND_TO_INTERFACE_BY_ROUTE makes one
// for each dial. A minute of normal use on the development Mac gave two
// messages, both RTM_MISS; a burst gave 44 RTM_GET in two seconds.
func isLookup(t byte) bool {
	switch t {
	case syscall.RTM_GET, syscall.RTM_GET2, syscall.RTM_MISS:
		return true
	default:
		return false
	}
}
