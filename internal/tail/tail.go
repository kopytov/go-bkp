// Package tail keeps the end of a stream, such as the stderr of a process,
// to quote it in an error message.
package tail

import (
	"bytes"
	"unicode/utf8"
)

// Buffer keeps about the last max bytes written to it. It is not safe for
// concurrent use.
type Buffer struct {
	b       []byte
	max     int
	dropped bool // the start of what was written is gone
}

// New returns a Buffer that keeps the last n bytes.
func New(n int) *Buffer {
	return &Buffer{max: n}
}

// Write keeps the end of p and never fails.
func (t *Buffer) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if len(t.b) > 2*t.max {
		t.b = append(t.b[:0], t.b[len(t.b)-t.max:]...)
		t.dropped = true
	}
	return len(p), nil
}

// String returns the last max bytes, cut at a character boundary and
// trimmed of surrounding white space, for an error message. A cut start is
// marked with "…", and nothing written reads as "(empty)".
func (t *Buffer) String() string {
	b := bytes.TrimRight(t.b, " \t\r\n")
	dropped := t.dropped || len(b) > t.max
	if len(b) > t.max {
		b = b[len(b)-t.max:]
	}
	if dropped {
		for len(b) > 0 && !utf8.RuneStart(b[0]) {
			b = b[1:]
		}
	}
	b = bytes.TrimSpace(b)
	switch {
	case len(b) == 0:
		return "(empty)"
	case dropped:
		return "…" + string(b)
	}
	return string(b)
}
