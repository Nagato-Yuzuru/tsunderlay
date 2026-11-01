//go:build darwin

package main

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"sync"
	"syscall"
)

// logFile writes to the file of a descriptor that launchd opened at path
// (StandardErrorPath). At a rotation, newsyslog renames the file and makes a
// new one at path. launchd opens path only when it starts the job, and the
// rotation rule sends no signal, so without logFile the daemon writes to the
// rotated file until its next start. With compression, newsyslog then
// removes that file, and each line goes to a file that has no name.
//
// So before each write, logFile checks that path still names the file of
// the descriptor. When it does not, logFile opens the file at path and puts
// it on the descriptor with dup2. Thus also a write to the descriptor that
// does not go through logFile, for example a panic of the runtime, goes to
// the new file.
type logFile struct {
	mu   sync.Mutex
	f    *os.File
	fd   int
	path string
}

// followLog returns a writer to f that follows a rotation of the file at
// path. When f is not the file at path, for example a terminal, it returns
// f.
func followLog(f *os.File, path string) io.Writer {
	if same, err := isFileAt(f, path); err != nil || !same {
		return f
	}
	return &logFile{f: f, fd: int(f.Fd()), path: path}
}

// Write writes p to the file at path. When the reopen fails, for example
// between the rename and the create of newsyslog, it writes to the old file:
// the line is not lost, and the next write tries again.
func (l *logFile) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	_ = l.reopen() //nolint:errcheck // the line goes to the old file, and the log is the only place to report the error
	return l.f.Write(p)
}

func (l *logFile) reopen() error {
	same, err := isFileAt(l.f, l.path)
	if err != nil || same {
		return err
	}
	// No O_CREATE: newsyslog makes the new file with the owner and the mode
	// of the rule.
	nf, err := os.OpenFile(l.path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	defer nf.Close() //nolint:errcheck // a close of a file that has no write cannot lose data
	return syscall.Dup2(int(nf.Fd()), l.fd)
}

// isFileAt tells whether f is the file at path now. A missing file is not
// an error.
func isFileAt(f *os.File, path string) (bool, error) {
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
