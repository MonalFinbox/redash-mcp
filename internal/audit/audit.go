// Package audit records one JSON line per tool call, so that what an
// assistant asked Redash for can be reconstructed afterwards.
//
// A record carries the tool, the instance, the arguments, the outcome and
// the size of what was returned. It never carries result data, and the
// errors it records come from code that is tested never to include the key.
package audit

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Record struct {
	Time       string         `json:"time"`
	Tool       string         `json:"tool"`
	Instance   string         `json:"instance,omitempty"`
	Args       map[string]any `json:"args,omitempty"`
	Outcome    string         `json:"outcome"`
	Error      string         `json:"error,omitempty"`
	DurationMS int64          `json:"duration_ms"`
	Rows       int            `json:"rows,omitempty"`
	Bytes      int            `json:"bytes,omitempty"`
}

type Logger struct {
	mu     sync.Mutex
	w      io.Writer
	closer io.Closer
	now    func() time.Time
}

// New writes records to w. It is the seam tests use.
func New(w io.Writer, now func() time.Time) *Logger {
	if now == nil {
		now = time.Now
	}
	return &Logger{w: w, now: now}
}

// Open resolves an audit destination: "stderr", "off", or a file path. A
// file is created owner-only, and an existing one that group or others can
// read is refused, since arguments can include identifiers a user searched
// for.
//
// stdout is not an option: under the stdio transport it carries the
// protocol, and a stray line there corrupts the session.
func Open(dest string) (*Logger, error) {
	switch strings.TrimSpace(dest) {
	case "", "stderr":
		return New(os.Stderr, nil), nil
	case "off":
		return New(io.Discard, nil), nil
	case "stdout":
		return nil, fmt.Errorf("REDASH_AUDIT_LOG cannot be stdout: the MCP stdio transport owns it")
	}

	p := strings.TrimSpace(dest)
	if p == "~" || strings.HasPrefix(p, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, err
		}
		p = filepath.Join(home, strings.TrimPrefix(p, "~"))
	}

	f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return nil, fmt.Errorf("REDASH_AUDIT_LOG: %w", err)
	}
	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("REDASH_AUDIT_LOG: %w", err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		f.Close()
		return nil, fmt.Errorf("audit log %s is readable by group or others (mode %04o). Fix it with:\n    chmod 600 %s",
			p, fi.Mode().Perm(), p)
	}
	l := New(f, nil)
	l.closer = f
	return l, nil
}

// Log writes a record, stamping its time. A failed write is dropped rather
// than failing the tool call it describes.
func (l *Logger) Log(r Record) {
	r.Time = l.now().UTC().Format(time.RFC3339Nano)
	b, err := json.Marshal(r)
	if err != nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.w.Write(append(b, '\n'))
}

func (l *Logger) Close() error {
	if l.closer == nil {
		return nil
	}
	return l.closer.Close()
}
