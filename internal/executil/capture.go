// SPDX-License-Identifier: AGPL-3.0-or-later

package executil

import "bytes"

// captureCap bounds how much of a child process's stdout/stderr is retained per
// stream. Beyond it the stream is truncated (with a marker) so a pathological
// command (e.g. `yes`, `cat /dev/urandom`) cannot exhaust daemon memory (D13).
const captureCap = 8 << 20 // 8 MiB

// Capture is an io.Writer that accumulates output up to captureCap bytes, then
// appends a truncation marker once and discards further writes. It always
// reports the full write length so the child never sees a short-write error.
type Capture struct {
	buf       bytes.Buffer
	truncated bool
}

// Write appends p to the capture buffer, truncating once it would exceed the
// cap. The reported n is always len(p).
func (c *Capture) Write(p []byte) (int, error) {
	if c.truncated {
		return len(p), nil
	}
	room := captureCap - c.buf.Len()
	if len(p) > room {
		c.buf.Write(p[:room])
		c.buf.WriteString("\n...[output truncated]\n")
		c.truncated = true
		return len(p), nil
	}
	c.buf.Write(p)
	return len(p), nil
}

// String returns the captured output as text for result payloads. On Windows
// the bytes may be the console codepage rather than UTF-8 (PowerShell, cmd
// and Python on zh-CN write GBK to a redirected pipe); normalizeConsoleText
// transcodes those, because Go's JSON encoder would otherwise replace every
// non-ASCII byte with U+FFFD and the delegator would read mojibake for text
// that was fine on the executor.
func (c *Capture) String() string { return normalizeConsoleText(c.buf.Bytes()) }

// Bytes returns the captured output.
func (c *Capture) Bytes() []byte { return c.buf.Bytes() }
