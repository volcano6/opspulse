package executor

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/volcano6/opspulse/internal/config"
)

// LogPathFor generates the standard log file path for a server operation and ensures the directory exists.
func LogPathFor(serverName string, timestamp time.Time) (string, error) {
	logDir := filepath.Join(config.DataDir(), "logs")
	if err := os.MkdirAll(logDir, 0o750); err != nil {
		return "", fmt.Errorf("failed to create logs directory: %w", err)
	}

	fileName := fmt.Sprintf("bootstrap-%s-%s.log", serverName, timestamp.Format("20060102T150405"))
	return filepath.Join(logDir, fileName), nil
}

// PrefixedWriter prefixes every new line of output with a tag (e.g. "[vps-01] ").
type PrefixedWriter struct {
	Prefix []byte
	Writer io.Writer
	buf    bytes.Buffer
}

// NewPrefixedWriter creates a new PrefixedWriter with the given prefix string.
func NewPrefixedWriter(prefix string, w io.Writer) *PrefixedWriter {
	return &PrefixedWriter{
		Prefix: []byte(prefix),
		Writer: w,
	}
}

// Write writes p to the underlying writer, prefixing lines.
func (pw *PrefixedWriter) Write(p []byte) (n int, err error) {
	total := len(p)
	for len(p) > 0 {
		idx := bytes.IndexByte(p, '\n')
		if idx >= 0 {
			line := p[:idx+1]
			p = p[idx+1:]

			if pw.buf.Len() > 0 {
				pw.buf.Write(line)
				if _, writeErr := pw.Writer.Write(append(pw.Prefix, pw.buf.Bytes()...)); writeErr != nil {
					return 0, writeErr
				}
				pw.buf.Reset()
			} else {
				if _, writeErr := pw.Writer.Write(append(pw.Prefix, line...)); writeErr != nil {
					return 0, writeErr
				}
			}
		} else {
			pw.buf.Write(p)
			break
		}
	}
	return total, nil
}

// Flush writes any remaining buffered data without a trailing newline.
func (pw *PrefixedWriter) Flush() error {
	if pw.buf.Len() > 0 {
		_, err := pw.Writer.Write(append(pw.Prefix, pw.buf.Bytes()...))
		pw.buf.Reset()
		return err
	}
	return nil
}
