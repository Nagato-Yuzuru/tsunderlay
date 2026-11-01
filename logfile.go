//go:build darwin

package main

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sync"
	"syscall"
)

// logFile is the log of the daemon at path, on the descriptor of f (the
// standard error). Thus also a write that does not go through logFile, for
// example a panic of the runtime, goes to the log.
//
// newsyslog rotates the log with a link of path to path.0, and then renames
// a new file to path. path always names a file, but the descriptor keeps
// the old file, now path.0, and the rotation rule sends no signal. So before
// each write, logFile checks that path still names the file of the
// descriptor. When it does not, logFile opens path and puts it on the
// descriptor with dup2.
//
// A write to the descriptor after a rotation and before the next write of
// logFile still goes to path.0. The rotation rule does not compress, so
// such a write, for example a crash report of the runtime, stays readable
// there.
type logFile struct {
	mu   sync.Mutex
	f    *os.File
	fd   int
	path string
}

// openLog opens the file at path, puts it on the descriptor of f, and
// returns a writer to f that follows a rotation of the file at path.
func openLog(f *os.File, path string) (io.Writer, error) {
	l := &logFile{f: f, fd: int(f.Fd()), path: path}
	if err := l.open(); err != nil {
		return nil, fmt.Errorf("opening the log %s: %w", path, err)
	}
	return l, nil
}

// Write writes p to the file at path. When the reopen fails, it writes to
// the old file: the line is not lost, and the next write tries again.
func (l *logFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if same, err := namesFile(l.path, l.f); err == nil && !same {
		_ = l.open() //nolint:errcheck // the line goes to the old file, and the log is the only place to report the error
	}
	return l.f.Write(p)
}

// open opens path and puts it on the descriptor. O_CREATE makes the file
// again when an operator removed or moved it.
func (l *logFile) open() error {
	nf, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return err
	}
	defer nf.Close() //nolint:errcheck // a close of a file that has no write cannot lose data
	return syscall.Dup2(int(nf.Fd()), l.fd)
}

// namesFile tells whether path names the file of f now. It follows a
// symbolic link in path, the same as the open. A missing file is not an
// error.
func namesFile(path string, f *os.File) (bool, error) {
	held, err := f.Stat()
	if err != nil {
		return false, err
	}
	now, err := os.Stat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return os.SameFile(held, now), nil
}
